package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gateforge/internal/config"
	"gateforge/internal/explain"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/shared"
	"gateforge/internal/storage"
	"github.com/alicebob/miniredis/v2"
)

func requestApp(t *testing.T, routes []config.Route, options gateway.Options) *App {
	t.Helper()
	a, err := New(storage.Snapshot{Revision: 12, Routes: routes}, nil, options, nil, strings.Repeat("a", 40), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}
func eventIs(row explain.Record, stage, outcome string) bool {
	for _, e := range row.Events {
		if e.Stage == stage && e.Outcome == outcome {
			return true
		}
	}
	return false
}
func TestRequestExplanationRetryAndPrivacy(t *testing.T) {
	var calls atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set(explain.Header, "spoofed")
		w.WriteHeader(204)
	}))
	defer good.Close()
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			calls.Store(0)
			a := requestApp(t, []config.Route{{Prefix: "/", Upstream: good.URL}, {Prefix: "/orders", Upstreams: []string{bad.URL, good.URL}, Retries: 1}}, gateway.Options{})
			r := httptest.NewRequest(method, "https://gateway/orders/customer-private?token=query-secret", nil)
			r.Header.Set("Authorization", "Bearer credential-secret")
			r.Header.Set("Cookie", "session=cookie-secret")
			r.Header.Set(explain.Header, "client-spoof")
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			row := a.Requests.Snapshot()[0]
			if row.Route != "/orders" || row.Revision != 12 || !row.Complete || row.ID != w.Header().Get(explain.Header) {
				t.Fatalf("bad record %+v", row)
			}
			if method == "GET" {
				if row.Status != 204 || calls.Load() != 2 || !eventIs(row, "retry", "retrying") {
					t.Fatal(row)
				}
			} else {
				if row.Status != 503 || calls.Load() != 1 || eventIs(row, "retry", "retrying") || !eventIs(row, "retry_policy", "disabled") {
					t.Fatal(row)
				}
			}
			data, _ := json.Marshal(row)
			for _, secret := range []string{"customer-private", "query-secret", "credential-secret", "cookie-secret", "spoofed", "client-spoof"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("retained private request data")
				}
			}
		})
	}
}

type explainKeys struct{}

func (explainKeys) LookupKey(_ context.Context, hash string) (security.Key, error) {
	if hash == security.Hash("storage-down") {
		return security.Key{}, errors.New("private storage error")
	}
	if hash != security.Hash("valid") && hash != security.Hash("wrong-route") {
		return security.Key{}, security.ErrInvalidKey
	}
	prefix := "/orders"
	if hash == security.Hash("wrong-route") {
		prefix = "/other"
	}
	return security.Key{ID: "private-key-id", Prefixes: []string{prefix}, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestRequestExplanationAuthAndEarlyExit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer upstream.Close()
	a := requestApp(t, []config.Route{{Prefix: "/orders", Upstream: upstream.URL, Auth: "api_key"}}, gateway.Options{Auth: &security.Authenticator{Keys: explainKeys{}}})
	for _, tc := range []struct {
		key     string
		status  int
		outcome string
	}{{"", 401, "rejected"}, {"invalid", 401, "rejected"}, {"wrong-route", 403, "rejected"}, {"storage-down", 503, "rejected"}, {"valid", 204, "accepted"}} {
		r := httptest.NewRequest("GET", "https://gateway/orders/7", nil)
		r.Header.Set("X-API-Key", tc.key)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		row := a.Requests.Snapshot()[0]
		if row.Status != tc.status || !eventIs(row, "authentication", tc.outcome) || (tc.status != 204 && eventIs(row, "backend", "selected")) {
			t.Fatal(row)
		}
	}
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://gateway/orders-other", nil))
	row := a.Requests.Snapshot()[0]
	if row.Status != 404 || row.Route != "" || !eventIs(row, "routing", "rejected") {
		t.Fatal(row)
	}
}
func TestRequestExplanationCacheAndQuota(t *testing.T) {
	server := miniredis.RunT(t)
	redis, err := shared.Open("redis://" + server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	a := requestApp(t, []config.Route{{Prefix: "/catalog", Upstream: upstream.URL, RateLimit: &config.RateLimit{Requests: 3, WindowSeconds: 60}, Cache: &config.Cache{TTLSeconds: 20, MaxBodyBytes: 100}}}, gateway.Options{Redis: redis})
	for i, outcome := range []string{"miss", "hit", "skipped", "rejected"} {
		r := httptest.NewRequest("GET", "https://gateway/catalog/items", nil)
		if i == 2 {
			r.Header.Set("Cookie", "private=value")
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		row := a.Requests.Snapshot()[0]
		stage := "cache"
		if i == 3 {
			stage = "rate_limit"
		}
		if !eventIs(row, stage, outcome) {
			t.Fatal(row)
		}
		if i == 1 && (calls.Load() != 1 || eventIs(row, "backend", "selected")) {
			t.Fatal("cache hit contacted backend")
		}
		if i == 3 && (row.Status != 429 || eventIs(row, "cache", "hit") || eventIs(row, "backend", "selected")) {
			t.Fatal(row)
		}
	}
	server.Close()
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://gateway/catalog/items", nil))
	row := a.Requests.Snapshot()[0]
	if row.Status != 503 || !eventIs(row, "rate_limit", "rejected") {
		t.Fatal(row)
	}
}
func TestRequestsEndpointRequiresAdministratorAndExcludesOperations(t *testing.T) {
	a := requestApp(t, []config.Route{{Prefix: "/", Upstream: "http://127.0.0.1:1"}}, gateway.Options{})
	for _, tc := range []struct {
		scheme, token, method string
		status                int
	}{{"http", strings.Repeat("a", 40), "GET", 426}, {"https", "", "GET", 401}, {"https", "wrong", "GET", 401}, {"https", strings.Repeat("a", 40), "GET", 200}, {"https", strings.Repeat("a", 40), "POST", 405}} {
		r := httptest.NewRequest(tc.method, tc.scheme+"://gateway/admin/api/requests", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://gateway/healthz", nil))
	if len(a.Requests.Snapshot()) != 0 {
		t.Fatal("operational requests retained")
	}
}

func TestExplanationTimeoutBodyLimitAndRequestID(t *testing.T) {
	var upstreamID atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		upstreamID.Store(r.Header.Get(explain.Header))
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	a := requestApp(t, []config.Route{{Prefix: "/", Upstream: upstream.URL, MaxBodyBytes: 4}, {Prefix: "/slow", Upstream: upstream.URL, TimeoutMS: 20}}, gateway.Options{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://gateway/item", nil)
	r.Header.Set(explain.Header, "spoofed-client-id")
	a.ServeHTTP(w, r)
	if upstreamID.Load() == "" || upstreamID.Load() != w.Header().Get(explain.Header) || upstreamID.Load() == "spoofed-client-id" {
		t.Fatal("request ID was not propagated")
	}
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "https://gateway/item", strings.NewReader("large body")))
	row := a.Requests.Snapshot()[0]
	if row.Status != 413 || !eventIs(row, "body", "rejected") || eventIs(row, "backend", "selected") {
		t.Fatal(row)
	}
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://gateway/slow", nil))
	row = a.Requests.Snapshot()[0]
	if row.Status != 504 || !eventIs(row, "proxy", "timeout") {
		t.Fatal(row)
	}
}
