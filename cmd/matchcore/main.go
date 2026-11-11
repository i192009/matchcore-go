package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	pb "github.com/matchcore/matchcore-go/api/gen/matchcore/v1"
	"github.com/matchcore/matchcore-go/internal/engine"
	"github.com/matchcore/matchcore-go/internal/observability"
	"github.com/matchcore/matchcore-go/internal/store"
	"github.com/matchcore/matchcore-go/internal/transport"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		slog.Error("matchcore failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 2 && os.Args[1] == "health" {
		client := http.Client{Timeout: 2 * time.Second}
		res, e := client.Get(os.Args[2])
		if e != nil {
			return e
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("not ready: %d", res.StatusCode)
		}
		return nil
	}
	ctx, cancel := transport.SignalContext()
	defer cancel()
	shutdown, e := observability.Setup(ctx, "matchcore")
	if e != nil {
		return e
	}
	defer func() { stop, c := store.Timeout(); defer c(); _ = shutdown(stop) }()
	db, e := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Pool.Close()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			return db.Migrate(ctx)
		case "seed":
			for _, id := range []string{"fees", "alice", "bob", "maker-buy", "maker-sell"} {
				if e = db.SeedAccount(ctx, id); e != nil {
					return e
				}
			}
			return nil
		default:
			return fmt.Errorf("unknown command %s", os.Args[1])
		}
	}
	auth, e := transport.LoadAuth()
	if e != nil {
		return e
	}
	eng, e := engine.New(ctx, db, 1024)
	if e != nil {
		return e
	}
	defer eng.Close()
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(auth.Interceptor), grpc.MaxRecvMsgSize(16384), grpc.MaxSendMsgSize(32<<20))
	pb.RegisterExchangeServer(grpcServer, &transport.Service{Engine: eng, Store: db, Started: time.Now()})
	listener, e := net.Listen("tcp", transport.Env("GRPC_ADDR", ":9090"))
	if e != nil {
		return e
	}
	defer listener.Close()
	errors := make(chan error, 2)
	go func() { errors <- grpcServer.Serve(listener) }()
	defer grpcServer.Stop()
	mux := transport.HealthMux(eng.Ready)
	mux.HandleFunc("POST /admin/restrictions", func(w http.ResponseWriter, r *http.Request) {
		p, err := auth.Check(r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if p.Role != "admin" {
			http.Error(w, "administrator required", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var body struct {
			Account    string `json:"account"`
			Restricted bool   `json:"restricted"`
		}
		if err = json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		if err = transport.ValidateAccount(body.Account); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		tag, err := db.Pool.Exec(r.Context(), "UPDATE accounts SET restricted=$2 WHERE id=$1", body.Account, body.Restricted)
		if err != nil {
			http.Error(w, "persistence failure", http.StatusServiceUnavailable)
			return
		}
		if tag.RowsAffected() != 1 {
			http.Error(w, "account not found", 404)
			return
		}
		w.WriteHeader(204)
	})
	go func() { errors <- transport.Serve(ctx, transport.Env("HTTP_ADDR", ":8081"), mux) }()
	select {
	case <-ctx.Done():
	case err := <-errors:
		if err != nil {
			return err
		}
	}
	cancel()
	done := make(chan struct{})
	go func() { grpcServer.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		grpcServer.Stop()
	}
	return nil
}
