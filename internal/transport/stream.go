package transport

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	pb "github.com/matchcore/matchcore-go/api/gen/matchcore/v1"
	"github.com/matchcore/matchcore-go/internal/core"
	"github.com/matchcore/matchcore-go/internal/observability"
	"github.com/matchcore/matchcore-go/internal/store"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/metadata"
)

type subscription struct {
	syncing bool
	pending []store.Envelope
}
type streamClient struct {
	identity Principal
	token    string
	subs     map[string]*subscription
	queue    chan any
	cancel   context.CancelFunc
}
type Hub struct {
	mu      sync.Mutex
	clients map[*streamClient]bool
	API     pb.ExchangeClient
	Auth    *Auth
	Origins []string
}

func NewHub(api pb.ExchangeClient, auth *Auth, origins []string) *Hub {
	return &Hub{clients: map[*streamClient]bool{}, API: api, Auth: auth, Origins: origins}
}

type levelDelta struct {
	Side  core.Side `json:"side"`
	Price int64     `json:"price"`
	Delta int64     `json:"delta"`
}
type publicTrade struct {
	ID        string `json:"id"`
	Price     int64  `json:"price"`
	Quantity  int64  `json:"quantity"`
	Timestamp int64  `json:"timestamp"`
}

func streamDelta(env store.Envelope, account string) map[string]any {
	levels := map[string]*levelDelta{}
	orders := []core.Order{}
	trades := []publicTrade{}
	add := func(o core.Order, mult int64) {
		if !o.Open() {
			return
		}
		key := string(o.Side) + "/" + strings.TrimSpace(jsonNumber(o.Price))
		if levels[key] == nil {
			levels[key] = &levelDelta{Side: o.Side, Price: o.Price}
		}
		levels[key].Delta += mult * o.Remaining
	}
	for _, c := range env.Plan.Changes {
		if c.Before != nil {
			add(*c.Before, -1)
		}
		add(c.After, 1)
		if c.After.Account == account {
			orders = append(orders, c.After)
		}
	}
	keys := make([]string, 0, len(levels))
	for k := range levels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changes := []levelDelta{}
	for _, k := range keys {
		if levels[k].Delta != 0 {
			changes = append(changes, *levels[k])
		}
	}
	for _, t := range env.Plan.Trades {
		trades = append(trades, publicTrade{t.ID, t.Price, t.Quantity, t.Timestamp})
	}
	var volume, notional int64
	for _, t := range env.Plan.Trades {
		volume += t.Quantity
		notional += t.Notional
	}
	return map[string]any{"type": "delta", "instrument": env.Instrument, "sequence": env.Sequence, "timestamp": env.Timestamp, "levels": changes, "trades": trades, "orders": orders, "statistics": map[string]any{"executions": len(trades), "volume_lots": volume, "notional_cents": notional}}
}
func jsonNumber(n int64) string { v, _ := json.Marshal(n); return string(v) }
func (h *Hub) enqueue(c *streamClient, v any) {
	select {
	case c.queue <- v:
	default:
		c.cancel()
	}
}
func (h *Hub) Broadcast(env store.Envelope) {
	observability.EventDelay.Observe(max(time.Since(time.UnixMilli(env.Timestamp)).Seconds(), 0))
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if sub := c.subs[env.Instrument]; sub != nil {
			if sub.syncing {
				if len(sub.pending) >= 128 {
					c.cancel()
				} else {
					sub.pending = append(sub.pending, env)
				}
			} else {
				h.enqueue(c, streamDelta(env, c.identity.Account))
			}
		}
	}
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	allowed := false
	for _, o := range h.Origins {
		if origin == o {
			allowed = true
		}
	}
	if !allowed {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	h.mu.Lock()
	full := len(h.clients) >= 1000
	h.mu.Unlock()
	if full {
		http.Error(w, "stream capacity reached", http.StatusServiceUnavailable)
		return
	}
	conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originHosts(h.Origins)})
	if e != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "closed") }()
	conn.SetReadLimit(4096)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	authCtx, authCancel := context.WithTimeout(ctx, 5*time.Second)
	var first struct {
		Op    string `json:"op"`
		Token string `json:"token"`
	}
	e = wsjson.Read(authCtx, conn, &first)
	authCancel()
	if e != nil || first.Op != "auth" {
		return
	}
	p, e := h.Auth.Check(first.Token)
	if e != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}
	c := &streamClient{identity: p, token: first.Token, subs: map[string]*subscription{}, queue: make(chan any, 128), cancel: cancel}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	observability.Clients.Inc()
	defer func() { h.mu.Lock(); delete(h.clients, c); h.mu.Unlock(); observability.Clients.Dec() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case v := <-c.queue:
				writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := wsjson.Write(writeCtx, conn, v)
				stop()
				if err != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(); _ = conn.CloseNow(); <-done }()
	h.enqueue(c, map[string]any{"type": "authenticated", "account": p.Account})
	for ctx.Err() == nil {
		var msg struct {
			Op         string   `json:"op"`
			Instrument string   `json:"instrument"`
			Account    string   `json:"account"`
			Channels   []string `json:"channels"`
		}
		if e = wsjson.Read(ctx, conn, &msg); e != nil {
			return
		}
		if _, e = h.Auth.Check(c.token); e != nil {
			return
		}
		if msg.Account != "" && msg.Account != p.Account {
			_ = conn.Close(websocket.StatusPolicyViolation, "private account access denied")
			return
		}
		if _, e = core.FindInstrument(msg.Instrument); e != nil {
			h.enqueue(c, map[string]any{"type": "error", "error": "unknown instrument"})
			continue
		}
		if msg.Op == "unsubscribe" {
			h.mu.Lock()
			delete(c.subs, msg.Instrument)
			h.mu.Unlock()
			continue
		}
		if msg.Op != "subscribe" {
			h.enqueue(c, map[string]any{"type": "error", "error": "unsupported operation"})
			continue
		}
		// This gateway delivers the combined depth/BBO/trade/last/statistics and own-order stream.
		for _, ch := range msg.Channels {
			if ch != "depth" && ch != "bbo" && ch != "trades" && ch != "last" && ch != "statistics" && ch != "orders" {
				h.enqueue(c, map[string]any{"type": "error", "error": "unsupported channel"})
				return
			}
		}
		h.mu.Lock()
		c.subs[msg.Instrument] = &subscription{syncing: true}
		h.mu.Unlock()
		snapCtx, stop := context.WithTimeout(metadata.AppendToOutgoingContext(ctx, "authorization", c.token), 5*time.Second)
		res, err := h.API.GetOrderBook(snapCtx, &pb.Query{Instrument: msg.Instrument})
		stop()
		if err != nil {
			h.enqueue(c, map[string]any{"type": "error", "error": "snapshot unavailable"})
			return
		}
		var d core.Depth
		if e = json.Unmarshal(res.Json, &d); e != nil {
			return
		}
		h.mu.Lock()
		h.enqueue(c, map[string]any{"type": "snapshot", "instrument": d.Instrument, "sequence": d.Sequence, "timestamp": time.Now().UnixMilli(), "depth": d})
		sub := c.subs[msg.Instrument]
		for _, env := range sub.pending {
			if env.Sequence > d.Sequence {
				h.enqueue(c, streamDelta(env, p.Account))
			}
		}
		sub.pending = nil
		sub.syncing = false
		h.mu.Unlock()
	}
}
func originHosts(origins []string) []string {
	hosts := make([]string, 0, len(origins))
	for _, o := range origins {
		o = strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
		hosts = append(hosts, o)
	}
	return hosts
}

