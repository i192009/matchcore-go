package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/i192009/matchcore-go/internal/core"
	"github.com/i192009/matchcore-go/internal/engine"
	"github.com/i192009/matchcore-go/internal/store"
	"github.com/i192009/matchcore-go/internal/transport"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

var serial atomic.Int64

func database(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("MATCHCORE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MATCHCORE_TEST_DATABASE_URL to run real-service tests")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), serial.Add(1))
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	s := &store.Store{Pool: pool}
	t.Cleanup(func() { pool.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	for _, a := range []string{"fees", "alice", "bob"} {
		if e = s.SeedAccount(ctx, a); e != nil {
			t.Fatal(e)
		}
	}
	return s
}
func command(account, key, instrument string, side core.Side, p, q int64) store.Command {
	o := core.Order{ID: account + "-" + key, ClientID: key, Account: account, Instrument: instrument, Side: side, Type: "LIMIT", TIF: "GTC", Price: p, Quantity: q}
	return store.Command{Kind: "place", Key: key, Account: account, Instrument: instrument, Order: o}
}
func start(t *testing.T, s *store.Store) *engine.Engine {
	t.Helper()
	e, err := engine.New(context.Background(), s, 128)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func submit(t *testing.T, e *engine.Engine, c store.Command) store.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env, err := e.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if env.Error != "" {
		t.Fatal(env.Error)
	}
	return env
}
func count(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	var n int
	if e := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func conserved(t *testing.T, s *store.Store) {
	t.Helper()
	var ok bool
	e := s.Pool.QueryRow(context.Background(), `SELECT bool_and(total=issued) FROM (SELECT asset,sum(available::numeric+reserved::numeric) total FROM balances GROUP BY asset) b JOIN supply USING(asset)`).Scan(&ok)
	if e != nil || !ok {
		t.Fatal("assets not conserved", e)
	}
}
func TestAcceptanceSettlementRecoveryAndIdempotency(t *testing.T) {
	s := database(t)
	e := start(t, s)
	sell := command("bob", "sell", "BTC-USD", core.Sell, 5000, 2000)
	se := submit(t, e, sell)
	buy := command("alice", "buy", "BTC-USD", core.Buy, 5000, 1000)
	be := submit(t, e, buy)
	if len(be.Plan.Trades) != 1 || be.Plan.Trades[0].Quantity != 1000 || be.Plan.Trades[0].Price != 5000 {
		t.Fatal(be)
	}
	if be.Order.Status != core.Filled {
		t.Fatal(be.Order)
	}
	orders, err := s.Orders(context.Background(), "bob", "BTC-USD")
	if err != nil || orders[0].Remaining != 1000 || orders[0].Reserved != 1000 {
		t.Fatal(orders, err)
	}
	balances, err := s.Balances(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range balances {
		if b.Asset == "USD" && (b.Available != 94_995_000 || b.Reserved != 0) {
			t.Fatal(b)
		}
		if b.Asset == "BTC" && b.Total != 101000 {
			t.Fatal(b)
		}
	}
	submit(t, e, store.Command{Kind: "cancel", Key: "cancel", Account: "bob", Instrument: "BTC-USD", OrderID: se.Order.ID})
	conserved(t, s)
	before, err := s.Balances(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range before {
		if b.Asset == "BTC" && (b.Reserved != 0 || b.Available != 99000) {
			t.Fatal(b)
		}
	}
	e.Close()
	e = start(t, s)
	defer e.Close()
	repeated := submit(t, e, buy)
	if repeated.ID != be.ID || count(t, s, "trades") != 1 || count(t, s, "settlements") != 1 {
		t.Fatal("duplicate settlement")
	}
	after, _ := s.Balances(context.Background(), "bob")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("balances changed on replay")
	}
	d, err := e.Depth("BTC-USD")
	if err != nil || len(d.Asks) != 0 {
		t.Fatal(d, err)
	}
	if count(t, s, "events") != 3 {
		t.Fatal("events missing")
	}
}
func TestCrashBeforeCommitAndAfterCommit(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			s := database(t)
			e := start(t, s)
			hook := func() error { return errors.New("injected crash") }
			if after {
				s.AfterCommit = hook
			} else {
				s.BeforeCommit = hook
			}
			c := command("alice", "crash", "BTC-USD", core.Buy, 50, 10)
			if _, err := e.Submit(context.Background(), c); err == nil {
				t.Fatal("failure injection ignored")
			}
			if after && e.Ready() {
				t.Fatal("unsafe matcher reports ready")
			}
			d, err := e.Depth("BTC-USD")
			if !after && (err != nil || len(d.Bids) != 0) {
				t.Fatal("uncommitted state visible")
			}
			e.Close()
			s.BeforeCommit = nil
			s.AfterCommit = nil
			e = start(t, s)
			defer e.Close()
			submit(t, e, c)
			if count(t, s, "orders") != 1 || count(t, s, "events") != 1 {
				t.Fatal("recovery duplicated or lost command")
			}
			conserved(t, s)
		})
	}
}
func TestSnapshotAndReplayValidation(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(snapshot), func(t *testing.T) {
			s := database(t)
			e := start(t, s)
			submit(t, e, command("alice", "one", "BTC-USD", core.Buy, 50, 10))
			e.Close()
			if !snapshot {
				if _, err := s.Pool.Exec(context.Background(), "DELETE FROM snapshots"); err != nil {
					t.Fatal(err)
				}
			}
			e = start(t, s)
			submit(t, e, command("bob", "two", "BTC-USD", core.Sell, 100, 10))
			e.Close()
			if _, err := s.Recover(context.Background(), "BTC-USD"); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("corrupt snapshot fails closed", func(t *testing.T) {
		s := database(t)
		e := start(t, s)
		e.Close()
		if _, err := s.Pool.Exec(context.Background(), "UPDATE snapshots SET checksum='corrupt' WHERE instrument='BTC-USD'"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Recover(context.Background(), "BTC-USD"); err == nil {
			t.Fatal("corruption silently ignored")
		}
	})
	t.Run("corrupt event fails closed", func(t *testing.T) {
		s := database(t)
		e := start(t, s)
		submit(t, e, command("alice", "a", "BTC-USD", core.Buy, 50, 1))
		e.Close()
		_, err := s.Pool.Exec(context.Background(), "DELETE FROM snapshots; ALTER TABLE events DISABLE TRIGGER events_immutable; UPDATE events SET checksum='bad'; ALTER TABLE events ENABLE TRIGGER events_immutable")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Recover(context.Background(), "BTC-USD"); err == nil {
			t.Fatal("invalid event ignored")
		}
	})
}
func TestCrossInstrumentReservationsAndDuplicates(t *testing.T) {
	s := database(t)
	e := start(t, s)
	defer e.Close()
	var wg sync.WaitGroup
	var accepted atomic.Int64
	errs := make(chan error, 100)
	for n := 0; n < 60; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := core.Instruments[n%3].ID
			c := command("alice", fmt.Sprintf("parallel-%d", n), id, core.Buy, 10000, 2000)
			env, err := e.Submit(context.Background(), c)
			if err != nil {
				errs <- err
				return
			}
			if env.Error == "" {
				accepted.Add(1)
			} else if env.Error != "insufficient funds" {
				errs <- errors.New(env.Error)
			}
		}(n)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if accepted.Load() != 4 {
		t.Fatalf("accepted %d; reservation should allow four", accepted.Load())
	}
	conserved(t, s)
	c := command("bob", "duplicate", "SOL-USD", core.Sell, 10, 1)
	wg = sync.WaitGroup{}
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Submit(context.Background(), c)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM orders WHERE account='bob'").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	c.Order.Quantity = 2
	if _, err := e.Submit(context.Background(), c); !errors.Is(err, store.ErrConflict) {
		t.Fatal("key conflict not detected", err)
	}
}
func TestFencingAndDatabaseUnavailability(t *testing.T) {
	s := database(t)
	e := start(t, s)
	defer e.Close()
	if second, err := engine.New(context.Background(), s, 1); err == nil {
		second.Close()
		t.Fatal("second owner accepted")
	}
	if _, err := s.Pool.Exec(context.Background(), "UPDATE instruments SET epoch=epoch+1 WHERE id='BTC-USD'"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Submit(context.Background(), command("alice", "stale", "BTC-USD", core.Buy, 10, 1)); !errors.Is(err, store.ErrStale) {
		t.Fatal(err)
	}
	if e.Ready() {
		t.Fatal("stale service ready")
	}
}
func TestOutboxRetryAndRealDependencies(t *testing.T) {
	s := database(t)
	e := start(t, s)
	submit(t, e, command("alice", "outbox", "BTC-USD", core.Buy, 50, 1))
	e.Close()
	attempts := 0
	_, err := s.PublishBatch(context.Background(), func(id, instrument string, data []byte) error {
		attempts++
		return errors.New("publisher crashed after broker ack")
	})
	if err == nil {
		t.Fatal("failure ignored")
	}
	var pending int
	if err = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatal(pending, err)
	}
	nc, js, err := transport.ConnectNATS(os.Getenv("MATCHCORE_TEST_NATS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_, err = s.PublishBatch(context.Background(), func(id, instrument string, data []byte) error {
		attempts++
		m := nats.NewMsg("matchcore.events." + instrument)
		m.Header.Set(nats.MsgIdHdr, id+"-"+fmt.Sprint(time.Now().UnixNano()))
		m.Data = data
		_, err := js.PublishMsg(m)
		return err
	})
	if err != nil || attempts != 2 {
		t.Fatal(attempts, err)
	}
	cache := redis.NewClient(&redis.Options{Addr: os.Getenv("MATCHCORE_TEST_REDIS_ADDR")})
	defer cache.Close()
	ctx := context.Background()
	if err = cache.Set(ctx, "matchcore-test", "committed", time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	v, err := cache.Get(ctx, "matchcore-test").Result()
	if err != nil || !strings.Contains(v, "committed") {
		t.Fatal(v, err)
	}
}
