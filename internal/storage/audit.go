package storage

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

type actorKey struct{}

func WithActor(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, actorKey{}, id)
}
func actor(ctx context.Context) string {
	if id, ok := ctx.Value(actorKey{}).(string); ok && id != "" {
		return id
	}
	return "system"
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	CreatedAt time.Time `json:"created_at"`
}
type AuditPage struct {
	Events     []AuditEvent `json:"events"`
	NextCursor int64        `json:"next_cursor,omitempty"`
}

// Called inside the same transaction as the change. No credentials, request
// bodies, personal IPs, or arbitrary user-provided text enter the audit record.
func audit(ctx context.Context, tx pgx.Tx, action, resource string) error {
	_, err := tx.Exec(ctx, `INSERT INTO gateforge_audit(actor,action,resource) VALUES($1,$2,$3)`, actor(ctx), action, resource)
	return err
}
func (s *Postgres) ListAudit(ctx context.Context, before int64, limit int) (AuditPage, error) {
	if before < 0 || limit < 1 || limit > 200 {
		return AuditPage{}, errors.New("invalid audit pagination")
	}
	query := `SELECT id,actor,action,resource,created_at FROM gateforge_audit ORDER BY id DESC LIMIT $1`
	args := []any{limit + 1}
	if before > 0 {
		query = `SELECT id,actor,action,resource,created_at FROM gateforge_audit WHERE id<$2 ORDER BY id DESC LIMIT $1`
		args = append(args, before)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	result := AuditPage{Events: []AuditEvent{}}
	for rows.Next() {
		var event AuditEvent
		if err = rows.Scan(&event.ID, &event.Actor, &event.Action, &event.Resource, &event.CreatedAt); err != nil {
			return AuditPage{}, err
		}
		result.Events = append(result.Events, event)
	}
	if err = rows.Err(); err != nil {
		return AuditPage{}, err
	}
	if len(result.Events) > limit {
		result.Events = result.Events[:limit]
		result.NextCursor = result.Events[limit-1].ID
	}
	return result, nil
}
