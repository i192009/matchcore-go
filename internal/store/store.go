package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/i192009/matchcore-go/internal/core"
	"github.com/i192009/matchcore-go/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool         *pgxpool.Pool
	BeforeCommit func() error
	AfterCommit  func() error
}

func Open(ctx context.Context, url string) (*Store, error) {
	p, e := pgxpool.New(ctx, url)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return &Store{Pool: p}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	_, e := s.Pool.Exec(ctx, migrations.Schema)
	if e != nil {
		return e
	}
	for _, i := range core.Instruments {
		if _, e = s.Pool.Exec(ctx, "INSERT INTO instruments(id) VALUES($1) ON CONFLICT DO NOTHING", i.ID); e != nil {
			return e
		}
	}
	return nil
}
func Hash(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }

type Command struct {
	Kind       string     `json:"kind"`
	Key        string     `json:"key"`
	Account    string     `json:"account"`
	Instrument string     `json:"instrument"`
	Order      core.Order `json:"order"`
	OrderID    string     `json:"order_id,omitempty"`
}

func (c Command) Digest() string { v, _ := json.Marshal(c); return Hash(v) }

type Event struct {
	Type    string `json:"type"`
	OrderID string `json:"order_id,omitempty"`
	TradeID string `json:"trade_id,omitempty"`
}
type Envelope struct {
	ConfigurationVersion int         `json:"configuration_version"`
	ID                   string      `json:"id"`
	Instrument           string      `json:"instrument"`
	Sequence             int64       `json:"sequence"`
	Version              int         `json:"schema_version"`
	Correlation          string      `json:"correlation_id"`
	Timestamp            int64       `json:"timestamp"`
	Plan                 core.Plan   `json:"plan"`
	Events               []Event     `json:"events"`
	Order                *core.Order `json:"order,omitempty"`
	Error                string      `json:"error,omitempty"`
}
type Balance struct {
	Account   string `json:"account"`
	Asset     string `json:"asset"`
	Available int64  `json:"available"`
	Reserved  int64  `json:"reserved"`
	Total     int64  `json:"total"`
}
type Delta struct {
	Account   string `json:"account"`
	Asset     string `json:"asset"`
	Available int64  `json:"available_delta"`
	Reserved  int64  `json:"reserved_delta"`
}

var ErrStale = errors.New("matcher ownership or sequence lost")
var ErrAmbiguous = errors.New("commit outcome uncertain; recover before resuming")
var ErrConflict = errors.New("idempotency key reused with different request")

func (s *Store) Lookup(ctx context.Context, c Command) (*Envelope, error) {
	var digest string
	var data []byte
	e := s.Pool.QueryRow(ctx, "SELECT digest,response FROM commands WHERE account=$1 AND key=$2", c.Account, c.Key).Scan(&digest, &data)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if digest != c.Digest() {
		return nil, ErrConflict
	}
	var env Envelope
	e = json.Unmarshal(data, &env)
	return &env, e
}

// Claim holds a dedicated session advisory lock. The epoch is also checked under
// the instrument row lock in every commit, fencing an old owner after reconnect.
func (s *Store) Claim(ctx context.Context, id string) (*pgxpool.Conn, int64, error) {
	c, e := s.Pool.Acquire(ctx)
	if e != nil {
		return nil, 0, e
	}
	fail := func(err error) (*pgxpool.Conn, int64, error) {
		_ = c.Conn().Close(context.Background())
		c.Release()
		return nil, 0, err
	}
	var ok bool
	if e = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", id).Scan(&ok); e != nil {
		return fail(e)
	}
	if !ok {
		return fail(errors.New("instrument already owned"))
	}
	var epoch int64
	e = c.QueryRow(ctx, "UPDATE instruments SET epoch=epoch+1 WHERE id=$1 RETURNING epoch", id).Scan(&epoch)
	if e != nil {
		return fail(e)
	}
	return c, epoch, nil
}

func reservation(o core.Order) (int64, error) {
	if !o.Open() {
		return 0, nil
	}
	if o.Side == core.Sell {
		return o.Remaining, nil
	}
	return core.ReserveBuy(o.Price, o.Remaining)
}
func assetFor(o core.Order, i core.Instrument) string {
	if o.Side == core.Buy {
		return i.Quote
	}
	return i.Base
}