func ConnectNATS(url string) (*nats.Conn, nats.JetStreamContext, error) {
	nc, e := nats.Connect(url, nats.Timeout(3*time.Second), nats.MaxReconnects(-1))
	if e != nil {
		return nil, nil, e
	}
	js, e := nc.JetStream()
	if e != nil {
		nc.Close()
		return nil, nil, e
	}
	_, e = js.AddStream(&nats.StreamConfig{Name: "MATCHCORE", Subjects: []string{"matchcore.events.>"}, Storage: nats.FileStorage, Retention: nats.LimitsPolicy, MaxBytes: 1 << 30, Discard: nats.DiscardNew, Duplicates: 10 * time.Minute})
	if e != nil {
		info, lookupErr := js.StreamInfo("MATCHCORE")
		if lookupErr != nil || info == nil {
			nc.Close()
			return nil, nil, e
		}
	}
	return nc, js, nil
}
func Consume(ctx context.Context, js nats.JetStreamContext, hub *Hub, cache *redis.Client, id string) (*nats.Subscription, error) {
	return js.Subscribe("matchcore.events.>", func(m *nats.Msg) {
		var env store.Envelope
		if e := json.Unmarshal(m.Data, &env); e != nil || env.Version != 1 {
			slog.Error("invalid market event; consumer stopped", "subject", m.Subject)
			_ = m.Term()
			return
		}
		// Stable instrument sequence makes repeated external delivery harmless to clients.
		hub.Broadcast(env)
		if len(env.Plan.Trades) > 0 {
			last := env.Plan.Trades[len(env.Plan.Trades)-1]
			data, _ := json.Marshal(map[string]any{"instrument": env.Instrument, "sequence": env.Sequence, "price": last.Price, "timestamp": last.Timestamp})
			cacheCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := cache.Eval(cacheCtx, `local seq=redis.call('HGET',KEYS[1],'sequence'); if not seq or tonumber(ARGV[1])>tonumber(seq) then redis.call('HSET',KEYS[1],'sequence',ARGV[1],'data',ARGV[2]); redis.call('EXPIRE',KEYS[1],300); end; return 1`, []string{"market:" + env.Instrument}, env.Sequence, string(data)).Err()
			cancel()
			if err != nil {
				slog.Warn("market cache unavailable", "error", err)
			}
		}
		if e := m.Ack(); e != nil {
			slog.Warn("consumer ack failed", "error", e)
		}
	}, nats.Durable("gateway-"+id), nats.ManualAck(), nats.AckExplicit(), nats.MaxAckPending(1), nats.DeliverAll(), nats.BindStream("MATCHCORE"))
}
