package gateway

import (
	"context"
	"gateforge/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoundRobinAndSafeRetries(t *testing.T) {
	var first, second atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { first.Add(1); w.WriteHeader(503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { second.Add(1); w.WriteHeader(204) }))
	defer good.Close()
	for _, tc := range []struct {
		method, body  string
		status, calls int
	}{{"GET", "", 204, 1}, {"HEAD", "", 204, 1}, {"POST", "payload", 503, 0}, {"PUT", "payload", 503, 0}, {"GET", "payload", 503, 0}} {
		t.Run(tc.method+tc.body, func(t *testing.T) {
			first.Store(0)
			second.Store(0)
			h, err := NewRoutes([]Route{{Prefix: "/", Upstreams: []string{bad.URL, good.URL}, Retries: 1}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer h.(io.Closer).Close()
			r := httptest.NewRequest(tc.method, "http://gateway/item", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || int(second.Load()) != tc.calls {
				t.Fatalf("status=%d second calls=%d", w.Code, second.Load())
			}
		})
	}
	first.Store(0)
	second.Store(0)
	h, err := NewRoutes([]Route{{Prefix: "/", Upstreams: []string{bad.URL, good.URL}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.(io.Closer).Close()
	for i := 0; i < 10; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://gateway/item", nil))
	}
	if first.Load() != 5 || second.Load() != 5 {
		t.Fatal("round robin was not balanced")
	}
}
func TestHealthChecksRemoveAndRecoverBackend(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer backend.Close()
	p, err := newPool(Route{Prefix: "/", Upstream: backend.URL, Health: &config.Health{Path: "/healthz", IntervalSeconds: 1, TimeoutMS: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	for _, want := range []bool{true, false, true} {
		healthy.Store(want)
		p.probe(context.Background(), p.backends[0])
		if got := p.backends[0].healthy.Load(); got != want {
			t.Fatalf("healthy=%v want=%v", got, want)
		}
		req := httptest.NewRequest("GET", "http://gateway/item", nil)
		req.RequestURI = ""
		response, err := p.RoundTrip(req)
		if !want && err != errNoHealthy {
			t.Fatalf("unhealthy backend was used: %v", err)
		}
		if want && err != nil {
			t.Fatal(err)
		}
		if response != nil {
			response.Body.Close()
		}
	}
}
func TestUpstreamDeadline(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
			w.WriteHeader(200)
		}
	}))
	defer backend.Close()
	h, err := NewRoutes([]Route{{Prefix: "/", Upstream: backend.URL, TimeoutMS: 30}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.(io.Closer).Close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://gateway/item", nil))
	if w.Code != 504 {
		t.Fatalf("got %d want 504", w.Code)
	}
}