// Commit is the sole financial boundary. All participating accounts and balance
// rows are locked in lexical order, including across instrument writers.
func (s *Store) Commit(ctx context.Context, c Command, p core.Plan, rejection string, seq, epoch, now int64) (Envelope, error) {
	env := Envelope{ConfigurationVersion: 1, ID: fmt.Sprintf("%s:%d", c.Instrument, seq), Instrument: c.Instrument, Sequence: seq, Version: 1, Correlation: c.Key, Timestamp: now, Plan: p, Events: []Event{}}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if e != nil {
		return env, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var gotSeq, gotEpoch int64
	e = tx.QueryRow(ctx, "SELECT sequence,epoch FROM instruments WHERE id=$1 FOR UPDATE", c.Instrument).Scan(&gotSeq, &gotEpoch)
	if e != nil {
		return env, e
	}
	if gotEpoch != epoch || gotSeq != seq-1 {
		return env, ErrStale
	}
	// Lock the incoming account first only through a globally sorted set.
	accts := map[string]bool{c.Account: true, "fees": true}
	for _, ch := range p.Changes {
		accts[ch.After.Account] = true
	}
	names := make([]string, 0, len(accts))
	for a := range accts {
		names = append(names, a)
	}
	sort.Strings(names)
	restricted := false
	for _, a := range names {
		var r bool
		e = tx.QueryRow(ctx, "SELECT restricted FROM accounts WHERE id=$1 FOR UPDATE", a).Scan(&r)
		if e != nil {
			return env, fmt.Errorf("account unavailable: %w", e)
		}
		if a == c.Account {
			restricted = r
		}
	}
	// A competing instrument may have committed the same key while locks waited.
	var oldDigest string
	var oldResponse []byte
	e = tx.QueryRow(ctx, "SELECT digest,response FROM commands WHERE account=$1 AND key=$2", c.Account, c.Key).Scan(&oldDigest, &oldResponse)
	if e == nil {
		if oldDigest != c.Digest() {
			return env, ErrConflict
		}
		e = json.Unmarshal(oldResponse, &env)
		return env, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return env, e
	}
	balances := map[string]Balance{}
	rows, e := tx.Query(ctx, "SELECT account,asset,available,reserved FROM balances WHERE account=ANY($1) ORDER BY account,asset FOR UPDATE", names)
	if e != nil {
		return env, e
	}
	for rows.Next() {
		var b Balance
		if e = rows.Scan(&b.Account, &b.Asset, &b.Available, &b.Reserved); e != nil {
			rows.Close()
			return env, e
		}
		balances[b.Account+"/"+b.Asset] = b
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return env, e
	}
	i, e := core.FindInstrument(c.Instrument)
	if e != nil {
		return env, e
	}
	if restricted && c.Kind == "place" && rejection == "" {
		rejection = "account trading restricted"
	}
	if c.Kind == "place" && rejection == "" {
		required := c.Order.Quantity
		if c.Order.Side == core.Buy {
			if c.Order.Type == "MARKET" {
				required = c.Order.MaxSpend
			} else {
				required, e = core.ReserveBuy(c.Order.Price, c.Order.Quantity)
				if e != nil {
					return env, e
				}
			}
		}
		if balances[c.Account+"/"+assetFor(c.Order, i)].Available < required {
			rejection = "insufficient funds"
		}
		var exists bool
		e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE account=$1 AND client_id=$2)", c.Account, c.Order.ClientID).Scan(&exists)
		if e != nil {
			return env, e
		}
		if exists {
			rejection = "duplicate client order id"
		}
	}
	if rejection != "" {
		env.Plan = core.Plan{Changes: []core.Change{}, Trades: []core.Trade{}}
		env.Error = rejection
		env.Events = append(env.Events, Event{Type: "OrderRejected"})
		if c.Kind == "place" {
			o := c.Order
			o.Quantity = max(o.Quantity, 0)
			o.Remaining = o.Quantity
			o.Status = core.Rejected
			o.Reason = rejection
			o.Created, o.Updated, o.Sequence = now, now, seq
			data, err := json.Marshal(o)
			if err != nil {
				return env, err
			}
			_, err = tx.Exec(ctx, "INSERT INTO orders(id,account,client_id,instrument,status,data) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", o.ID, o.Account, o.ClientID, o.Instrument, o.Status, data)
			if err != nil {
				return env, err
			}
			env.Order = &o
		}
	} else {
		deltas := map[string]*Delta{}
		add := func(account, asset string, total, reserved int64) {
			k := account + "/" + asset
			d := deltas[k]
			if d == nil {
				d = &Delta{Account: account, Asset: asset}
				deltas[k] = d
			}
			d.Available += total - reserved
			d.Reserved += reserved
		}
		for idx, ch := range env.Plan.Changes {
			r, err := reservation(ch.After)
			if err != nil {
				return env, err
			}
			before := int64(0)
			if ch.Before != nil {
				before = ch.Before.Reserved
			}
			env.Plan.Changes[idx].After.Reserved = r
			add(ch.After.Account, assetFor(ch.After, i), 0, r-before)
			if r > before {
				env.Events = append(env.Events, Event{Type: "BalanceReserved", OrderID: ch.After.ID})
			}
			if r < before {
				env.Events = append(env.Events, Event{Type: "BalanceReleased", OrderID: ch.After.ID})
			}
		}
		for _, t := range p.Trades {
			add(t.Buyer, i.Base, t.Quantity, 0)
			add(t.Seller, i.Base, -t.Quantity, 0)
			add(t.Buyer, i.Quote, -t.Notional-t.Fee, 0)
			add(t.Seller, i.Quote, t.Notional-t.Fee, 0)
			add("fees", i.Quote, 2*t.Fee, 0)
			env.Events = append(env.Events, Event{Type: "TradeExecuted", TradeID: t.ID}, Event{Type: "SettlementCompleted", TradeID: t.ID})
		}
		conservation := map[string]int64{}
		keys := make([]string, 0, len(deltas))
		for k, d := range deltas {
			conservation[d.Asset] += d.Available + d.Reserved
			keys = append(keys, k)
		}
		for _, v := range conservation {
			if v != 0 {
				return env, errors.New("unbalanced settlement")
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			d := deltas[k]
			b, ok := balances[k]
			if !ok {
				return env, errors.New("missing balance row")
			}
			a, r := b.Available+d.Available, b.Reserved+d.Reserved
			if a < 0 || r < 0 || (d.Available > 0 && a < b.Available) || (d.Reserved > 0 && r < b.Reserved) {
				return env, errors.New("balance invariant failure")
			}
			_, e = tx.Exec(ctx, "UPDATE balances SET available=$3,reserved=$4 WHERE account=$1 AND asset=$2", d.Account, d.Asset, a, r)
			if e != nil {
				return env, e
			}
		}
		for _, ch := range env.Plan.Changes {
			previous := core.Status("")
			if ch.Before != nil {
				previous = ch.Before.Status
			}
			if e = core.Transition(previous, func() core.Status {
				if previous == "" {
					return core.New
				}
				return ch.After.Status
			}()); e != nil {
				return env, e
			}
			if previous == "" {
				if e = core.Transition(core.New, ch.After.Status); e != nil {
					return env, e
				}
			}
			data, err := json.Marshal(ch.After)
			if err != nil {
				return env, err
			}
			tag, execErr := tx.Exec(ctx, "INSERT INTO orders(id,account,client_id,instrument,status,data) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,data=EXCLUDED.data WHERE orders.account=EXCLUDED.account AND orders.client_id=EXCLUDED.client_id AND orders.instrument=EXCLUDED.instrument", ch.After.ID, ch.After.Account, ch.After.ClientID, ch.After.Instrument, ch.After.Status, data)
			e = execErr
			if e != nil {
				return env, e
			}
			if tag.RowsAffected() != 1 {
				return env, ErrConflict
			}
			kind := "OrderAccepted"
			switch ch.After.Status {
			case core.Partial:
				kind = "OrderPartiallyFilled"
			case core.Filled:
				kind = "OrderFilled"
			case core.Cancelled:
				kind = "OrderCancelled"
			}
			env.Events = append(env.Events, Event{Type: kind, OrderID: ch.After.ID})
			if ch.After.Account == c.Account && (ch.After.ID == c.Order.ID || ch.After.ID == c.OrderID) {
				o := ch.After
				env.Order = &o
			}
		}
		// Event must exist before the ledger FK can be inserted; insert below after serialization.
		if e = s.persistEvent(ctx, tx, env); e != nil {
			return env, e
		}
		for _, k := range keys {
			d := deltas[k]
			if d.Available == 0 && d.Reserved == 0 {
				continue
			}
			_, e = tx.Exec(ctx, "INSERT INTO ledger(event_id,account,asset,available_delta,reserved_delta,kind) VALUES($1,$2,$3,$4,$5,'reservation_and_settlement')", env.ID, d.Account, d.Asset, d.Available, d.Reserved)
			if e != nil {
				return env, e
			}
		}
		for _, t := range p.Trades {
			data, _ := json.Marshal(t)
			_, e = tx.Exec(ctx, "INSERT INTO trades(id,instrument,sequence,buyer,seller,data) VALUES($1,$2,$3,$4,$5,$6)", t.ID, t.Instrument, t.Sequence, t.Buyer, t.Seller, data)
			if e != nil {
				return env, e
			}
			_, e = tx.Exec(ctx, "INSERT INTO settlements(trade_id,event_id) VALUES($1,$2)", t.ID, env.ID)
			if e != nil {
				return env, e
			}
		}
	}
	if rejection != "" {
		if e = s.persistEvent(ctx, tx, env); e != nil {
			return env, e
		}
	}
	data, e := json.Marshal(env)
	if e != nil {
		return env, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO commands(account,key,digest,response) VALUES($1,$2,$3,$4)", c.Account, c.Key, c.Digest(), data)
	if e != nil {
		return env, e
	}
	_, e = tx.Exec(ctx, "UPDATE instruments SET sequence=$2 WHERE id=$1", c.Instrument, seq)
	if e != nil {
		return env, e
	}
	if s.BeforeCommit != nil {
		if e = s.BeforeCommit(); e != nil {
			return env, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return env, fmt.Errorf("%w: %v", ErrAmbiguous, e)
	}
	if s.AfterCommit != nil {
		if e = s.AfterCommit(); e != nil {
			return env, fmt.Errorf("%w: %v", ErrAmbiguous, e)
		}
	}
	return env, nil
}
func (s *Store) persistEvent(ctx context.Context, tx pgx.Tx, env Envelope) error {
	data, e := json.Marshal(env)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO events(id,instrument,sequence,schema_version,correlation_id,timestamp,payload,checksum) VALUES($1,$2,$3,1,$4,$5,$6,$7)", env.ID, env.Instrument, env.Sequence, env.Correlation, env.Timestamp, data, Hash(data))
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO outbox(event_id) VALUES($1)", env.ID)
	return e
}

func (s *Store) SeedAccount(ctx context.Context, id string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, e := tx.Exec(ctx, "INSERT INTO accounts(id) VALUES($1) ON CONFLICT DO NOTHING", id)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	for _, asset := range []string{"BTC", "ETH", "SOL", "USD"} {
		n := int64(100_000)
		if asset == "USD" {
			n = 100_000_000
		}
		if id == "fees" {
			n = 0
		}
		_, e = tx.Exec(ctx, "INSERT INTO balances(account,asset,available,reserved) VALUES($1,$2,$3,0)", id, asset, n)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "INSERT INTO supply(asset,issued) VALUES($1,$2) ON CONFLICT(asset) DO UPDATE SET issued=supply.issued+EXCLUDED.issued", asset, n)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) Balances(ctx context.Context, account string) ([]Balance, error) {
	rows, e := s.Pool.Query(ctx, "SELECT account,asset,available,reserved FROM balances WHERE account=$1 ORDER BY asset", account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Balance{}
	for rows.Next() {
		var b Balance
		if e = rows.Scan(&b.Account, &b.Asset, &b.Available, &b.Reserved); e != nil {
			return nil, e
		}
		b.Total = b.Available + b.Reserved
		result = append(result, b)
	}
	return result, rows.Err()
}
func (s *Store) Orders(ctx context.Context, account, instrument string) ([]core.Order, error) {
	rows, e := s.Pool.Query(ctx, "SELECT data FROM orders WHERE account=$1 AND ($2='' OR instrument=$2) ORDER BY (data->>'created')::bigint DESC LIMIT 200", account, instrument)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []core.Order{}
	for rows.Next() {
		var data []byte
		var o core.Order
		if e = rows.Scan(&data); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(data, &o); e != nil {
			return nil, e
		}
		result = append(result, o)
	}
	return result, rows.Err()
}
func (s *Store) Trades(ctx context.Context, instrument, account string) ([]core.Trade, error) {
	rows, e := s.Pool.Query(ctx, "SELECT data FROM trades WHERE ($1='' OR instrument=$1) AND ($2='' OR buyer=$2 OR seller=$2) ORDER BY sequence DESC,id DESC LIMIT 100", instrument, account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []core.Trade{}
	for rows.Next() {
		var data []byte
		var t core.Trade
		if e = rows.Scan(&data); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(data, &t); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) Audit(ctx context.Context, account string) ([]Delta, error) {
	rows, e := s.Pool.Query(ctx, "SELECT account,asset,available_delta,reserved_delta FROM ledger WHERE account=$1 ORDER BY id DESC LIMIT 200", account)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Delta{}
	for rows.Next() {
		var d Delta
		if e = rows.Scan(&d.Account, &d.Asset, &d.Available, &d.Reserved); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Timeout supplies bounded contexts for background operations.
func Timeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
