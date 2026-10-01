package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBodyLimitBeforeUpstreamSideEffects(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer backend.Close()
	h, err := NewRoutes([]Route{{Prefix: "/", Upstream: backend.URL, MaxBodyBytes: 8}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.(*runtime).Close()
	for _, chunked := range []bool{false, true} {
		req := httptest.NewRequest("POST", "http://gateway/write", strings.NewReader("123456789"))
		if chunked {
			req.ContentLength = -1
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 413 {
			t.Fatalf("chunked=%v status=%d", chunked, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("oversized body reached upstream")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://gateway/write", strings.NewReader("12345678")))
	if w.Code != 204 || calls.Load() != 1 {
		t.Fatal("body at limit rejected", w.Code)
	}
}
