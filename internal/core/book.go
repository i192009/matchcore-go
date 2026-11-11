package core

import (
	"container/list"
	"errors"
	"fmt"
)

// A deterministic treap indexes price levels. Each level is a FIFO list;
// the order map points directly into that list for constant-time cancellation.
type node struct {
	price, quantity int64
	priority        uint64
	queue           *list.List
	left, right     *node
}
type entry struct {
	level   *node
	element *list.Element
	side    Side
}
type Book struct {
	bids, asks *node
	index      map[string]entry
	Sequence   int64
	Instrument string
}

func (b *Book) Len() int      { return len(b.index) }
func NewBook(id string) *Book { return &Book{Instrument: id, index: make(map[string]entry)} }
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
func rotateRight(n *node) *node { q := n.left; n.left = q.right; q.right = n; return q }
func rotateLeft(n *node) *node  { q := n.right; n.right = q.left; q.left = n; return q }
func insert(n *node, p int64) (*node, *node) {
	if n == nil {
		q := &node{price: p, priority: mix(uint64(p)), queue: list.New()}
		return q, q
	}
	if p == n.price {
		return n, n
	}
	var q *node
	if p < n.price {
		n.left, q = insert(n.left, p)
		if n.left.priority < n.priority {
			n = rotateRight(n)
		}
	} else {
		n.right, q = insert(n.right, p)
		if n.right.priority < n.priority {
			n = rotateLeft(n)
		}
	}
	return n, q
}
func remove(n *node, p int64) *node {
	if n == nil {
		return nil
	}
	if p < n.price {
		n.left = remove(n.left, p)
	} else if p > n.price {
		n.right = remove(n.right, p)
	} else {
		if n.left == nil {
			return n.right
		}
		if n.right == nil {
			return n.left
		}
		if n.left.priority < n.right.priority {
			n = rotateRight(n)
			n.right = remove(n.right, p)
		} else {
			n = rotateLeft(n)
			n.left = remove(n.left, p)
		}
	}
	return n
}
func next(n *node, p int64, ascending bool) *node {
	var best *node
	for n != nil {
		if ascending {
			if n.price > p {
				best = n
				n = n.left
			} else {
				n = n.right
			}
		} else {
			if n.price < p {
				best = n
				n = n.right
			} else {
				n = n.left
			}
		}
	}
	return best
}
func (b *Book) Get(id string) (Order, bool) {
	e, ok := b.index[id]
	if !ok {
		return Order{}, false
	}
	return e.element.Value.(Order), true
}
func (b *Book) erase(id string) {
	e, ok := b.index[id]
	if !ok {
		return
	}
	o := e.element.Value.(Order)
	e.level.quantity -= o.Remaining
	e.level.queue.Remove(e.element)
	delete(b.index, id)
	if e.level.queue.Len() == 0 {
		if e.side == Buy {
			b.bids = remove(b.bids, e.level.price)
		} else {
			b.asks = remove(b.asks, e.level.price)
		}
	}
}
func (b *Book) add(o Order) {
	var n *node
	if o.Side == Buy {
		b.bids, n = insert(b.bids, o.Price)
	} else {
		b.asks, n = insert(b.asks, o.Price)
	}
	n.quantity += o.Remaining
	b.index[o.ID] = entry{n, n.queue.PushBack(o), o.Side}
}

// Apply preserves queue position for a partially filled maker.
func (b *Book) Apply(p Plan, seq int64) error {
	if seq != b.Sequence+1 {
		return errors.New("non-contiguous sequence")
	}
	for _, c := range p.Changes {
		if c.Before != nil {
			old, ok := b.Get(c.Before.ID)
			if c.Before.Open() && (!ok || old != *c.Before) {
				return errors.New("state precondition mismatch")
			}
		}
		if e, ok := b.index[c.After.ID]; ok {
			if c.After.Open() {
				e.level.quantity += c.After.Remaining - e.element.Value.(Order).Remaining
				e.element.Value = c.After
			} else {
				b.erase(c.After.ID)
			}
		} else if c.After.Open() {
			b.add(c.After)
		}
	}
	b.Sequence = seq
	return b.Check()
}
func (b *Book) Depth(limit int) Depth {
	d := Depth{Instrument: b.Instrument, Sequence: b.Sequence, Bids: []Level{}, Asks: []Level{}}
	for _, side := range []Side{Buy, Sell} {
		root := b.bids
		p := int64(9223372036854775807)
		asc := false
		if side == Sell {
			root = b.asks
			p = 0
			asc = true
		}
		for n := next(root, p, asc); n != nil; n = next(root, p, asc) {
			if side == Buy {
				d.Bids = append(d.Bids, Level{n.price, n.quantity})
				if len(d.Bids) >= limit {
					break
				}
			} else {
				d.Asks = append(d.Asks, Level{n.price, n.quantity})
				if len(d.Asks) >= limit {
					break
				}
			}
			p = n.price
		}
	}
	return d
}
func (b *Book) Orders() []Order {
	o := make([]Order, 0, len(b.index))
	// Canonical price-level traversal avoids sorting large order structs, while
	// retaining the exact FIFO order required to restore each queue.
	var visit func(*node)
	visit = func(n *node) {
		if n == nil {
			return
		}
		visit(n.left)
		for el := n.queue.Front(); el != nil; el = el.Next() {
			o = append(o, el.Value.(Order))
		}
		visit(n.right)
	}
	visit(b.bids)
	visit(b.asks)
	return o
}
func Restore(id string, seq int64, orders []Order) (*Book, error) {
	b := NewBook(id)
	for _, o := range orders {
		if !o.Open() || o.Instrument != id || o.Remaining <= 0 || o.Filled < 0 || o.Filled > o.Quantity || o.Remaining != o.Quantity-o.Filled || o.Type != "LIMIT" || o.TIF != "GTC" || o.Price <= 0 || o.Sequence > seq {
			return nil, errors.New("invalid snapshot order")
		}
		if _, ok := b.index[o.ID]; ok {
			return nil, errors.New("duplicate snapshot order")
		}
		b.add(o)
	}
	b.Sequence = seq
	return b, b.Check()
}
func (b *Book) Check() error {
	d := b.Depth(1)
	if len(d.Bids) > 0 && len(d.Asks) > 0 && d.Bids[0].Price >= d.Asks[0].Price {
		return errors.New("crossed book")
	}
	return nil
}

