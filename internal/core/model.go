// Package core is a deterministic matching state machine. It has no I/O dependencies.
package core

import (
	"errors"
	"fmt"
	"math"
)

type Side string
type Status string

// MaxExecutions bounds transaction, response and downstream message sizes.
const MaxExecutions = 1024

const (
	Buy       Side   = "BUY"
	Sell      Side   = "SELL"
	New       Status = "NEW"
	Partial   Status = "PARTIALLY_FILLED"
	Filled    Status = "FILLED"
	Cancelled Status = "CANCELLED"
	Rejected  Status = "REJECTED"
)

// Quantities are 0.001 coin lots. A price tick is one USD cent per lot
// ($10 per coin). This makes every notional exactly representable in cents.
type Instrument struct {
	ID          string `json:"id"`
	Base        string `json:"base"`
	Quote       string `json:"quote"`
	LotsPerCoin int64  `json:"lots_per_coin"`
	MaxQuantity int64  `json:"max_quantity"`
	MaxNotional int64  `json:"max_notional"`
	Version     int    `json:"version"`
}

var Instruments = []Instrument{
	{"BTC-USD", "BTC", "USD", 1000, 1_000_000, 1_000_000_000_000, 1},
	{"ETH-USD", "ETH", "USD", 1000, 1_000_000, 1_000_000_000_000, 1},
	{"SOL-USD", "SOL", "USD", 1000, 1_000_000, 1_000_000_000_000, 1},
}

func FindInstrument(id string) (Instrument, error) {
	for _, i := range Instruments {
		if i.ID == id {
			return i, nil
		}
	}
	return Instrument{}, errors.New("unknown instrument")
}

type Order struct {
	ID           string `json:"id"`
	ClientID     string `json:"client_id"`
	Account      string `json:"account"`
	Instrument   string `json:"instrument"`
	Side         Side   `json:"side"`
	Type         string `json:"type"`
	TIF          string `json:"tif"`
	Price        int64  `json:"price"`
	Quantity     int64  `json:"quantity"`
	Remaining    int64  `json:"remaining"`
	Filled       int64  `json:"filled"`
	Notional     int64  `json:"notional"`
	AveragePrice int64  `json:"average_price"`
	MaxSpend     int64  `json:"max_spend"`
	Reserved     int64  `json:"reserved"`
	Fees         int64  `json:"fees"`
	Status       Status `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Created      int64  `json:"created"`
	Updated      int64  `json:"updated"`
	Sequence     int64  `json:"sequence"`
}

func (o Order) Open() bool { return o.Status == New || o.Status == Partial }
func Transition(from, to Status) error {
	if from == to {
		return nil
	}
	if from == "" && (to == New || to == Rejected) {
		return nil
	}
	if (from == New || from == Partial) && (to == Partial || to == Filled || to == Cancelled) {
		return nil
	}
	return fmt.Errorf("invalid transition %s -> %s", from, to)
}
func Mul(a, b int64) (int64, error) {
	if a < 0 || b < 0 || (a != 0 && b > math.MaxInt64/a) {
		return 0, errors.New("numeric overflow")
	}
	return a * b, nil
}

// Fee is 10 basis points, rounded up per execution. Both sides pay quote fees.
func Fee(notional int64) int64 {
	if notional <= 0 {
		return 0
	}
	return notional/1000 + boolInt(notional%1000 != 0)
}
func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
func ReserveBuy(p, q int64) (int64, error) {
	if p <= 0 || p > math.MaxInt64-Fee(p) {
		return 0, errors.New("invalid price")
	}
	return Mul(p+Fee(p), q)
}
func Validate(o Order, i Instrument) error {
	if o.ID == "" || o.ClientID == "" || len(o.ClientID) > 128 || o.Account == "" {
		return errors.New("id, client_id and account required")
	}
	if o.Side != Buy && o.Side != Sell {
		return errors.New("side must be BUY or SELL")
	}
	if o.Quantity <= 0 || o.Quantity > i.MaxQuantity {
		return errors.New("quantity outside instrument limits")
	}
	if o.Type != "LIMIT" && o.Type != "MARKET" {
		return errors.New("type must be LIMIT or MARKET")
	}
	if o.TIF != "GTC" && o.TIF != "IOC" {
		return errors.New("tif must be GTC or IOC")
	}
	if o.Type == "MARKET" && o.TIF != "IOC" {
		return errors.New("market orders require IOC")
	}
	if o.Type == "MARKET" && o.Price != 0 {
		return errors.New("market price must be zero")
	}
	if o.Type == "LIMIT" {
		n, e := Mul(o.Price, o.Quantity)
		if e != nil || o.Price <= 0 || n > i.MaxNotional {
			return errors.New("invalid price or notional")
		}
		if _, e = ReserveBuy(o.Price, o.Quantity); e != nil {
			return e
		}
	}
	if o.Type == "MARKET" && o.Side == Buy && (o.MaxSpend <= 0 || o.MaxSpend > i.MaxNotional) {
		return errors.New("market buy requires bounded max_spend")
	}
	return nil
}

type Trade struct {
	ID         string `json:"id"`
	Instrument string `json:"instrument"`
	Maker      string `json:"maker"`
	Taker      string `json:"taker"`
	Buyer      string `json:"buyer"`
	Seller     string `json:"seller"`
	Price      int64  `json:"price"`
	Quantity   int64  `json:"quantity"`
	Notional   int64  `json:"notional"`
	Fee        int64  `json:"fee"`
	Sequence   int64  `json:"sequence"`
	Timestamp  int64  `json:"timestamp"`
}
type Change struct {
	Before *Order `json:"before,omitempty"`
	After  Order  `json:"after"`
}
type Plan struct {
	Changes  []Change `json:"changes"`
	Trades   []Trade  `json:"trades"`
	Attempts int      `json:"attempts"`
}
type Level struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
}
type Depth struct {
	Instrument string  `json:"instrument"`
	Sequence   int64   `json:"sequence"`
	Bids       []Level `json:"bids"`
	Asks       []Level `json:"asks"`
}
