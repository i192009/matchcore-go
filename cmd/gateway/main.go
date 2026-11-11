package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	pb "github.com/matchcore/matchcore-go/api/gen/matchcore/v1"
	"github.com/matchcore/matchcore-go/internal/observability"
	"github.com/matchcore/matchcore-go/internal/store"
	"github.com/matchcore/matchcore-go/internal/transport"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := transport.SignalContext()
	defer cancel()
	shutdown, e := observability.Setup(ctx, "gateway")
	if e != nil {
		return e
	}
	defer func() { stop, c := store.Timeout(); defer c(); _ = shutdown(stop) }()
	auth, e := transport.LoadAuth()
	if e != nil {
		return e
	}
	conn, e := grpc.NewClient(transport.Env("MATCHER_ADDR", "localhost:9090"), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32<<20)))
	if e != nil {
		return e
	}
	defer conn.Close()
	api := pb.NewExchangeClient(conn)
	origins := transport.Origins(os.Getenv("ALLOWED_ORIGINS"))
	hub := transport.NewHub(api, auth, origins)
	cache := redis.NewClient(&redis.Options{Addr: transport.Env("REDIS_URL", "localhost:6379"), DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	defer cache.Close()
	nc, js, e := transport.ConnectNATS(transport.Env("NATS_URL", "nats://localhost:4222"))
	if e != nil {
		return e
	}
	defer nc.Close()
	sub, e := transport.Consume(ctx, js, hub, cache, transport.Env("GATEWAY_ID", "local-1"))
	if e != nil {
		return e
	}
	defer func() { _ = sub.Unsubscribe() }()
	var keys []transport.Credential
	if e = json.Unmarshal([]byte(os.Getenv("API_KEYS")), &keys); e != nil {
		return e
	}
	if len(keys) == 0 {
		return errors.New("no readiness credential")
	}
	probeToken := "Bearer " + keys[0].Key
	g := &transport.Gateway{API: api, Auth: auth, Hub: hub, Cache: cache, Origins: origins, Ready: func(ctx context.Context) bool {
		if !nc.IsConnected() {
			return false
		}
		res, err := api.GetOperations(metadata.AppendToOutgoingContext(ctx, "authorization", probeToken), &pb.Query{})
		if err != nil {
			return false
		}
		var state struct {
			Ready bool `json:"ready"`
		}
		if json.Unmarshal(res.Json, &state) != nil || !state.Ready {
			return false
		}
		return cache.Ping(ctx).Err() == nil
	}}
	return transport.Serve(ctx, transport.Env("HTTP_ADDR", ":8080"), g.Handler())
}
