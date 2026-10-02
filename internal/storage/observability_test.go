package storage

import (
	"context"
	"encoding/json"
	"gateforge/internal/analytics"
	"gateforge/internal/config"
	"gateforge/internal/testutil"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedMetricsRetryRestartAndConcurrentSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	url := testutil.DatabaseURL(t)
	db, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sample := func(n uint64) analytics.Snapshot {
		return analytics.Snapshot{StartedAt: time.Now().Add(-time.Hour), Routes: []analytics.Row{{Route: "/users", Requests: n, Bytes: n * 100, DurationSeconds: float64(n) * .01, Buckets: make([]uint64, 13)}}}
	}
	if err = db.SaveMetrics(ctx, "source-one", sample(10)); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveMetrics(ctx, "source-one", sample(10)); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveMetrics(ctx, "source-one", sample(5)); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveMetrics(ctx, "source-one", sample(12)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"source-two", "source-three"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := db.SaveMetrics(ctx, id, sample(7)); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	restarted, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	result, err := restarted.ReadMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Routes) != 1 || result.Routes[0].Requests != 26 || result.Routes[0].Bytes != 2600 || result.Scope != "shared" || result.PersistedAt.IsZero() {
		t.Fatalf("lost or duplicated shared counters: %+v", result)
	}
}

func TestTransactionalAppendOnlyAudit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ctx = WithActor(ctx, "administrator-test-id")
	db, err := Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	routes := []config.Route{{Prefix: "/users", Upstream: "http://localhost:9001"}}
	if err = db.Seed(ctx, routes); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Save(ctx, 1, routes); err != nil {
		t.Fatal(err)
	}
	key, secret, err := db.CreateKey(ctx, "private display name", []string{"/users"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RevokeKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.RevokeKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListAudit(ctx, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.NextCursor == 0 {
		t.Fatal("audit pagination missing")
	}
	next, err := db.ListAudit(ctx, page.NextCursor, 2)
	if err != nil || len(next.Events) != 1 {
		t.Fatal("audit pagination repeated/skipped events", err)
	}
	if page.Events[0].Action != "key.revoked" || page.Events[1].Action != "key.created" || next.Events[0].Action != "routes.updated" {
		t.Fatal("wrong audit order")
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private display name") {
		t.Fatal("audit leaked credential or body")
	}
	for _, event := range page.Events {
		if event.Actor != "administrator-test-id" {
			t.Fatal("actor lost")
		}
	}
	for _, sql := range []string{`DELETE FROM gateforge_audit`, `UPDATE gateforge_audit SET actor='forged'`, `TRUNCATE gateforge_audit`} {
		if _, err = db.pool.Exec(ctx, sql); err == nil {
			t.Fatal("audit mutation permitted", sql)
		}
	}
	// Inject an audit failure in the isolated test schema; the route update must roll back.
	if _, err = db.pool.Exec(ctx, `ALTER TABLE gateforge_audit ADD CONSTRAINT reject_test_audit CHECK(actor <> 'denied-actor')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Save(WithActor(ctx, "denied-actor"), 2, routes); err == nil {
		t.Fatal("write survived failed audit")
	}
	snapshot, err := db.Load(ctx)
	if err != nil || snapshot.Revision != 2 {
		t.Fatal("failed audit did not roll back route update", err)
	}
}
