package storage

import (
	"context"
	"errors"
	"gateforge/internal/adminauth"
	"gateforge/internal/testutil"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdministratorSessionPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		raw, _ := adminauth.Random()
		if _, err = db.CreateAdminSession(ctx, "https://issuer.example", "subject", "owner@example.com", adminauth.Hash(raw), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.pool.QueryRow(ctx, `SELECT count(*) FROM gateforge_admin_sessions`).Scan(&count); err != nil || count != 8 {
		t.Fatalf("bounded sessions: %d, %v", count, err)
	}
	if _, err = db.CreateAdminSession(ctx, "https://issuer.example", "different-subject", "owner@example.com", "other-hash", time.Now().Add(time.Hour)); !errors.Is(err, adminauth.ErrUnauthorized) {
		t.Fatalf("email takeover allowed: %v", err)
	}
	raw, _ := adminauth.Random()
	hash := adminauth.Hash(raw)
	id, err := db.CreateAdminSession(ctx, "https://issuer.example", "subject", "owner@example.com", hash, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.AdminSession(ctx, hash)
	if err != nil || session.Identity != id {
		t.Fatal(session, err)
	}
	if _, err = db.AdminSession(ctx, raw); !errors.Is(err, adminauth.ErrUnauthorized) {
		t.Fatal("raw secret usable as database hash")
	}
	if err = db.DeleteAdminSession(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AdminSession(ctx, hash); !errors.Is(err, adminauth.ErrUnauthorized) {
		t.Fatal("revoked session accepted")
	}
}

func TestAdministratorLoginConsumedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f := adminauth.LoginFlow{StateHash: "state", BrowserHash: "browser", Nonce: "nonce", Verifier: "verifier", ExpiresAt: time.Now().Add(time.Minute)}
	if err = db.SaveAdminLogin(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ConsumeAdminLogin(ctx, "state", "wrong"); !errors.Is(err, adminauth.ErrUnauthorized) {
		t.Fatal("browser binding bypassed")
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.ConsumeAdminLogin(ctx, "state", "browser")
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, adminauth.ErrUnauthorized) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("login replay succeeded", wins.Load())
	}
	f.ExpiresAt = time.Now().Add(-time.Minute)
	if err = db.SaveAdminLogin(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ConsumeAdminLogin(ctx, "state", "browser"); !errors.Is(err, adminauth.ErrUnauthorized) {
		t.Fatal("expired login accepted")
	}
}
