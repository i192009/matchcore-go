package integration

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matchcore/matchcore-go/internal/core"
	"github.com/matchcore/matchcore-go/internal/store"
)

func TestOrderIDCollisionCannotOverwriteAnotherInstrument(t *testing.T) {
	s := database(t)
	eng := start(t, s)
	defer eng.Close()
	original := command("alice", "original", "BTC-USD", core.Buy, 10, 1)
	submit(t, eng, original)
	before := mustBalances(t, s, "bob")
	collision := command("bob", "collision", "ETH-USD", core.Sell, 100, 1)
	collision.Order.ID = original.Order.ID
	if _, err := eng.Submit(context.Background(), collision); !errors.Is(err, store.ErrConflict) {
		t.Fatal("order identity collision not rejected", err)
	}
	if !reflect.DeepEqual(before, mustBalances(t, s, "bob")) {
		t.Fatal("collision changed balances")
	}
	orders, err := s.Orders(context.Background(), "alice", "BTC-USD")
	if err != nil || len(orders) != 1 || orders[0].ClientID != "original" {
		t.Fatal("original order overwritten", orders, err)
	}
	d, err := eng.Depth("ETH-USD")
	if err != nil || d.Sequence != 0 || len(d.Asks) != 0 {
		t.Fatal("collision advanced book", d, err)
	}
	conserved(t, s)
}

func TestPartialFeeRoundingAndPriceImprovement(t *testing.T) {
	s := database(t)
	e := start(t, s)
	defer e.Close()
	submit(t, e, command("bob", "ask", "BTC-USD", core.Sell, 90, 1))
	buy := submit(t, e, command("alice", "bid", "BTC-USD", core.Buy, 100, 3))
	if buy.Order.Remaining != 2 || buy.Order.Reserved != 202 || buy.Order.Fees != 1 || buy.Order.Notional != 90 {
		t.Fatal(buy.Order)
	}
	for _, b := range mustBalances(t, s, "alice") {
		if b.Asset == "USD" && (b.Available != 99_999_707 || b.Reserved != 202) {
			t.Fatal("price improvement not released", b)
		}
	}
	submit(t, e, store.Command{Kind: "cancel", Key: "cancel-bid", Account: "alice", Instrument: "BTC-USD", OrderID: buy.Order.ID})
	for _, b := range mustBalances(t, s, "alice") {
		if b.Asset == "USD" && (b.Available != 99_999_909 || b.Reserved != 0) {
			t.Fatal(b)
		}
	}
	conserved(t, s)
}
func TestMarketBudgetSettlementAndRestrictedCancellation(t *testing.T) {
	s := database(t)
	e := start(t, s)
	defer e.Close()
	ask := submit(t, e, command("bob", "ask", "BTC-USD", core.Sell, 100, 5))
	c := command("alice", "market", "BTC-USD", core.Buy, 0, 3)
	c.Order.Type = "MARKET"
	c.Order.TIF = "IOC"
	c.Order.MaxSpend = 101
	env := submit(t, e, c)
	if env.Order.Filled != 1 || env.Order.Remaining != 2 || env.Order.Status != core.Cancelled || env.Order.Reserved != 0 {
		t.Fatal(env.Order)
	}
	for _, b := range mustBalances(t, s, "alice") {
		if b.Asset == "USD" && b.Available != 99_999_899 {
			t.Fatal(b)
		}
	}
	if _, err := s.Pool.Exec(context.Background(), "UPDATE accounts SET restricted=true WHERE id='bob'"); err != nil {
		t.Fatal(err)
	}
	rejected, err := e.Submit(context.Background(), command("bob", "restricted", "ETH-USD", core.Sell, 100, 1))
	if err != nil || rejected.Error != "account trading restricted" || rejected.Order.Status != core.Rejected {
		t.Fatal(rejected, err)
	}
	submit(t, e, store.Command{Kind: "cancel", Key: "risk-reducing-cancel", Account: "bob", Instrument: "BTC-USD", OrderID: ask.Order.ID})
	for _, b := range mustBalances(t, s, "bob") {
		if b.Asset == "BTC" && b.Reserved != 0 {
			t.Fatal(b)
		}
	}
	conserved(t, s)
}
