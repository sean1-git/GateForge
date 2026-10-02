package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"gateforge/internal/config"
	"gateforge/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("configuration changed; refresh before saving")
var ErrDuplicateRequest = errors.New("this key request already completed; check the key list and revoke it if its secret was lost")
var ErrRequestConflict = errors.New("idempotency key was already used for a different request")

type Snapshot struct {
	Revision int64          `json:"revision"`
	Routes   []config.Route `json:"routes"`
}
type Postgres struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Postgres, error) {
	c, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	c.MaxConns = 10
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	p, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, errors.New("could not create PostgreSQL pool")
	}
	s := &Postgres{p}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, errors.New("PostgreSQL is unavailable")
	}
	return s, nil
}
func (s *Postgres) Close()                         { s.pool.Close() }
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// DDL is transactional and serialized across instances. Schema changes are additive.
func (s *Postgres) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(71642319)`); err != nil {
		return err
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS gateforge_config (id integer PRIMARY KEY CHECK(id=1), revision bigint NOT NULL, routes jsonb NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS gateforge_keys (id text PRIMARY KEY, name text NOT NULL, key_hash text UNIQUE NOT NULL, prefixes text[] NOT NULL, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), revoked boolean NOT NULL DEFAULT false)`,
		`ALTER TABLE gateforge_keys ADD COLUMN IF NOT EXISTS request_id text`,
		`ALTER TABLE gateforge_keys ADD COLUMN IF NOT EXISTS request_hash text`,
		`CREATE UNIQUE INDEX IF NOT EXISTS gateforge_keys_request_id_idx ON gateforge_keys (request_id)`,
		`CREATE INDEX IF NOT EXISTS gateforge_keys_page_idx ON gateforge_keys (created_at DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS gateforge_admins (id text PRIMARY KEY, email text UNIQUE NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS gateforge_admin_sessions (token_hash text PRIMARY KEY, admin_id text NOT NULL REFERENCES gateforge_admins(id), expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE INDEX IF NOT EXISTS gateforge_admin_sessions_expiry_idx ON gateforge_admin_sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS gateforge_admin_sessions_user_idx ON gateforge_admin_sessions(admin_id,created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS gateforge_admin_logins (state_hash text PRIMARY KEY, browser_hash text NOT NULL, nonce text NOT NULL, verifier text NOT NULL, expires_at timestamptz NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS gateforge_admin_logins_expiry_idx ON gateforge_admin_logins(expires_at)`,
		`CREATE TABLE IF NOT EXISTS gateforge_metric_totals (id integer PRIMARY KEY CHECK(id=1), snapshot jsonb NOT NULL)`,
		`INSERT INTO gateforge_metric_totals(id,snapshot) VALUES(1,'{}') ON CONFLICT DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS gateforge_metric_sources (id text PRIMARY KEY, snapshot jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS gateforge_audit (id bigserial PRIMARY KEY, actor text NOT NULL, action text NOT NULL, resource text NOT NULL, created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE OR REPLACE FUNCTION gateforge_protect_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit records are append-only'; END; $$`,
		`DROP TRIGGER IF EXISTS gateforge_audit_immutable ON gateforge_audit`,
		`CREATE TRIGGER gateforge_audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON gateforge_audit FOR EACH STATEMENT EXECUTE FUNCTION gateforge_protect_audit()`,
	} {
		if _, err = tx.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *Postgres) Seed(ctx context.Context, routes []config.Route) error {
	data, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO gateforge_config(id,revision,routes) VALUES(1,1,$1) ON CONFLICT(id) DO NOTHING`, data)
	return err
}
func (s *Postgres) Load(ctx context.Context) (Snapshot, error) {
	var result Snapshot
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT revision,routes FROM gateforge_config WHERE id=1`).Scan(&result.Revision, &data)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result.Routes)
	return result, err
}

