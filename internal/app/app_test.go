package app

import (
	"context"
	"errors"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeStore struct {
	snapshot    storage.Snapshot
	unavailable bool
}

func (s *fakeStore) Ping(context.Context) error {
	if s.unavailable {
		return errors.New("down")
	}
	return nil
}
func (s *fakeStore) Load(context.Context) (storage.Snapshot, error) {
	if s.unavailable {
		return storage.Snapshot{}, errors.New("down")
	}
	return s.snapshot, nil
}
func (s *fakeStore) Save(_ context.Context, revision int64, routes []config.Route) (storage.Snapshot, error) {
	if revision != s.snapshot.Revision {
		return storage.Snapshot{}, storage.ErrConflict
	}
	s.snapshot = storage.Snapshot{Revision: revision + 1, Routes: routes}
	return s.snapshot, nil
}
func (s *fakeStore) CreateKey(context.Context, string, []string, time.Time, ...string) (security.Key, string, error) {
	return security.Key{}, "", nil
}
func (s *fakeStore) ListKeys(context.Context, storage.KeyQuery) (storage.KeyPage, error) {
	return storage.KeyPage{Keys: []security.Key{}}, nil
}
func (s *fakeStore) RevokeKey(context.Context, string) error { return nil }
func TestAdminAccessAndAtomicSave(t *testing.T) {
	token := strings.Repeat("a", 40)
	s := &fakeStore{snapshot: storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/users", Upstream: "http://localhost:9001"}}}}
	a, err := New(s.snapshot, s, gateway.Options{}, nil, token, false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	call := func(scheme, method, path, credential, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, scheme+"://gateway"+path, strings.NewReader(body))
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		scheme, credential string
		status             int
	}{{"http", token, 426}, {"https", "", 401}, {"https", "wrong", 401}, {"https", token, 200}} {
		if w := call(tc.scheme, "GET", "/admin/api/config", tc.credential, ""); w.Code != tc.status {
			t.Fatalf("got %d want %d", w.Code, tc.status)
		}
	}
	body := `{"revision":1,"routes":[{"prefix":"/orders","upstream":"http://localhost:9002"}]}`
	if w := call("https", "PUT", "/admin/api/config", token, body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := call("https", "PUT", "/admin/api/config", token, body); w.Code != 409 {
		t.Fatalf("stale save accepted: %d", w.Code)
	}
	invalid := `{"revision":2,"routes":[{"prefix":"/orders","upstream":"ftp://localhost"}]}`
	if w := call("https", "PUT", "/admin/api/config", token, invalid); w.Code != 400 {
		t.Fatalf("bad config accepted: %d", w.Code)
	}
	if s.snapshot.Revision != 2 || a.current.Load().snapshot.Revision != 2 {
		t.Fatal("failed save changed configuration")
	}
	if w := call("https", "GET", "/metrics", token, ""); w.Code != 200 {
		t.Fatalf("metrics: %d", w.Code)
	}
	s.unavailable = true
	if err := a.Reload(context.Background()); err == nil {
		t.Fatal("expected storage error")
	}
	if a.current.Load().snapshot.Revision != 2 {
		t.Fatal("last valid snapshot lost")
	}
	if w := call("https", "GET", "/readyz", "", ""); w.Code != 503 {
		t.Fatal("readiness did not fail")
	}
	if w := call("https", "GET", "/healthz", "", ""); w.Code != http.StatusOK {
		t.Fatal("liveness depends on storage")
	}
}