// Plan never mutates the book. Its inputs contain all time/identity decisions.
func (b *Book) Plan(o Order, seq, now int64) (Plan, error) {
	if _, exists := b.index[o.ID]; exists {
		return Plan{}, errors.New("duplicate order id")
	}
	i, e := FindInstrument(o.Instrument)
	if e != nil {
		return Plan{}, e
	}
	if e = Validate(o, i); e != nil {
		return Plan{}, e
	}
	if o.Instrument != b.Instrument || seq != b.Sequence+1 {
		return Plan{}, errors.New("wrong instrument or sequence")
	}
	o.Remaining = o.Quantity
	o.Status = New
	o.Sequence = seq
	o.Created = now
	o.Updated = now
	p := Plan{Changes: []Change{}, Trades: []Trade{}}
	root := b.asks
	asc := true
	price := int64(0)
	if o.Side == Sell {
		root = b.bids
		asc = false
		price = 9223372036854775807
	}
	spent := int64(0)
	stop := false
	for n := next(root, price, asc); n != nil && o.Remaining > 0 && !stop; n = next(root, price, asc) {
		if o.Type == "LIMIT" && ((o.Side == Buy && n.price > o.Price) || (o.Side == Sell && n.price < o.Price)) {
			break
		}
		for el := n.queue.Front(); el != nil && o.Remaining > 0; el = el.Next() {
			if len(p.Trades) >= MaxExecutions {
				o.Reason = "execution count protection exhausted"
				stop = true
				break
			}
			maker := el.Value.(Order)
			p.Attempts++
			if maker.Account == o.Account {
				o.Reason = "self-trade prevention: cancel incoming remainder"
				stop = true
				break
			}
			q := min(o.Remaining, maker.Remaining)
			// Bound market sells as well as buys by the configured notional exposure.
			remainingNotional := i.MaxNotional - o.Notional
			q = min(q, remainingNotional/n.price)
			if o.Type == "MARKET" && o.Side == Buy {
				lo, hi := int64(0), q
				for lo < hi {
					m := lo + (hi-lo+1)/2
					v, err := Mul(n.price, m)
					if err == nil && v <= o.MaxSpend-spent && Fee(v) <= o.MaxSpend-spent-v {
						lo = m
					} else {
						hi = m - 1
					}
				}
				q = lo
			}
			if q == 0 {
				o.Reason = "price protection exhausted"
				stop = true
				break
			}
			value, err := Mul(n.price, q)
			if err != nil {
				return Plan{}, err
			}
			fee := Fee(value)
			spent += value + fee
			before := maker
			fill := func(v *Order) {
				v.Remaining -= q
				v.Filled += q
				v.Notional += value
				v.AveragePrice = v.Notional / v.Filled
				v.Fees += fee
				v.Updated = now
				v.Status = Partial
				if v.Remaining == 0 {
					v.Status = Filled
				}
			}
			fill(&maker)
			fill(&o)
			p.Changes = append(p.Changes, Change{&before, maker})
			buyer, seller := o.Account, maker.Account
			if o.Side == Sell {
				buyer, seller = maker.Account, o.Account
			}
			p.Trades = append(p.Trades, Trade{fmt.Sprintf("%s:%d:%d", b.Instrument, seq, len(p.Trades)), b.Instrument, maker.ID, o.ID, buyer, seller, n.price, q, value, fee, seq, now})
		}
		price = n.price
	}
	if o.Remaining > 0 && (o.Type == "MARKET" || o.TIF == "IOC" || stop) {
		o.Status = Cancelled
		if o.Reason == "" {
			o.Reason = "IOC remainder"
		}
	}
	p.Changes = append(p.Changes, Change{After: o})
	return p, nil
}
func (b *Book) Cancel(id, account string, now int64) (Plan, error) {
	o, ok := b.Get(id)
	if !ok {
		return Plan{}, errors.New("order not open")
	}
	if o.Account != account {
		return Plan{}, errors.New("account ownership mismatch")
	}
	before := o
	o.Status = Cancelled
	o.Updated = now
	return Plan{Changes: []Change{{&before, o}}, Trades: []Trade{}}, nil
}
