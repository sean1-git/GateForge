package security

import (
	"context"
	"gateforge/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type tenantKeys map[string]Key

func (k tenantKeys) LookupKey(_ context.Context, hash string) (Key, error) {
	v, ok := k[hash]
	if !ok {
		return v, ErrInvalidKey
	}
	return v, nil
}
func TestTenantSlotsAreSharedByKeysAndReleased(t *testing.T) {
	limiter := NewConcurrency(2, 1)
	keys := tenantKeys{}
	for _, key := range []struct{ raw, tenant string }{{"first", "customer-a"}, {"rotated", "customer-a"}, {"other", "customer-b"}} {
		keys[Hash(key.raw)] = Key{ID: key.raw, TenantID: key.tenant, Prefixes: []string{"/items"}, ExpiresAt: time.Now().Add(time.Hour)}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	auth := &Authenticator{Keys: keys, TLSOffloaded: true}
	handler := auth.Wrap(config.Route{Prefix: "/items", Auth: "api_key"}, limiter.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Hold") == "yes" {
			close(entered)
			<-release
		}
		w.WriteHeader(200)
	})))
	call := func(key, hold string) int {
		r := httptest.NewRequest("GET", "http://gateway/items", nil)
		r.Header.Set("X-API-Key", key)
		r.Header.Set("X-Tenant-ID", "spoofed-"+key)
		r.Header.Set("Hold", hold)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	done := make(chan int)
	go func() { done <- call("first", "yes") }()
	<-entered
	if call("rotated", "") != 429 {
		t.Fatal("same customer bypassed its allowance using another key")
	}
	if call("other", "") != 200 {
		t.Fatal("busy customer starved another tenant")
	}
	close(release)
	if <-done != 200 || limiter.Snapshot().Active != 0 {
		t.Fatal("slot leak")
	}
	if call("rotated", "") != 200 {
		t.Fatal("slot not reusable")
	}
}
func TestGlobalBoundAndPanicRelease(t *testing.T) {
	limiter := NewConcurrency(1, 1)
	hold := make(chan struct{})
	entered := make(chan struct{})
	done := make(chan struct{})
	handler := limiter.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-hold }))
	req := func(tenant string) *http.Request {
		return httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), tenantKey{}, tenant))
	}
	go func() { handler.ServeHTTP(httptest.NewRecorder(), req("a")); close(done) }()
	<-entered
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req("b"))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	close(hold)
	<-done
	func() {
		defer func() { recover() }()
		limiter.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("test") })).ServeHTTP(httptest.NewRecorder(), req("a"))
	}()
	if limiter.Snapshot().Active != 0 || len(limiter.active) != 0 {
		t.Fatal("panic leaked admission state")
	}
}
