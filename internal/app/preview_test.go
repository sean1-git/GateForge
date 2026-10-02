package app

import (
	"encoding/json"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewDoesNotSendTrafficOrWriteConfiguration(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer upstream.Close()
	route := config.Route{Prefix: "/old", Upstream: upstream.URL}
	store := &fakeStore{snapshot: storage.Snapshot{Revision: 1, Routes: []config.Route{route}}}
	app, err := New(store.snapshot, store, gateway.Options{}, nil, strings.Repeat("a", 40), true)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://gateway/old/item", nil))
	draft := policies(store.snapshot)
	draft.Routes[0].Prefix = "/old/item"
	body, _ := json.Marshal(draft)
	r := httptest.NewRequest("POST", "https://gateway/admin/api/policies/preview", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40))
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"routing_changed":true`) || calls.Load() != 1 || store.snapshot.Revision != 1 {
		t.Fatal("preview changed live state", w.Code, w.Body.String(), calls.Load())
	}
	route.Health = &config.Health{Path: "/healthz", IntervalSeconds: 1, TimeoutMS: 100}
	if gateway.ValidateRoutes([]config.Route{route}, app.Options) != nil {
		t.Fatal("validation failed")
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("preview validation started health probes")
	}
	noAuth := httptest.NewRecorder()
	app.ServeHTTP(noAuth, httptest.NewRequest("POST", "https://gateway/admin/api/lab", strings.NewReader(`{"mode":"failure"}`)))
	if noAuth.Code != 401 || app.Lab.Snapshot().Running {
		t.Fatal("unauthorized lab mutation")
	}
}

func TestPreviewDetectsPoolSwapWithoutPrefixChange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer upstream.Close()
	snapshot := storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/a", Upstream: upstream.URL}, {Prefix: "/b", Upstream: upstream.URL}}}
	a, err := New(snapshot, nil, gateway.Options{}, nil, strings.Repeat("a", 40), true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "https://gateway/a/item", nil))
	draft := policies(snapshot)
	draft.Routes[0].SourcePrefix, draft.Routes[1].SourcePrefix = "/b", "/a"
	body, _ := json.Marshal(draft)
	r := httptest.NewRequest("POST", "https://gateway/admin/api/policies/preview", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"routing_changed":true`) {
		t.Fatal("backend pool swap was not identified", w.Code, w.Body.String())
	}
}
