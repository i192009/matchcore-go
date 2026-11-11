package store

import (
	"context"
	"errors"

	"github.com/matchcore/matchcore-go/internal/observability"
)

// PublishBatch serializes publishers with a transactional advisory lock. A
// successful callback must mean a durable broker acknowledgement was received.
// An acknowledged event may be delivered again after a crash before SQL commit.
func (s *Store) PublishBatch(ctx context.Context, publish func(id, instrument string, payload []byte) error) (int, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var locked bool
	if e = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(781632951)").Scan(&locked); e != nil {
		return 0, e
	}
	if !locked {
		return 0, nil
	}
	var backlog int64
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&backlog); e != nil {
		return 0, e
	}
	observability.Backlog.Set(float64(backlog))
	rows, e := tx.Query(ctx, "SELECT e.id,e.instrument,e.payload FROM outbox o JOIN events e ON e.id=o.event_id WHERE o.published_at IS NULL ORDER BY e.instrument,e.sequence LIMIT 100 FOR UPDATE OF o")
	if e != nil {
		return 0, e
	}
	type item struct {
		id, instrument string
		payload        []byte
	}
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.instrument, &v.payload); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, v := range items {
		if e = publish(v.id, v.instrument, v.payload); e != nil {
			observability.PublishErrors.Inc()
			return 0, e
		}
		tag, err := tx.Exec(ctx, "UPDATE outbox SET published_at=now(),attempts=attempts+1 WHERE event_id=$1", v.id)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() != 1 {
			return 0, errors.New("outbox row disappeared")
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return len(items), nil
}
