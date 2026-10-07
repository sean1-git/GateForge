package storage

import (
	"context"
	"encoding/json"
	"gateforge/internal/analytics"
	"time"
)

// Source progress and totals commit together. A retry after an uncertain commit
// is idempotent, and different gateway processes contribute without overwriting.
func (s *Postgres) SaveMetrics(ctx context.Context, instance string, current analytics.Snapshot) error {
	if len(current.Routes) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO gateforge_metric_sources(id,snapshot) VALUES($1,'{}') ON CONFLICT DO NOTHING`, instance); err != nil {
		return err
	}
	var raw []byte
	var previous, total analytics.Snapshot
	if err = tx.QueryRow(ctx, `SELECT snapshot FROM gateforge_metric_sources WHERE id=$1 FOR UPDATE`, instance).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &previous); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT snapshot FROM gateforge_metric_totals WHERE id=1 FOR UPDATE`).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &total); err != nil {
		return err
	}
	total = analytics.Accumulate(total, previous, current)
	total.PersistedAt = time.Now().UTC()
	raw, err = json.Marshal(total)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateforge_metric_totals SET snapshot=$1 WHERE id=1`, raw); err != nil {
		return err
	}
	// An older retry must not move the checkpoint backward, or the next flush
	// would count already-committed requests again.
	current = analytics.Accumulate(previous, previous, current)
	raw, err = json.Marshal(current)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateforge_metric_sources SET snapshot=$1,updated_at=now() WHERE id=$2`, raw, instance); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Postgres) ReadMetrics(ctx context.Context) (analytics.Snapshot, error) {
	var raw []byte
	var result analytics.Snapshot
	err := s.pool.QueryRow(ctx, `SELECT snapshot FROM gateforge_metric_totals WHERE id=1`).Scan(&raw)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	result.Scope = "shared"
	if result.Routes == nil {
		result.Routes = []analytics.Row{}
	}
	return result, err
}
