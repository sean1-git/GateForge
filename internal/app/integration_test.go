package app

import (
	"context"
	"encoding/json"
	"fmt"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/shared"
	"gateforge/internal/storage"
	"gateforge/internal/testutil"
	"github.com/alicebob/miniredis/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPSGatewaysShareKeysLimitsAndCache(t *testing.T) {
	dbURL := testutil.DatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	redisURL := os.Getenv("GATEFORGE_TEST_REDIS_URL")
	if redisURL == "" {
		mini := miniredis.RunT(t)
		redisURL = "redis://" + mini.Addr()
	}
	redisStore, err := shared.Open(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer redisStore.Close()
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials reached upstream")
		}
		w.Header().Set("Cache-Control", "public, max-age=30")
		fmt.Fprint(w, "response")
	}))
	defer backend.Close()
	// Isolate shared Redis keys between test executions without deleting any existing data.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	users := "/users-" + suffix
	catalog := "/catalog-" + suffix
	routes := []config.Route{{Prefix: users, Upstream: backend.URL, Auth: "api_key", RateLimit: &config.RateLimit{Requests: 3, WindowSeconds: 60}}, {Prefix: catalog, Upstream: backend.URL, Auth: "public", Cache: &config.Cache{TTLSeconds: 20, MaxBodyBytes: 1024}}}
	if err = db.Seed(ctx, routes); err != nil {
		t.Fatal(err)
	}
	original, err := db.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Save(ctx, original.Revision, routes)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		restoreCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		current, err := db.Load(restoreCtx)
		if err == nil {
			_, _ = db.Save(restoreCtx, current.Revision, original.Routes)
		}
	}()
	auth := &security.Authenticator{Keys: db}
	admin := strings.Repeat("t", 40)
	makeServer := func() *httptest.Server {
		a, err := New(snapshot, db, gateway.Options{Auth: auth, Redis: redisStore}, nil, admin, false)
		if err != nil {
			t.Fatal(err)
		}
		s := httptest.NewTLSServer(a)
		t.Cleanup(func() { s.Close(); a.Close() })
		return s
	}
	first, second := makeServer(), makeServer()
	input := fmt.Sprintf(`{"name":"integration","prefixes":[%q],"expires_at":%q}`, users, time.Now().Add(time.Hour).Format(time.RFC3339))
	req, _ := http.NewRequest("POST", first.URL+"/admin/api/keys", strings.NewReader(input))
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err := first.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Key    security.Key `json:"key"`
		Secret string       `json:"secret"`
	}
	err = json.NewDecoder(resp.Body).Decode(&issued)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 201 || issued.Secret == "" {
		t.Fatalf("create key: status=%d err=%v", resp.StatusCode, err)
	}
	for i := 0; i < 4; i++ {
		s := first
		if i%2 == 1 {
			s = second
		}
		req, _ := http.NewRequest("GET", s.URL+users, nil)
		req.Header.Set("X-API-Key", issued.Secret)
		resp, err = s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 200
		if i == 3 {
			want = 429
		}
		if resp.StatusCode != want {
			t.Fatalf("shared quota request %d got %d want %d", i, resp.StatusCode, want)
		}
	}
	before := calls.Load()
	for _, s := range []*httptest.Server{first, second} {
		cacheRequest, _ := http.NewRequest("GET", s.URL+catalog, nil)
		cacheRequest.Host = "gateway.example"
		resp, err = s.Client().Do(cacheRequest)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal("cache request failed")
		}
	}
	if calls.Load() != before+1 {
		t.Fatal("cache was not shared across gateways")
	}
	req, _ = http.NewRequest("DELETE", first.URL+"/admin/api/keys/"+issued.Key.ID, nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	resp, err = first.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal("revocation failed")
	}
	req, _ = http.NewRequest("GET", second.URL+users, nil)
	req.Header.Set("X-API-Key", issued.Secret)
	resp, err = second.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("revocation was not enforced on the other gateway")
	}
}
