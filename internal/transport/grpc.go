package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"

	pb "github.com/i192009/matchcore-go/api/gen/matchcore/v1"
	"github.com/i192009/matchcore-go/internal/core"
	"github.com/i192009/matchcore-go/internal/engine"
	"github.com/i192009/matchcore-go/internal/observability"
	"github.com/i192009/matchcore-go/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	pb.UnimplementedExchangeServer
	Engine  *engine.Engine
	Store   *store.Store
	Started time.Time
}

func result(v any, e error) (*pb.Result, error) {
	if e != nil {
		return nil, apiError(e)
	}
	data, e := json.Marshal(v)
	if e != nil {
		return nil, status.Error(codes.Internal, "serialization failed")
	}
	return &pb.Result{Json: data}, nil
}
func apiError(e error) error {
	switch {
	case errors.Is(e, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded; retry the same key")
	case errors.Is(e, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled; retry the same key")
	case errors.Is(e, store.ErrConflict):
		return status.Error(codes.AlreadyExists, e.Error())
	case errors.Is(e, engine.ErrQueueFull):
		return status.Error(codes.ResourceExhausted, e.Error())
	case errors.Is(e, engine.ErrUnavailable), errors.Is(e, store.ErrStale), errors.Is(e, store.ErrAmbiguous):
		return status.Error(codes.Unavailable, e.Error())
	default:
		return status.Error(codes.Internal, "persistence or engine failure")
	}
}
func (s *Service) PlaceOrder(ctx context.Context, r *pb.PlaceOrderRequest) (*pb.Result, error) {
	if r.ClientId == "" || len(r.ClientId) > 128 {
		return nil, status.Error(codes.InvalidArgument, "client_id required (max 128 characters)")
	}
	if _, e := core.FindInstrument(r.Instrument); e != nil {
		return nil, status.Error(codes.NotFound, e.Error())
	}
	p := identity(ctx)
	id := store.Hash([]byte(p.Account + "/" + r.ClientId))[:32]
	o := core.Order{ID: id, ClientID: r.ClientId, Account: p.Account, Instrument: r.Instrument, Side: core.Side(r.Side.String()), Type: r.Type.String(), TIF: r.Tif.String(), Price: r.Price, Quantity: r.Quantity, MaxSpend: r.MaxSpend}
	env, e := s.Engine.Submit(ctx, store.Command{Kind: "place", Key: r.ClientId, Account: p.Account, Instrument: r.Instrument, Order: o})
	return result(env, e)
}
func (s *Service) CancelOrder(ctx context.Context, r *pb.CancelOrderRequest) (*pb.Result, error) {
	if r.Key == "" || len(r.Key) > 128 || r.OrderId == "" {
		return nil, status.Error(codes.InvalidArgument, "key and order_id required")
	}
	if _, e := core.FindInstrument(r.Instrument); e != nil {
		return nil, status.Error(codes.NotFound, e.Error())
	}
	env, e := s.Engine.Submit(ctx, store.Command{Kind: "cancel", Key: r.Key, Account: identity(ctx).Account, Instrument: r.Instrument, OrderID: r.OrderId})
	return result(env, e)
}
func (s *Service) GetOrder(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	var data []byte
	e := s.Store.Pool.QueryRow(ctx, "SELECT data FROM orders WHERE id=$1 AND account=$2", q.OrderId, identity(ctx).Account).Scan(&data)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "order not found")
	}
	if e != nil {
		return result(nil, e)
	}
	return &pb.Result{Json: data}, nil
}
func (s *Service) GetOrderBook(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	d, e := s.Engine.Depth(q.Instrument)
	if e != nil && e.Error() == "unknown instrument" {
		return nil, status.Error(codes.NotFound, e.Error())
	}
	return result(d, e)
}
func (s *Service) GetRecentTrades(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	v, e := s.Store.Trades(ctx, q.Instrument, "")
	public := make([]publicTrade, 0, len(v))
	for _, t := range v {
		public = append(public, publicTrade{t.ID, t.Price, t.Quantity, t.Timestamp})
	}
	return result(public, e)
}
func (s *Service) GetAccountBalances(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	v, e := s.Store.Balances(ctx, identity(ctx).Account)
	return result(v, e)
}
func (s *Service) GetOrderHistory(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	v, e := s.Store.Orders(ctx, identity(ctx).Account, q.Instrument)
	return result(v, e)
}
func (s *Service) ListInstruments(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	return result(core.Instruments, nil)
}
func (s *Service) GetAudit(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	v, e := s.Store.Audit(ctx, identity(ctx).Account)
	return result(v, e)
}

func (s *Service) GetAccountSummary(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	var fees, trades, orders int64
	account := identity(ctx).Account
	e := s.Store.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT sum((data->>'fees')::bigint) FROM orders WHERE account=$1),0),(SELECT count(*) FROM trades WHERE buyer=$1 OR seller=$1),(SELECT count(*) FROM orders WHERE account=$1)`, account).Scan(&fees, &trades, &orders)
	return result(map[string]int64{"fees_cents": fees, "executions": trades, "orders": orders}, e)
}
func (s *Service) GetMyTrades(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	account := identity(ctx).Account
	trades, e := s.Store.Trades(ctx, q.Instrument, account)
	out := []map[string]any{}
	for _, t := range trades {
		side := "BUY"
		if t.Seller == account {
			side = "SELL"
		}
		out = append(out, map[string]any{"id": t.ID, "instrument": t.Instrument, "side": side, "price": t.Price, "quantity": t.Quantity, "fee": t.Fee, "timestamp": t.Timestamp})
	}
	return result(out, e)
}
func (s *Service) GetOperations(ctx context.Context, q *pb.Query) (*pb.Result, error) {
	var orders, trades, backlog int64
	var delay float64
	e := s.Store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders),(SELECT count(*) FROM trades),(SELECT count(*) FROM outbox WHERE published_at IS NULL),COALESCE((SELECT EXTRACT(EPOCH FROM now())-min(timestamp)/1000.0 FROM events JOIN outbox ON events.id=outbox.event_id WHERE published_at IS NULL),0)`).Scan(&orders, &trades, &backlog, &delay)
	return result(map[string]any{"ready": s.Engine.Ready(), "engines": s.Engine.States(), "orders": orders, "trades": trades, "outbox_backlog": backlog, "event_delay_seconds": max(delay, 0), "uptime_seconds": time.Since(s.Started).Seconds(), "latency": observability.LatencySummary(), "metrics_url": "/metrics", "latency_note": "Histogram bucket upper-bound estimates since process start; use Grafana for rate windows"}, e)
}
func ValidateAccount(id string) error {
	if len(id) < 1 || len(id) > 64 {
		return errors.New("invalid account id")
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return fmt.Errorf("account id must use lowercase letters, digits or hyphens")
		}
	}
	return nil
}
