package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/i192009/matchcore-go/internal/core"
	"github.com/i192009/matchcore-go/internal/engine"
	"github.com/i192009/matchcore-go/internal/store"
)

func TestBoundedQueueAndExpiredRequests(t *testing.T) {
	s := database(t)
	e, err := engine.New(context.Background(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.BeforeCommit = func() error { once.Do(func() { close(entered); <-release }); return nil }
	done := make(chan error, 2)
	go func() {
		_, err := e.Submit(context.Background(), command("alice", "one", "BTC-USD", core.Buy, 10, 1))
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, err := e.Submit(ctx, command("alice", "expired", "BTC-USD", core.Buy, 10, 1)); done <- err }()
	deadline := time.Now().Add(time.Second)
	for e.States()[0].Queue != 1 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("second request was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = e.Submit(context.Background(), command("alice", "overflow", "BTC-USD", core.Buy, 10, 1)); !errors.Is(err, engine.ErrQueueFull) {
		close(release)
		t.Fatal(err)
	}
	cancel()
	close(release)
	for n := 0; n < 2; n++ {
		err = <-done
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	for _, b := range mustBalances(t, s, "alice") {
		if b.Asset == "USD" && b.Reserved != 11 {
			t.Fatal("cancelled queued request reserved funds", b)
		}
	}
}
func mustBalances(t *testing.T, s *store.Store, account string) []store.Balance {
	t.Helper()
	b, e := s.Balances(context.Background(), account)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestCancellationCompetingWithExecution(t *testing.T) {
	s := database(t)
	e := start(t, s)
	defer e.Close()
	sell := submit(t, e, command("bob", "sell", "BTC-USD", core.Sell, 100, 1))
	var wg sync.WaitGroup
	for _, c := range []store.Command{command("alice", "buy", "BTC-USD", core.Buy, 100, 1), {Kind: "cancel", Key: "cancel", Account: "bob", Instrument: "BTC-USD", OrderID: sell.Order.ID}} {
		wg.Add(1)
		go func(c store.Command) {
			defer wg.Done()
			_, err := e.Submit(context.Background(), c)
			if err != nil {
				t.Error(err)
			}
		}(c)
	}
	wg.Wait()
	if count(t, s, "trades") > 1 {
		t.Fatal("duplicate execution")
	}
	for _, b := range mustBalances(t, s, "bob") {
		if b.Asset == "BTC" && b.Reserved != 0 {
			t.Fatal(b)
		}
	}
	conserved(t, s)
}
func TestRealSQLFailureRollsBackBook(t *testing.T) {
	s := database(t)
	e := start(t, s)
	defer e.Close()
	ctx := context.Background()
	_, err := s.Pool.Exec(ctx, `CREATE FUNCTION fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'database unavailable injection'; END $$; CREATE TRIGGER fail_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION fail_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	c := command("alice", "rollback", "BTC-USD", core.Buy, 10, 1)
	if _, err = e.Submit(ctx, c); err == nil {
		t.Fatal("SQL failure ignored")
	}
	d, err := e.Depth("BTC-USD")
	if err != nil || len(d.Bids) != 0 || count(t, s, "orders") != 0 || count(t, s, "ledger") != 0 {
		t.Fatal("failed transaction advanced state", d, err)
	}
	if _, err = s.Pool.Exec(ctx, "DROP TRIGGER fail_event ON events"); err != nil {
		t.Fatal(err)
	}
	submit(t, e, c)
	conserved(t, s)
}
