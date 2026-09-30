package storage

import (
	"context"
	"gateforge/internal/config"
	"gateforge/internal/security"
	"gateforge/internal/testutil"
	"testing"
	"time"
)

// This test requires a disposable dedicated database; it never clears existing data.
func TestPostgresPersistence(t *testing.T) {
	url := testutil.DatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal("migration is not repeatable", err)
	}
	routes := []config.Route{{Prefix: "/users", Upstream: "http://localhost:9001", Auth: "api_key"}}
	if err = db.Seed(ctx, routes); err != nil {
		t.Fatal(err)
	}
	initial, err := db.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.Save(ctx, initial.Revision, routes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Save(ctx, initial.Revision, routes); err != ErrConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	key, raw, err := db.CreateKey(ctx, "integration", []string{"/users"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	loaded, err := second.Load(ctx)
	if err != nil || loaded.Revision != saved.Revision {
		t.Fatal("configuration was not persisted", err)
	}
	lookedUp, err := second.LookupKey(ctx, security.Hash(raw))
	if err != nil || lookedUp.ID != key.ID {
		t.Fatal("key was not persisted", err)
	}
	var storedHash string
	if err = db.pool.QueryRow(ctx, `SELECT key_hash FROM gateforge_keys WHERE id=$1`, key.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == raw || storedHash != security.Hash(raw) {
		t.Fatal("key was not stored as a hash")
	}
	if err = second.RevokeKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	lookedUp, err = db.LookupKey(ctx, security.Hash(raw))
	if err != nil || !lookedUp.Revoked {
		t.Fatal("revocation did not propagate", err)
	}
}
