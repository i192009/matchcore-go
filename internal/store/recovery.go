package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/matchcore/matchcore-go/internal/core"
)

type Snapshot struct {
	Instrument core.Instrument `json:"instrument"`
	Sequence   int64           `json:"sequence"`
	Orders     []core.Order    `json:"orders"`
	Version    int             `json:"schema_version"`
}

func (s *Store) Snapshot(ctx context.Context, b *core.Book) error {
	i, e := core.FindInstrument(b.Instrument)
	if e != nil {
		return e
	}
	data, e := json.Marshal(Snapshot{i, b.Sequence, b.Orders(), 1})
	if e != nil {
		return e
	}
	_, e = s.Pool.Exec(ctx, "INSERT INTO snapshots(instrument,sequence,schema_version,payload,checksum) VALUES($1,$2,1,$3,$4) ON CONFLICT DO NOTHING", b.Instrument, b.Sequence, data, Hash(data))
	return e
}
func (s *Store) Recover(ctx context.Context, id string) (*core.Book, error) {
	b := core.NewBook(id)
	var data []byte
	var checksum string
	var version int
	var sequence int64
	e := s.Pool.QueryRow(ctx, "SELECT payload,checksum,schema_version,sequence FROM snapshots WHERE instrument=$1 ORDER BY sequence DESC LIMIT 1", id).Scan(&data, &checksum, &version, &sequence)
	if e == nil {
		if version != 1 || Hash(data) != checksum {
			return nil, errors.New("corrupt snapshot")
		}
		var snap Snapshot
		if e = json.Unmarshal(data, &snap); e != nil {
			return nil, e
		}
		i, err := core.FindInstrument(id)
		if err != nil {
			return nil, err
		}
		if snap.Version != 1 || snap.Sequence != sequence || snap.Instrument != i {
			return nil, errors.New("snapshot configuration mismatch")
		}
		b, e = core.Restore(id, snap.Sequence, snap.Orders)
		if e != nil {
			return nil, e
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	rows, e := s.Pool.Query(ctx, "SELECT payload,checksum,schema_version,sequence FROM events WHERE instrument=$1 AND sequence>$2 ORDER BY sequence", id, b.Sequence)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		if e = rows.Scan(&data, &checksum, &version, &sequence); e != nil {
			rows.Close()
			return nil, e
		}
		if version != 1 || Hash(data) != checksum {
			rows.Close()
			return nil, errors.New("corrupt authoritative event")
		}
		var env Envelope
		if e = json.Unmarshal(data, &env); e != nil {
			rows.Close()
			return nil, e
		}
		if env.Sequence != sequence || env.Version != 1 || env.ConfigurationVersion != 1 || env.Instrument != id {
			rows.Close()
			return nil, errors.New("event metadata mismatch")
		}
		if e = b.Apply(env.Plan, env.Sequence); e != nil {
			rows.Close()
			return nil, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	var committed int64
	if e = s.Pool.QueryRow(ctx, "SELECT sequence FROM instruments WHERE id=$1", id).Scan(&committed); e != nil {
		return nil, e
	}
	if committed != b.Sequence {
		return nil, errors.New("event sequence incomplete")
	}
	// Compare the replayed book with the transactional query projection as well.
	rows, e = s.Pool.Query(ctx, "SELECT data FROM orders WHERE instrument=$1 AND status IN ('NEW','PARTIALLY_FILLED')", id)
	if e != nil {
		return nil, e
	}
	expected := []core.Order{}
	for rows.Next() {
		var o core.Order
		if e = rows.Scan(&data); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal(data, &o); e != nil {
			rows.Close()
			return nil, e
		}
		expected = append(expected, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	actual := b.Orders()
	sort.Slice(expected, func(i, j int) bool { return expected[i].ID < expected[j].ID })
	sort.Slice(actual, func(i, j int) bool { return actual[i].ID < actual[j].ID })
	if !reflect.DeepEqual(actual, expected) {
		return nil, errors.New("replay/projection mismatch")
	}
	return b, nil
}
