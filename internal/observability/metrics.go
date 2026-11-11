package observability

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var (
	Commands        = prometheus.NewCounter(prometheus.CounterOpts{Name: "matchcore_commands_total", Help: "Received engine commands."})
	Orders          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "matchcore_orders_total", Help: "Order outcomes."}, []string{"outcome"})
	Trades          = prometheus.NewCounter(prometheus.CounterOpts{Name: "matchcore_trades_total", Help: "Committed executions."})
	Attempts        = prometheus.NewCounter(prometheus.CounterOpts{Name: "matchcore_match_attempts_total", Help: "Maker matching attempts."})
	CommandLatency  = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "matchcore_command_seconds", Help: "Command duration including queue.", Buckets: prometheus.ExponentialBuckets(0.0001, 2, 18)})
	MatchingLatency = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "matchcore_matching_seconds", Help: "Pure matching plan duration.", Buckets: prometheus.ExponentialBuckets(0.000001, 2, 18)})
	DBLatency       = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "matchcore_commit_seconds", Help: "Financial transaction latency.", Buckets: prometheus.ExponentialBuckets(0.0001, 2, 18)})
	Recovery        = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "matchcore_recovery_seconds", Help: "Instrument recovery duration."})
	Failures        = prometheus.NewCounter(prometheus.CounterOpts{Name: "matchcore_transactions_failed_total", Help: "Failed database transactions."})
	Queue           = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "matchcore_queue_depth", Help: "Pending commands."}, []string{"instrument"})
	Depth           = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "matchcore_book_orders", Help: "Open orders."}, []string{"instrument"})
	Safe            = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "matchcore_engine_safe", Help: "Recovered and safe writer."}, []string{"instrument"})
	Clients         = prometheus.NewGauge(prometheus.GaugeOpts{Name: "matchcore_websocket_clients", Help: "Active WebSocket clients."})
	Backlog         = prometheus.NewGauge(prometheus.GaugeOpts{Name: "matchcore_outbox_backlog", Help: "Unpublished committed events."})
	PublishErrors   = prometheus.NewCounter(prometheus.CounterOpts{Name: "matchcore_publish_failures_total", Help: "Failed event publication attempts."})
	EventDelay      = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "matchcore_event_delay_seconds", Help: "Commit to gateway event delivery delay.", Buckets: prometheus.ExponentialBuckets(.001, 2, 18)})
)

func init() {
	prometheus.MustRegister(Commands, Orders, Trades, Attempts, CommandLatency, MatchingLatency, DBLatency, Recovery, Failures, Queue, Depth, Safe, Clients, Backlog, PublishErrors, EventDelay)
}
func Setup(ctx context.Context, service string) (func(context.Context) error, error) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", service))), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(.1)))}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, e := otlptracehttp.New(ctx)
		if e != nil {
			return nil, e
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	provider := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}
