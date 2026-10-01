package shared

import (
	"fmt"
	"gateforge/internal/config"
	"github.com/alicebob/miniredis/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGlobalLoginQuotaAcrossClientsAndInstances(t *testing.T) {
	r, server := testRedis(t)
	other, err := Open("redis://" + server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	route := config.Route{Prefix: "/admin/auth/token", RateLimit: &config.RateLimit{Requests: 10, WindowSeconds: 60}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	handlers := []http.Handler{r.WrapGlobal(route, next), other.WrapGlobal(route, next)}
	var allowed, denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "https://gateway/admin/auth/token", nil)
			req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i)
			w := httptest.NewRecorder()
			handlers[i%2].ServeHTTP(w, req)
			if w.Code == 204 {
				allowed.Add(1)
			} else if w.Code == 429 && w.Header().Get("Retry-After") != "" {
				denied.Add(1)
			} else {
				t.Errorf("unexpected status %d", w.Code)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 || denied.Load() != 30 {
		t.Fatal("shared login quota bypassed", allowed.Load(), denied.Load())
	}
	server.Close()
	w := httptest.NewRecorder()
	handlers[0].ServeHTTP(w, httptest.NewRequest("POST", "https://gateway/admin/auth/token", nil))
	if w.Code != 503 {
		t.Fatal("Redis outage did not fail closed")
	}
}

func testRedis(t *testing.T) (*Redis, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	r, err := Open("redis://" + s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, s
}
func TestLimitAcrossTwoInstances(t *testing.T) {
	r, s := testRedis(t)
	r2, err := Open("redis://" + s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	route := config.Route{Prefix: "/users", RateLimit: &config.RateLimit{Requests: 10, WindowSeconds: 60}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	handlers := []http.Handler{r.Wrap(route, next), r2.Wrap(route, next)}
	var allowed, denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "http://gateway/users", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			handlers[i%2].ServeHTTP(w, req)
			switch w.Code {
			case 204:
				allowed.Add(1)
			case 429:
				denied.Add(1)
			default:
				t.Errorf("unexpected %d", w.Code)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 || denied.Load() != 40 {
		t.Fatalf("allowed %d denied %d", allowed.Load(), denied.Load())
	}
	s.FastForward(time.Minute)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://gateway/users", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	handlers[0].ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatal("window did not expire")
	}
	s.Close()
	w = httptest.NewRecorder()
	handlers[0].ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("limiter must fail closed")
	}
}
func TestCacheRulesAndSharedEntries(t *testing.T) {
	for _, tc := range []struct {
		name              string
		request, response http.Header
		body              string
		hit               bool
	}{
		{"public", nil, http.Header{"Cache-Control": {"public, max-age=60"}}, "hello", true},
		{"shared max age", nil, http.Header{"Cache-Control": {"public, max-age=0, s-maxage=60"}}, "hello", true},
		{"negative age", nil, http.Header{"Cache-Control": {"public, max-age=60"}, "Age": {"-1"}}, "hello", false},
		{"expired age", nil, http.Header{"Cache-Control": {"public, max-age=60"}, "Age": {"60"}}, "hello", false},
		{"private", nil, http.Header{"Cache-Control": {"private, max-age=60"}}, "hello", false},
		{"cookie", nil, http.Header{"Cache-Control": {"public, max-age=60"}, "Set-Cookie": {"session=secret"}}, "hello", false},
		{"unlisted vary", nil, http.Header{"Cache-Control": {"public, max-age=60"}, "Vary": {"X-Customer"}}, "hello", false},
		{"no store", nil, http.Header{"Cache-Control": {"public, no-store, max-age=60"}}, "hello", false},
		{"credential", http.Header{"Authorization": {"Bearer secret"}}, http.Header{"Cache-Control": {"public, max-age=60"}}, "hello", false},
		{"oversize", nil, http.Header{"Cache-Control": {"public, max-age=60"}}, "longer than ten bytes", false},
		{"conditional", http.Header{"If-None-Match": {"tag"}}, http.Header{"Cache-Control": {"public, max-age=60"}}, "hello", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := testRedis(t)
			r2, err := Open("redis://" + s.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer r2.Close()
			calls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				for k, v := range tc.response {
					w.Header()[k] = v
				}
				fmt.Fprint(w, tc.body)
			})
			route := config.Route{Prefix: "/catalog", Cache: &config.Cache{TTLSeconds: 30, MaxBodyBytes: 10}}
			for _, client := range []*Redis{r, r2} {
				req := httptest.NewRequest("GET", "http://gateway/catalog?page=1", nil)
				if tc.request != nil {
					req.Header = tc.request.Clone()
				}
				w := httptest.NewRecorder()
				client.Wrap(route, next).ServeHTTP(w, req)
				if w.Body.String() != tc.body {
					t.Error("body changed")
				}
			}
			want := 2
			if tc.hit {
				want = 1
			}
			if calls != want {
				t.Fatalf("backend calls=%d want %d", calls, want)
			}
		})
	}
}

func TestCachePreservesAgeAndAllVaryValues(t *testing.T) {
	r, _ := testRedis(t)
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Age", "40")
		w.Header().Set("Vary", "Accept")
		fmt.Fprint(w, req.Header.Values("Accept"))
	})
	h := r.Wrap(config.Route{Prefix: "/", Cache: &config.Cache{TTLSeconds: 10, MaxBodyBytes: 100}}, next)
	for _, variant := range []string{"text/plain", "application/json"} {
		for attempt := 0; attempt < 2; attempt++ {
			req := httptest.NewRequest("GET", "http://gateway/", nil)
			req.Header["Accept"] = []string{"*/*", variant}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if attempt == 1 && (w.Header().Get("X-GateForge-Cache") != "HIT" || w.Header().Get("Age") != "40") {
				t.Fatalf("expected cache hit preserving upstream age: %v", w.Header())
			}
		}
	}
	if calls != 2 {
		t.Fatalf("distinct Vary values require separate entries; calls=%d", calls)
	}
}
func TestCacheSeparatesQueriesAndAccept(t *testing.T) {
	r, _ := testRedis(t)
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Accept")
		fmt.Fprint(w, r.URL.RawQuery+r.Header.Get("Accept"))
	})
	h := r.Wrap(config.Route{Prefix: "/", Cache: &config.Cache{TTLSeconds: 10, MaxBodyBytes: 100}}, next)
	for _, q := range []string{"?q=1", "?q=2"} {
		for _, accept := range []string{"text/plain", "application/json"} {
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest("GET", "http://gateway/"+q, nil)
				req.Header.Set("Accept", accept)
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		}
	}
	if calls != 4 {
		t.Fatalf("got %d backend calls want 4", calls)
	}
}
