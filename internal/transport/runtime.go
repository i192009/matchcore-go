package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func Serve(ctx context.Context, address string, handler http.Handler) error {
	s := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if e := s.Shutdown(stop); e != nil {
				slog.Warn("HTTP shutdown", "error", e)
			}
		case <-done:
		}
	}()
	defer close(done)
	e := s.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func HealthMux(ready func() bool) *http.ServeMux {
	m := http.NewServeMux()
	m.Handle("/metrics", promhttp.Handler())
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	return m
}
