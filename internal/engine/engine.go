// Package engine owns each instrument's single command-processing goroutine.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/matchcore/matchcore-go/internal/core"
	"github.com/matchcore/matchcore-go/internal/observability"
	"github.com/matchcore/matchcore-go/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

var ErrUnavailable = errors.New("engine unavailable; recovery required")
var ErrQueueFull = errors.New("instrument command queue full")

type request struct {
	ctx      context.Context
	command  store.Command
	response chan response
}
type response struct {
	env store.Envelope
	err error
}
type worker struct {
	book  *core.Book
	epoch int64
	owner *pgxpool.Conn
	queue chan request
	depth atomic.Pointer[core.Depth]
	safe  atomic.Bool
}
type Engine struct {
	store   *store.Store
	workers map[string]*worker
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	ready   atomic.Bool
}
type State struct {
	Instrument string `json:"instrument"`
	Sequence   int64  `json:"sequence"`
	Queue      int    `json:"queue_depth"`
	Capacity   int    `json:"queue_capacity"`
	Safe       bool   `json:"safe"`
}

func New(ctx context.Context, s *store.Store, capacity int) (*Engine, error) {
	if capacity < 1 {
		return nil, errors.New("queue capacity must be positive")
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &Engine{store: s, workers: map[string]*worker{}, ctx: ctx, cancel: cancel}
	for _, i := range core.Instruments {
		start := time.Now()
		owner, epoch, err := s.Claim(ctx, i.ID)
		if err != nil {
			e.Close()
			return nil, err
		}
		w := &worker{owner: owner, epoch: epoch, queue: make(chan request, capacity)}
		e.workers[i.ID] = w
		w.book, err = s.Recover(ctx, i.ID)
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("recover %s: %w", i.ID, err)
		}
		w.safe.Store(true)
		d := w.book.Depth(100001)
		w.depth.Store(&d)
		observability.Recovery.Observe(time.Since(start).Seconds())
		observability.Safe.WithLabelValues(i.ID).Set(1)
	}
	e.ready.Store(true)
	for id, w := range e.workers {
		e.wg.Add(1)
		go e.run(id, w)
	}
	return e, nil
}
func (e *Engine) Ready() bool { return e.ready.Load() && e.ctx.Err() == nil }
func (e *Engine) Close() {
	e.ready.Store(false)
	e.cancel()
	e.wg.Wait()
	for id, w := range e.workers {
		if w.owner != nil {
			ctx, cancel := store.Timeout()
			// Await the server's unlock acknowledgement before closing the socket;
			// TCP teardown alone can leave a brief lock-release race on restart.
			if _, err := w.owner.Exec(ctx, "SELECT pg_advisory_unlock(hashtextextended($1,0))", id); err != nil {
				slog.Warn("owner unlock failed", "instrument", id, "error", err)
			}
			_ = w.owner.Conn().Close(ctx)
			cancel()
			w.owner.Release()
			w.owner = nil
		}
	}
}
func (e *Engine) States() []State {
	out := make([]State, 0, len(e.workers))
	for _, i := range core.Instruments {
		w := e.workers[i.ID]
		d := w.depth.Load()
		seq := int64(0)
		if d != nil {
			seq = d.Sequence
		}
		out = append(out, State{i.ID, seq, len(w.queue), cap(w.queue), w.safe.Load()})
	}
	return out
}
func (e *Engine) Depth(id string) (core.Depth, error) {
	w, ok := e.workers[id]
	if !ok {
		return core.Depth{}, errors.New("unknown instrument")
	}
	if !e.Ready() || !w.safe.Load() {
		return core.Depth{}, ErrUnavailable
	}
	d := *w.depth.Load()
	d.Bids = append([]core.Level{}, d.Bids...)
	d.Asks = append([]core.Level{}, d.Asks...)
	return d, nil
}
func (e *Engine) Submit(ctx context.Context, c store.Command) (store.Envelope, error) {
	observability.Commands.Inc()
	start := time.Now()
	defer func() { observability.CommandLatency.Observe(time.Since(start).Seconds()) }()
	w, ok := e.workers[c.Instrument]
	if !ok {
		return store.Envelope{}, errors.New("unknown instrument")
	}
	if !e.Ready() || !w.safe.Load() {
		return store.Envelope{}, ErrUnavailable
	}
	r := request{ctx, c, make(chan response, 1)}
	select {
	case <-ctx.Done():
		return store.Envelope{}, ctx.Err()
	case <-e.ctx.Done():
		return store.Envelope{}, ErrUnavailable
	case w.queue <- r:
	default:
		return store.Envelope{}, ErrQueueFull
	}
	select {
	case out := <-r.response:
		return out.env, out.err
	case <-ctx.Done():
		return store.Envelope{}, ctx.Err()
	case <-e.ctx.Done():
		return store.Envelope{}, ErrUnavailable
	}
}
func (e *Engine) unsafe(id string, w *worker, err error) {
	w.safe.Store(false)
	e.ready.Store(false)
	observability.Safe.WithLabelValues(id).Set(0)
	slog.Error("matcher stopped; restart required", "instrument", id, "error", err)
}
func (e *Engine) run(id string, w *worker) {
	defer e.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			if w.safe.Load() {
				ctx, cancel := store.Timeout()
				if err := e.store.Snapshot(ctx, w.book); err != nil {
					slog.Error("shutdown snapshot failed", "error", err)
				}
				cancel()
			}
			return
		case <-ticker.C:
			observability.Queue.WithLabelValues(id).Set(float64(len(w.queue)))
			ctx, cancel := context.WithTimeout(e.ctx, time.Second)
			err := w.owner.Ping(ctx)
			cancel()
			if err != nil {
				e.unsafe(id, w, err)
			}
		case r := <-w.queue:
			if !w.safe.Load() || !e.Ready() {
				r.response <- response{err: ErrUnavailable}
				continue
			}
			if r.ctx.Err() != nil {
				r.response <- response{err: r.ctx.Err()}
				continue
			}
			env, err := e.process(r.ctx, w, r.command)
			if errors.Is(err, store.ErrAmbiguous) || errors.Is(err, store.ErrStale) {
				e.unsafe(id, w, err)
			}
			r.response <- response{env, err}
		}
	}
}
func (e *Engine) process(ctx context.Context, w *worker, c store.Command) (store.Envelope, error) {
	ctx, span := otel.Tracer("matchcore/engine").Start(ctx, "command")
	defer span.End()
	existing, err := e.store.Lookup(ctx, c)
	if err != nil {
		return store.Envelope{}, err
	}
	if existing != nil {
		return *existing, nil
	}
	seq := w.book.Sequence + 1
	now := time.Now().UnixMilli()
	var p core.Plan
	rejection := ""
	matchStart := time.Now()
	switch c.Kind {
	case "place":
		p, err = w.book.Plan(c.Order, seq, now)
	case "cancel":
		p, err = w.book.Cancel(c.OrderID, c.Account, now)
	default:
		err = errors.New("unsupported command")
	}
	observability.MatchingLatency.Observe(time.Since(matchStart).Seconds())
	if err != nil {
		rejection = err.Error()
		p = core.Plan{Changes: []core.Change{}, Trades: []core.Trade{}}
	}
	if rejection == "" {
		count := w.book.Len()
		for _, ch := range p.Changes {
			if ch.Before != nil && ch.Before.Open() {
				count--
			}
			if ch.After.Open() {
				count++
			}
		}
		if count > 100000 {
			rejection = "instrument open-order capacity exceeded"
			p = core.Plan{Changes: []core.Change{}, Trades: []core.Trade{}}
		}
	}
	start := time.Now()
	env, err := e.store.Commit(ctx, c, p, rejection, seq, w.epoch, now)
	observability.DBLatency.Observe(time.Since(start).Seconds())
	if err != nil {
		observability.Failures.Inc()
		return env, err
	}
	// A duplicate committed on another instrument returns its original sequence.
	if env.Instrument != w.book.Instrument || env.Sequence <= w.book.Sequence {
		return env, nil
	}
	if err = w.book.Apply(env.Plan, env.Sequence); err != nil {
		return env, fmt.Errorf("%w: apply: %v", store.ErrAmbiguous, err)
	}
	d := w.book.Depth(100001)
	w.depth.Store(&d)
	observability.Depth.WithLabelValues(w.book.Instrument).Set(float64(w.book.Len()))
	observability.Trades.Add(float64(len(env.Plan.Trades)))
	observability.Attempts.Add(float64(p.Attempts))
	if env.Error != "" {
		observability.Orders.WithLabelValues("rejected").Inc()
	} else {
		observability.Orders.WithLabelValues("accepted").Inc()
	}
	slog.Info("command committed", "instrument", c.Instrument, "sequence", env.Sequence, "correlation_id", c.Key, "trace_id", trace.SpanContextFromContext(ctx).TraceID().String())
	if seq%100 == 0 {
		snapCtx, cancel := store.Timeout()
		err = e.store.Snapshot(snapCtx, w.book)
		cancel()
		if err != nil {
			slog.Error("snapshot failed", "error", err)
		}
	}
	return env, nil
}
