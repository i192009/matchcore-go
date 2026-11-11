package main

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/matchcore/matchcore-go/internal/observability"
	"github.com/matchcore/matchcore-go/internal/store"
	"github.com/matchcore/matchcore-go/internal/transport"
	"github.com/nats-io/nats.go"
)

func main() {
	if e := run(); e != nil {
		slog.Error("publisher failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := transport.SignalContext()
	defer cancel()
	shutdown, e := observability.Setup(ctx, "publisher")
	if e != nil {
		return e
	}
	defer func() { stop, c := store.Timeout(); defer c(); _ = shutdown(stop) }()
	db, e := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Pool.Close()
	nc, js, e := transport.ConnectNATS(transport.Env("NATS_URL", "nats://localhost:4222"))
	if e != nil {
		return e
	}
	defer nc.Close()
	var ready atomic.Bool
	ready.Store(true)
	httpErr := make(chan error, 1)
	go func() {
		httpErr <- transport.Serve(ctx, ":8082", transport.HealthMux(func() bool { return ready.Load() && nc.IsConnected() }))
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-httpErr:
			return e
		case <-ticker.C:
			batchCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			_, err := db.PublishBatch(batchCtx, func(id, instrument string, payload []byte) error {
				m := nats.NewMsg("matchcore.events." + instrument)
				m.Data = payload
				m.Header.Set(nats.MsgIdHdr, id)
				_, err := js.PublishMsg(m, nats.Context(batchCtx))
				return err
			})
			stop()
			ready.Store(err == nil)
			if err != nil {
				slog.Warn("outbox publish failed; will retry", "error", err)
			}
		}
	}
}