// Avoid transferring and decoding unchanged route JSON on every refresh.
func (s *Postgres) LoadAfter(ctx context.Context, revision int64) (Snapshot, error) {
	var result Snapshot
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT revision,routes FROM gateforge_config WHERE id=1 AND revision>$1`, revision).Scan(&result.Revision, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result.Routes)
	return result, err
}
func (s *Postgres) Save(ctx context.Context, expected int64, routes []config.Route) (Snapshot, error) {
	data, err := json.Marshal(routes)
	if err != nil {
		return Snapshot{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	err = tx.QueryRow(ctx, `UPDATE gateforge_config SET routes=$1,revision=revision+1 WHERE id=1 AND revision=$2 RETURNING revision`, data, expected).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrConflict
	}
	if err != nil {
		return Snapshot{}, err
	}
	if err = audit(ctx, tx, "routes.updated", "revision:"+strconv.FormatInt(revision, 10)); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{revision, routes}, tx.Commit(ctx)
}
func (s *Postgres) LookupKey(ctx context.Context, hash string) (security.Key, error) {
	var k security.Key
	err := s.pool.QueryRow(ctx, `SELECT id,name,prefixes,expires_at,created_at,revoked FROM gateforge_keys WHERE key_hash=$1`, hash).Scan(&k.ID, &k.Name, &k.Prefixes, &k.ExpiresAt, &k.CreatedAt, &k.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		err = security.ErrInvalidKey
	}
	return k, err
}
func (s *Postgres) CreateKey(ctx context.Context, name string, prefixes []string, expires time.Time, requestID ...string) (security.Key, string, error) {
	raw, err := security.GenerateKey()
	if err != nil {
		return security.Key{}, "", err
	}
	k := security.Key{ID: security.Hash(raw)[:24], Name: name, Prefixes: prefixes, ExpiresAt: expires}
	var id *string
	if len(requestID) > 0 && requestID[0] != "" {
		id = &requestID[0]
	}
	payload, err := json.Marshal(struct {
		Name     string
		Prefixes []string
		Expires  time.Time
	}{name, prefixes, expires.UTC()})
	if err != nil {
		return security.Key{}, "", err
	}
	fingerprint := security.Hash(string(payload))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return security.Key{}, "", err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO gateforge_keys(id,name,key_hash,prefixes,expires_at,request_id,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (request_id) DO NOTHING RETURNING created_at`, k.ID, name, security.Hash(raw), prefixes, expires, id, fingerprint).Scan(&k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) && id != nil {
		var previous string
		if err = tx.QueryRow(ctx, `SELECT id,request_hash FROM gateforge_keys WHERE request_id=$1`, *id).Scan(&k.ID, &previous); err != nil {
			return security.Key{}, "", err
		}
		if previous != fingerprint {
			return security.Key{}, "", ErrRequestConflict
		}
		return k, "", ErrDuplicateRequest
	}
	if err != nil {
		return security.Key{}, "", err
	}
	if err = audit(ctx, tx, "key.created", k.ID); err != nil {
		return security.Key{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return security.Key{}, "", err
	}
	return k, raw, nil
}
func (s *Postgres) ListKeys(ctx context.Context, query KeyQuery) (KeyPage, error) {
	if query.Limit < 1 || query.Limit > 200 {
		return KeyPage{}, errors.New("invalid page size")
	}
	statement := `SELECT id,name,prefixes,expires_at,created_at,revoked FROM gateforge_keys ORDER BY created_at DESC,id DESC LIMIT $1`
	args := []any{query.Limit + 1}
	if query.BeforeID != "" {
		statement = `SELECT id,name,prefixes,expires_at,created_at,revoked FROM gateforge_keys WHERE (created_at,id)<($2,$3) ORDER BY created_at DESC,id DESC LIMIT $1`
		args = append(args, query.BeforeTime, query.BeforeID)
	}
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return KeyPage{}, err
	}
	defer rows.Close()
	result := []security.Key{}
	for rows.Next() {
		var k security.Key
		if err = rows.Scan(&k.ID, &k.Name, &k.Prefixes, &k.ExpiresAt, &k.CreatedAt, &k.Revoked); err != nil {
			return KeyPage{}, err
		}
		result = append(result, k)
	}
	if err = rows.Err(); err != nil {
		return KeyPage{}, err
	}
	page := KeyPage{Keys: result}
	if len(result) > query.Limit {
		page.Keys = result[:query.Limit]
		last := page.Keys[len(page.Keys)-1]
		page.NextCursor = EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}
func (s *Postgres) RevokeKey(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revoked bool
	if err = tx.QueryRow(ctx, `SELECT revoked FROM gateforge_keys WHERE id=$1 FOR UPDATE`, id).Scan(&revoked); errors.Is(err, pgx.ErrNoRows) {
		return security.ErrInvalidKey
	} else if err != nil {
		return err
	}
	if revoked {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE gateforge_keys SET revoked=true WHERE id=$1`, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, "key.revoked", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
