package storage

import (
	"context"
	"fmt"
	"gateforge/internal/config"
	"gateforge/internal/security"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in rehearsal in disposable databases, never restore over the source.
func TestBackupRestore(t *testing.T) {
	if os.Getenv("GATEFORGE_TEST_BACKUP_RESTORE") != "1" {
		t.Skip("enable the isolated backup restore rehearsal explicitly")
	}
	raw := os.Getenv("GATEFORGE_TEST_DATABASE_URL")
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("restore rehearsal requires a dedicated database ending in _test")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal("restore tools missing:", tool)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	control, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal("connect to disposable test database")
	}
	defer control.Close(context.Background())
	source := fmt.Sprintf("gf_backup_%x", time.Now().UnixNano())
	target := source + "_restored"
	for _, name := range []string{source, target} {
		if _, err = control.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal("create isolated restore database", err)
		}
		defer func(name string) {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if _, err := control.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error("remove isolated database", err)
			}
		}(name)
	}
	connect := func(name string) *Postgres {
		copy := *u
		copy.Path = "/" + name
		db, err := Open(ctx, copy.String())
		if err != nil {
			t.Fatal("open rehearsal database")
		}
		return db
	}
	db := connect(source)
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	routes := []config.Route{{Prefix: "/users", Upstream: "http://localhost:9001", Auth: "api_key"}}
	if err = db.Seed(ctx, routes); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	key, secret, err := db.CreateKey(ctx, "restore-fixture", []string{"/users"}, expiry, "restore-request-1234")
	if err != nil {
		t.Fatal(err)
	}
	revoked, revokedSecret, err := db.CreateKey(ctx, "revoked-fixture", []string{"/users"}, expiry)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RevokeKey(ctx, revoked.ID); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal("parse test connection")
	}
	archive := filepath.Join(t.TempDir(), "gateway.dump")
	run := func(tool, database string, args ...string) {
		cmd := exec.CommandContext(ctx, tool, args...)
		cmd.Env = append(os.Environ(), "PGHOST="+cfg.Host, fmt.Sprintf("PGPORT=%d", cfg.Port), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+database, "PGSSLMODE=disable")
		if output, err := cmd.CombinedOutput(); err != nil {
			_ = output
			t.Fatalf("%s failed (credential-bearing output omitted): %v", tool, err)
		}
	}
	run("pg_dump", source, "--format=custom", "--no-owner", "--file="+archive)
	run("pg_restore", target, "--exit-on-error", "--no-owner", "--dbname="+target, archive)
	restored := connect(target)
	defer restored.Close()
	snap, err := restored.Load(ctx)
	if err != nil || len(snap.Routes) != 1 || snap.Routes[0].Prefix != "/users" {
		t.Fatal("configuration did not survive restore", err)
	}
	active, err := restored.LookupKey(ctx, security.Hash(secret))
	if err != nil || active.ID != key.ID || active.Revoked {
		t.Fatal("active key did not survive restore", err)
	}
	dead, err := restored.LookupKey(ctx, security.Hash(revokedSecret))
	if err != nil || !dead.Revoked {
		t.Fatal("revocation did not survive restore", err)
	}
	if _, _, err = restored.CreateKey(ctx, "restore-fixture", []string{"/users"}, expiry, "restore-request-1234"); err != ErrDuplicateRequest {
		t.Fatal("deduplication did not survive restore", err)
	}
	var count int
	if err = restored.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename='gateforge_keys' AND indexname IN ('gateforge_keys_page_idx','gateforge_keys_request_id_idx')`).Scan(&count); err != nil || count != 2 {
		t.Fatal("indexes missing after restore", err)
	}
	t.Log("Restored configuration, hashed credentials, revocation, idempotency and indexes into a separate database")
}
