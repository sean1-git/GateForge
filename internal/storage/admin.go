package storage

import (
	"context"
	"errors"
	"time"

	"gateforge/internal/adminauth"
	"github.com/jackc/pgx/v5"
)

func (s *Postgres) CreateAdminSession(ctx context.Context, issuer, subject, email, hash string, expires time.Time) (adminauth.Identity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return adminauth.Identity{}, err
	}
	defer tx.Rollback(ctx)
	id := adminauth.Identity{ID: adminauth.Hash(issuer + "\x00" + subject), Email: email}
	// Bind an approved email to its provider subject on first sign-in. A later
	// different subject must be explicitly reviewed instead of taking over.
	var saved string
	err = tx.QueryRow(ctx, `INSERT INTO gateforge_admins(id,email) VALUES($1,$2)
		ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING id`, id.ID, email).Scan(&saved)
	if err != nil {
		return adminauth.Identity{}, err
	}
	if saved != id.ID {
		return adminauth.Identity{}, adminauth.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateforge_admin_sessions WHERE expires_at<=now()`); err != nil {
		return adminauth.Identity{}, err
	}
	// Repeated sign-ins must not accumulate unlimited active sessions. Reserve
	// one of the eight allowed slots for the session being created.
	if _, err = tx.Exec(ctx, `DELETE FROM gateforge_admin_sessions WHERE admin_id=$1 AND token_hash NOT IN
		(SELECT token_hash FROM gateforge_admin_sessions WHERE admin_id=$1 ORDER BY created_at DESC, token_hash DESC LIMIT 7)`, id.ID); err != nil {
		return adminauth.Identity{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO gateforge_admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2,$3)`, hash, id.ID, expires); err != nil {
		return adminauth.Identity{}, err
	}
	if err = audit(WithActor(ctx, id.ID), tx, "administrator.signed_in", "session"); err != nil {
		return adminauth.Identity{}, err
	}
	return id, tx.Commit(ctx)
}

func (s *Postgres) AdminSession(ctx context.Context, hash string) (adminauth.Session, error) {
	var result adminauth.Session
	err := s.pool.QueryRow(ctx, `SELECT a.id,a.email,s.expires_at FROM gateforge_admin_sessions s
		JOIN gateforge_admins a ON a.id=s.admin_id WHERE s.token_hash=$1 AND s.expires_at>now()`, hash).Scan(&result.Identity.ID, &result.Identity.Email, &result.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, adminauth.ErrUnauthorized
	}
	return result, err
}
func (s *Postgres) DeleteAdminSession(ctx context.Context, hash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `DELETE FROM gateforge_admin_sessions WHERE token_hash=$1 RETURNING admin_id`, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = audit(WithActor(ctx, id), tx, "administrator.signed_out", "session"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Postgres) SaveAdminLogin(ctx context.Context, f adminauth.LoginFlow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM gateforge_admin_logins WHERE expires_at<=now()`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO gateforge_admin_logins(state_hash,browser_hash,nonce,verifier,expires_at) VALUES($1,$2,$3,$4,$5)`, f.StateHash, f.BrowserHash, f.Nonce, f.Verifier, f.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Postgres) ConsumeAdminLogin(ctx context.Context, state, browser string) (adminauth.LoginFlow, error) {
	var f adminauth.LoginFlow
	err := s.pool.QueryRow(ctx, `DELETE FROM gateforge_admin_logins WHERE state_hash=$1 AND browser_hash=$2 AND expires_at>now() RETURNING state_hash,browser_hash,nonce,verifier,expires_at`, state, browser).Scan(&f.StateHash, &f.BrowserHash, &f.Nonce, &f.Verifier, &f.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = adminauth.ErrUnauthorized
	}
	return f, err
}
