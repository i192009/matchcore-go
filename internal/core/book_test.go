package core

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func order(id string, side Side, p, q int64) Order {
	return Order{ID: id, ClientID: id, Account: id, Instrument: "BTC-USD", Side: side, Price: p, Quantity: q, Type: "LIMIT", TIF: "GTC"}
}
func apply(t testing.TB, b *Book, o Order) Plan {
	t.Helper()
	p, e := b.Plan(o, b.Sequence+1, 1000+b.Sequence)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Apply(p, b.Sequence+1); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPriceTimeAndRestingPrice(t *testing.T) {
	b := NewBook("BTC-USD")
	apply(t, b, order("s1", Sell, 101, 2))
	apply(t, b, order("s2", Sell, 100, 1))
	apply(t, b, order("s3", Sell, 100, 3))
	p := apply(t, b, order("b", Buy, 105, 5))
	want := []string{"s2", "s3", "s1"}
	for i, tr := range p.Trades {
		if tr.Maker != want[i] {
			t.Fatalf("maker %d: %s", i, tr.Maker)
		}
	}
	if p.Trades[0].Price != 100 || p.Trades[2].Price != 101 {
		t.Fatal("must execute resting prices")
	}
	o, _ := b.Get("s1")
	if o.Status != Partial || o.Remaining != 1 || o.Filled != 1 {
		t.Fatal(o)
	}
	d := b.Depth(10)
	if len(d.Asks) != 1 || d.Asks[0].Quantity != 1 {
		t.Fatal(d)
	}
}
func TestBidPriorityAndCancel(t *testing.T) {
	b := NewBook("BTC-USD")
	apply(t, b, order("b1", Buy, 100, 3))
	apply(t, b, order("b2", Buy, 101, 2))
	p := apply(t, b, order("s", Sell, 99, 1))
	if p.Trades[0].Maker != "b2" {
		t.Fatal(p)
	}
	if _, e := b.Cancel("b1", "intruder", 1); e == nil {
		t.Fatal("ownership bypass")
	}
	p, e := b.Cancel("b1", "b1", 2)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Apply(p, b.Sequence+1); e != nil {
		t.Fatal(e)
	}
	if _, ok := b.Get("b1"); ok {
		t.Fatal("cancelled order remains")
	}
	if _, e = b.Cancel("b1", "b1", 2); e == nil {
		t.Fatal("repeat cancellation accepted")
	}
}
func TestMarketIOCAndBudget(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		b := NewBook("BTC-USD")
		o := order("b", Buy, 0, 5)
		o.Type = "MARKET"
		o.TIF = "IOC"
		o.MaxSpend = 500
		p := apply(t, b, o)
		if p.Changes[0].After.Status != Cancelled || len(p.Trades) != 0 {
			t.Fatal(p)
		}
	})
	t.Run("budget includes fees", func(t *testing.T) {
		b := NewBook("BTC-USD")
		apply(t, b, order("s", Sell, 100, 20))
		o := order("b", Buy, 0, 20)
		o.Type = "MARKET"
		o.TIF = "IOC"
		o.MaxSpend = 1001
		p := apply(t, b, o)
		if p.Trades[0].Quantity != 10 || p.Changes[len(p.Changes)-1].After.Status != Cancelled {
			t.Fatal(p)
		}
		if p.Trades[0].Notional+p.Trades[0].Fee > o.MaxSpend {
			t.Fatal("overspend")
		}
	})
	t.Run("IOC limit", func(t *testing.T) {
		b := NewBook("BTC-USD")
		apply(t, b, order("s", Sell, 100, 2))
		o := order("b", Buy, 100, 3)
		o.TIF = "IOC"
		p := apply(t, b, o)
		last := p.Changes[len(p.Changes)-1].After
		if last.Status != Cancelled || last.Remaining != 1 || last.Filled != 2 {
			t.Fatal(last)
		}
	})
}
func TestSelfTradePrevention(t *testing.T) {
	b := NewBook("BTC-USD")
	o := order("s", Sell, 100, 2)
	o.Account = "same"
	apply(t, b, o)
	o = order("b", Buy, 100, 2)
	o.Account = "same"
	p := apply(t, b, o)
	if len(p.Trades) != 0 || p.Changes[0].After.Status != Cancelled {
		t.Fatal(p)
	}
}
func TestPlanIsReadOnlyAndFIFOAfterPartial(t *testing.T) {
	b := NewBook("BTC-USD")
	apply(t, b, order("a", Sell, 100, 4))
	apply(t, b, order("b", Sell, 100, 4))
	before, _ := json.Marshal(b.Orders())
	p, e := b.Plan(order("c", Buy, 100, 1), b.Sequence+1, 5)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := json.Marshal(b.Orders())
	if string(before) != string(after) {
		t.Fatal("planning mutated state")
	}
	if e = b.Apply(p, b.Sequence+1); e != nil {
		t.Fatal(e)
	}
	p = apply(t, b, order("d", Buy, 100, 4))
	if p.Trades[0].Maker != "a" || p.Trades[0].Quantity != 3 {
		t.Fatal(p)
	}
}
func TestValidationArithmeticAndTransitions(t *testing.T) {
	i := Instruments[0]
	good := order("x", Buy, 100, 10)
	cases := []Order{good, good, good, good, good, good}
	cases[0].Quantity = 0
	cases[1].Price = -1
	cases[2].Side = "BAD"
	cases[3].Type = "FOK"
	cases[4].Price = math.MaxInt64
	cases[5].TIF = "FOK"
	for _, o := range cases {
		if Validate(o, i) == nil {
			t.Fatal(o)
		}
	}
	if _, e := Mul(math.MaxInt64, 2); e == nil {
		t.Fatal("overflow accepted")
	}
	if _, e := ReserveBuy(math.MaxInt64, 1); e == nil {
		t.Fatal("fee overflow accepted")
	}
	if Fee(1) != 1 || Fee(1000) != 1 || Fee(1001) != 2 {
		t.Fatal("fee rounding")
	}
	if r, e := ReserveBuy(100, 10); e != nil || r != 1010 {
		t.Fatal(r, e)
	}
	if Transition(Filled, New) == nil || Transition(Cancelled, Filled) == nil {
		t.Fatal("terminal transition")
	}
}
func TestRandomInvariantsAndReplay(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	b := NewBook("BTC-USD")
	replay := NewBook("BTC-USD")
	for n := 0; n < 3000; n++ {
		o := order(fmt.Sprint(n), Buy, 90+int64(rng.Intn(21)), int64(1+rng.Intn(15)))
		if rng.Intn(2) == 0 {
			o.Side = Sell
		}
		o.Account = fmt.Sprint(rng.Intn(8))
		p := apply(t, b, o)
		for _, c := range p.Changes {
			v := c.After
			if v.Filled+v.Remaining != v.Quantity || v.Remaining < 0 || v.Filled > v.Quantity {
				t.Fatal(v)
			}
		}
		for _, tr := range p.Trades {
			if tr.Buyer == tr.Seller || tr.Quantity <= 0 {
				t.Fatal(tr)
			}
		}
		if e := replay.Apply(p, replay.Sequence+1); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(b.Orders(), replay.Orders()) {
			t.Fatal("nondeterministic replay")
		}
		if e := b.Check(); e != nil {
			t.Fatal(e)
		}
	}
}
func TestSnapshotRestoreFIFO(t *testing.T) {
	b := NewBook("BTC-USD")
	for n := 0; n < 30; n++ {
		apply(t, b, order(fmt.Sprint(n), Sell, 100, 3))
	}
	c, e := Restore(b.Instrument, b.Sequence, b.Orders())
	if e != nil {
		t.Fatal(e)
	}
	p := apply(t, c, order("take", Buy, 100, 4))
	if p.Trades[0].Maker != "0" || p.Trades[1].Maker != "1" {
		t.Fatal(p)
	}
}
func FuzzConservation(f *testing.F) {
	f.Add(int64(100), int64(50))
	f.Add(int64(math.MaxInt64), int64(2))
	f.Fuzz(func(t *testing.T, p, q int64) {
		v, e := Mul(p, q)
		if e == nil && p > 0 && q > 0 {
			if v/p != q || v < 0 {
				t.Fatal("multiplication error")
			}
			fee := Fee(v)
			if fee < 0 || fee > v {
				t.Fatal("fee out of range")
			}
		}
	})
}
