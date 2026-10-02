package storage

import (
	"context"
	"gateforge/internal/security"
	"gateforge/internal/testutil"
	"testing"
	"time"
)

func TestTenantAssignmentPersistsAndBindsIdempotency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Migrate(ctx) != nil {
		t.Fatal("migrate")
	}
	expiry := time.Now().Add(time.Hour)
	key, raw, err := db.CreateTenantKey(ctx, "tenant-test", []string{"/users"}, expiry, "customer-a", "tenant-fixture-request")
	if err != nil {
		t.Fatal(err)
	}
	found, err := db.LookupKey(ctx, security.Hash(raw))
	if err != nil || found.TenantID != "customer-a" || found.ID != key.ID {
		t.Fatal("tenant was not persisted")
	}
	if _, _, err = db.CreateTenantKey(ctx, "tenant-test", []string{"/users"}, expiry, "customer-b", "tenant-fixture-request"); err != ErrRequestConflict {
		t.Fatal("idempotency did not bind tenant", err)
	}
	page, err := db.ListKeys(ctx, KeyQuery{Limit: 50})
	if err != nil || len(page.Keys) != 1 || page.Keys[0].TenantID != "customer-a" {
		t.Fatal("tenant missing from key listing")
	}
}
