package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	pb "github.com/matchcore/matchcore-go/api/gen/matchcore/v1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Gateway struct {
	API     pb.ExchangeClient
	Auth    *Auth
	Hub     *Hub
	Cache   *redis.Client
	Origins []string
	Ready   func(context.Context) bool
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !g.Ready(ctx) {
			http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("GET /ws", g.Hub)
	mux.HandleFunc("/api/", g.api)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, o := range g.Origins {
				if origin == o {
					allowed = true
				}
			}
			if !allowed {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(new(any)); !errors.Is(e, io.EOF) {
		return errors.New("one JSON object required")
	}
	return nil
}
func (g *Gateway) api(w http.ResponseWriter, r *http.Request) {
	parent := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	traceCtx, span := otel.Tracer("matchcore/http").Start(parent, r.Method+" "+r.URL.Path)
	defer span.End()
	requestID := make([]byte, 16)
	if _, e := rand.Read(requestID); e != nil {
		http.Error(w, "request id unavailable", 500)
		return
	}
	id := hex.EncodeToString(requestID)
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("Content-Type", "application/json")
	token := r.Header.Get("Authorization")
	p, e := g.Auth.Check(token)
	if e != nil {
		writeError(w, e, id)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	traceCarrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(traceCtx, traceCarrier)
	traceCtx = metadata.AppendToOutgoingContext(traceCtx, "authorization", token, "x-request-id", id)
	for k, v := range traceCarrier {
		traceCtx = metadata.AppendToOutgoingContext(traceCtx, k, v)
	}
	ctx, cancel := context.WithTimeout(traceCtx, 10*time.Second)
	defer cancel()
	q := &pb.Query{Instrument: r.URL.Query().Get("instrument"), OrderId: r.URL.Query().Get("order_id")}
	var res *pb.Result
	switch r.Method + " " + r.URL.Path {
	case "POST /api/orders":
		var body struct {
			ClientID   string `json:"client_id"`
			Instrument string `json:"instrument"`
			Side       string `json:"side"`
			Type       string `json:"type"`
			TIF        string `json:"tif"`
			Price      int64  `json:"price"`
			Quantity   int64  `json:"quantity"`
			MaxSpend   int64  `json:"max_spend"`
		}
		if e = decode(r, &body); e != nil {
			writeError(w, status.Error(codes.InvalidArgument, "invalid order JSON"), id)
			return
		}
		res, e = g.API.PlaceOrder(ctx, &pb.PlaceOrderRequest{ClientId: body.ClientID, Instrument: body.Instrument, Side: pb.Side(pb.Side_value[body.Side]), Type: pb.OrderType(pb.OrderType_value[body.Type]), Tif: pb.TimeInForce(pb.TimeInForce_value[body.TIF]), Price: body.Price, Quantity: body.Quantity, MaxSpend: body.MaxSpend})
	case "POST /api/cancel":
		var body struct {
			Key        string `json:"key"`
			Instrument string `json:"instrument"`
			OrderID    string `json:"order_id"`
		}
		if e = decode(r, &body); e != nil {
			writeError(w, status.Error(codes.InvalidArgument, "invalid cancellation JSON"), id)
			return
		}
		res, e = g.API.CancelOrder(ctx, &pb.CancelOrderRequest{Key: body.Key, Instrument: body.Instrument, OrderId: body.OrderID})
	case "GET /api/instruments":
		res, e = g.API.ListInstruments(ctx, q)
	case "GET /api/book":
		res, e = g.API.GetOrderBook(ctx, q)
	case "GET /api/trades":
		res, e = g.API.GetRecentTrades(ctx, q)
	case "GET /api/balances":
		res, e = g.API.GetAccountBalances(ctx, q)
	case "GET /api/orders":
		res, e = g.API.GetOrderHistory(ctx, q)
	case "GET /api/order":
		res, e = g.API.GetOrder(ctx, q)
	case "GET /api/operations":
		res, e = g.API.GetOperations(ctx, q)
	case "GET /api/audit":
		res, e = g.API.GetAudit(ctx, q)
	case "GET /api/account":
		res, e = g.API.GetAccountSummary(ctx, q)
	case "GET /api/my-trades":
		res, e = g.API.GetMyTrades(ctx, q)
	case "GET /api/identity":
		_ = json.NewEncoder(w).Encode(p)
		return
	case "GET /api/market":
		data, err := g.Cache.HGet(ctx, "market:"+q.Instrument, "data").Result()
		if errors.Is(err, redis.Nil) {
			_, _ = w.Write([]byte("null"))
			return
		}
		if err != nil {
			writeError(w, status.Error(codes.Unavailable, "cache unavailable"), id)
			return
		}
		_, _ = w.Write([]byte(data))
		return
	default:
		writeError(w, status.Error(codes.NotFound, "route not found"), id)
		return
	}
	if e != nil {
		slog.Warn("API request failed", "request_id", id, "code", status.Code(e).String())
		writeError(w, e, id)
		return
	}
	var outcome struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(res.Json, &outcome)
	if outcome.Error != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	slog.Info("API request completed", "request_id", id, "trace_id", span.SpanContext().TraceID().String(), "route", r.URL.Path)
	_, _ = w.Write(res.Json)
}
func writeError(w http.ResponseWriter, e error, id string) {
	code := status.Code(e)
	httpCode := 500
	switch code {
	case codes.InvalidArgument:
		httpCode = 400
	case codes.Unauthenticated:
		httpCode = 401
	case codes.PermissionDenied:
		httpCode = 403
	case codes.NotFound:
		httpCode = 404
	case codes.AlreadyExists:
		httpCode = 409
	case codes.ResourceExhausted:
		httpCode = 429
	case codes.DeadlineExceeded:
		httpCode = 504
	case codes.Unavailable:
		httpCode = 503
	case codes.Canceled:
		httpCode = 408
	}
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code.String(), "error": status.Convert(e).Message(), "request_id": id})
}
func Origins(value string) []string {
	out := []string{}
	for _, o := range strings.Split(value, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}
