package core

import (
	"fmt"
	"testing"
)

func TestExecutionCountProtection(t *testing.T) {
	b := NewBook("BTC-USD")
	for n := 0; n < MaxExecutions+2; n++ {
		apply(t, b, order(fmt.Sprint(n), Sell, 100, 1))
	}
	p := apply(t, b, order("sweep", Buy, 100, MaxExecutions+2))
	o := p.Changes[len(p.Changes)-1].After
	if len(p.Trades) != MaxExecutions || o.Remaining != 2 || o.Status != Cancelled {
		t.Fatal("unbounded sweep or crossed remainder", o)
	}
	if e := b.Check(); e != nil {
		t.Fatal(e)
	}
}
