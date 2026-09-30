package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gateforge/internal/config"
	"gateforge/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("configuration changed; refresh before saving")

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
func (s *Postgres) Save(ctx context.Context, expected int64, routes []config.Route) (Snapshot, error) {
	data, err := json.Marshal(routes)
	if err != nil {
		return Snapshot{}, err
	}
	var revision int64
	err = s.pool.QueryRow(ctx, `UPDATE gateforge_config SET routes=$1,revision=revision+1 WHERE id=1 AND revision=$2 RETURNING revision`, data, expected).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrConflict
	}
	return Snapshot{revision, routes}, err
}
func (s *Postgres) LookupKey(ctx context.Context, hash string) (security.Key, error) {
	var k security.Key
	err := s.pool.QueryRow(ctx, `SELECT id,name,prefixes,expires_at,created_at,revoked FROM gateforge_keys WHERE key_hash=$1`, hash).Scan(&k.ID, &k.Name, &k.Prefixes, &k.ExpiresAt, &k.CreatedAt, &k.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		err = security.ErrInvalidKey
	}
	return k, err
}
func (s *Postgres) CreateKey(ctx context.Context, name string, prefixes []string, expires time.Time) (security.Key, string, error) {
	raw, err := security.GenerateKey()
	if err != nil {
		return security.Key{}, "", err
	}
	k := security.Key{ID: security.Hash(raw)[:24], Name: name, Prefixes: prefixes, ExpiresAt: expires}
	err = s.pool.QueryRow(ctx, `INSERT INTO gateforge_keys(id,name,key_hash,prefixes,expires_at) VALUES($1,$2,$3,$4,$5) RETURNING created_at`, k.ID, name, security.Hash(raw), prefixes, expires).Scan(&k.CreatedAt)
	if err != nil {
		return security.Key{}, "", err
	}
	return k, raw, nil
}
func (s *Postgres) ListKeys(ctx context.Context) ([]security.Key, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,prefixes,expires_at,created_at,revoked FROM gateforge_keys ORDER BY created_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []security.Key{}
	for rows.Next() {
		var k security.Key
		if err = rows.Scan(&k.ID, &k.Name, &k.Prefixes, &k.ExpiresAt, &k.CreatedAt, &k.Revoked); err != nil {
			return nil, err
		}
		result = append(result, k)
	}
	return result, rows.Err()
}
func (s *Postgres) RevokeKey(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE gateforge_keys SET revoked=true WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return security.ErrInvalidKey
	}
	return err
}
