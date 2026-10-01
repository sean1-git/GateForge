package storage

import (
	"context"
	"errors"
	"gateforge/internal/testutil"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyPaginationAndConcurrentDeduplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	var created, duplicate atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := db.CreateKey(ctx, "concurrent", []string{"/users"}, expiry, "same-request-123456")
			if err == nil {
				created.Add(1)
			} else if errors.Is(err, ErrDuplicateRequest) {
				duplicate.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || duplicate.Load() != 19 {
		t.Fatalf("created=%d duplicate=%d", created.Load(), duplicate.Load())
	}
	if _, _, err = db.CreateKey(ctx, "different", []string{"/users"}, expiry, "same-request-123456"); !errors.Is(err, ErrRequestConflict) {
		t.Fatal("idempotency payload mismatch accepted", err)
	}
	for i := 0; i < 4; i++ {
		if _, _, err = db.CreateKey(ctx, "page", []string{"/users"}, expiry); err != nil {
			t.Fatal(err)
		}
	}
	// Equal timestamps exercise the ID tie-breaker, rather than relying on timing.
	if _, err = db.pool.Exec(ctx, `UPDATE gateforge_keys SET created_at=$1`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		query, err := ParseKeyQuery(2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		page, err := db.ListKeys(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Keys) > 2 {
			t.Fatal("unbounded page")
		}
		for _, k := range page.Keys {
			if seen[k.ID] {
				t.Fatal("duplicate on page")
			}
			seen[k.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("pagination skipped keys: %d", len(seen))
	}
}
func TestCursorValidation(t *testing.T) {
	for _, cursor := range []string{"!", "e30", EncodeCursor(time.Now(), "not-a-valid-key")} {
		if _, err := ParseKeyQuery(50, cursor); err == nil {
			t.Fatal("bad cursor accepted")
		}
	}
	if _, err := ParseKeyQuery(201, ""); err == nil {
		t.Fatal("unbounded page accepted")
	}
}
