package security

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngressBoundsConcurrentAttemptsAndIgnoresForwarding(t *testing.T) {
	l := NewIngress(10, 3)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/users", nil)
			r.RemoteAddr = "192.0.2.1:80"
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i+2))
			w := httptest.NewRecorder()
			if l.Allow(w, r) {
				allowed.Add(1)
			} else if w.Code != 429 || w.Header().Get("Retry-After") == "" {
				t.Error("missing rate-limit response")
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatal(allowed.Load())
	}
	for i := 0; i < 4; i++ {
		r := httptest.NewRequest("GET", "/admin/api/requests", nil)
		r.RemoteAddr = "192.0.2.1:80"
		if l.Allow(httptest.NewRecorder(), r) != (i < 3) {
			t.Fatal("admin limit not independent")
		}
	}
	if !l.Allow(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil)) {
		t.Fatal("health probe blocked")
	}
	l.window = time.Now().Add(-time.Minute)
	if !l.Allow(httptest.NewRecorder(), httptest.NewRequest("GET", "/users", nil)) {
		t.Fatal("window did not reset")
	}
}
func TestIngressMemoryBound(t *testing.T) {
	l := NewIngress(10, 10)
	for i := 0; i < 5000; i++ {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = fmt.Sprintf("peer-%d", i)
		l.Allow(httptest.NewRecorder(), r)
	}
	if len(l.counts) != 4096 {
		t.Fatal("peer map is unbounded")
	}
}
