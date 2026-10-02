package app

import (
	"context"
	"encoding/json"
	"errors"
	"gateforge/internal/adminauth"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/shared"
	"gateforge/internal/storage"
	"github.com/alicebob/miniredis/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTokenLoginRequiresHTTPS(t *testing.T) {
	a, err := New(storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/", Upstream: "http://localhost:9001"}}}, nil, gateway.Options{}, nil, strings.Repeat("test", 8), false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.AdminLogin = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("insecure login reached handler") })
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("POST", "http://gateway/admin/auth/token", strings.NewReader(`{}`)))
	if w.Code != 426 {
		t.Fatal(w.Code)
	}
}

func TestCachedBackendStillSandboxed(t *testing.T) {
	server := miniredis.RunT(t)
	redis, err := shared.Open("redis://" + server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<script>alert('backend')</script>"))
	}))
	defer backend.Close()
	a, err := New(storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/service", Upstream: backend.URL, Cache: &config.Cache{TTLSeconds: 30, MaxBodyBytes: 1024}}}}, nil, gateway.Options{Redis: redis}, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, cache := range []string{"MISS", "HIT"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", "https://gateway/service", nil))
		if w.Code != 200 || w.Header().Get("X-GateForge-Cache") != cache || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox;") {
			t.Fatalf("unsafe %s response %d", cache, w.Code)
		}
	}
}

type adminSessionStore struct {
	value         adminauth.Session
	revoked, down bool
}

func (s *adminSessionStore) CreateAdminSession(context.Context, string, string, string, string, time.Time) (adminauth.Identity, error) {
	return s.value.Identity, nil
}
func (s *adminSessionStore) AdminSession(context.Context, string) (adminauth.Session, error) {
	if s.down {
		return adminauth.Session{}, errors.New("down")
	}
	if s.revoked {
		return adminauth.Session{}, adminauth.ErrUnauthorized
	}
	return s.value, nil
}
func (s *adminSessionStore) DeleteAdminSession(context.Context, string) error {
	if s.down {
		return errors.New("down")
	}
	s.revoked = true
	return nil
}

func TestSessionOnlyAdministratorAPI(t *testing.T) {
	legacy := strings.Repeat("legacy-test-", 4)
	store := &fakeStore{snapshot: storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/users", Upstream: "http://localhost:9001"}}}}
	a, err := New(store.snapshot, store, gateway.Options{}, nil, legacy, false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	db := &adminSessionStore{value: adminauth.Session{Identity: adminauth.Identity{ID: "individual", Email: "owner@example.com"}, ExpiresAt: time.Now().Add(time.Hour)}}
	a.AdminSessions = &adminauth.Sessions{Store: db, Origin: "https://gateway", AllowedEmails: map[string]bool{"owner@example.com": true}}
	raw, _ := adminauth.Random()
	call := func(method, path string, cookie bool, origin, csrf, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://gateway"+path, nil)
		if cookie {
			r.AddCookie(&http.Cookie{Name: adminauth.SessionCookie, Value: raw})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if csrf != "" {
			r.Header.Set("X-GateForge-CSRF", csrf)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/admin/api/config", false, "", "", legacy); w.Code != 401 {
		t.Fatal("shared token still grants admin access", w.Code)
	}
	if w := call("GET", "/admin/api/config", true, "", "", "service-jwt"); w.Code != 401 {
		t.Fatal("ambiguous credentials accepted", w.Code)
	}
	if w := call("GET", "/admin/api/requests", false, "", "", legacy); w.Code != 401 {
		t.Fatal("request explanations bypassed session authentication", w.Code)
	}
	if w := call("GET", "/admin/api/requests", true, "", "", ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session cannot read private request explanations", w.Code)
	}
	w := call("GET", "/admin/auth/session", true, "", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var session adminauth.Session
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), raw) || session.CSRFToken == "" {
		t.Fatal("session secret leaked or CSRF missing")
	}
	for _, tc := range []struct{ origin, csrf string }{{"", ""}, {"https://evil.example", session.CSRFToken}, {"https://gateway", "wrong"}} {
		if w := call("DELETE", "/admin/api/keys/test", true, tc.origin, tc.csrf, ""); w.Code != 403 {
			t.Fatal("forged write accepted", w.Code)
		}
	}
	if w := call("DELETE", "/admin/api/keys/test", true, "https://gateway", session.CSRFToken, ""); w.Code != 204 {
		t.Fatal("valid write rejected", w.Code)
	}
	if w := call("GET", "/admin/auth/logout", true, "", "", ""); w.Code != 405 || db.revoked {
		t.Fatal("GET logged user out")
	}
	db.down = true
	if w := call("GET", "/admin/api/config", true, "", "", ""); w.Code != 503 {
		t.Fatal("outage bypassed session validation", w.Code)
	}
	db.down = false
	if w := call("POST", "/admin/auth/logout", true, "https://gateway", session.CSRFToken, ""); w.Code != 204 || !db.revoked {
		t.Fatal("logout failed", w.Code)
	}
	if w := call("GET", "/admin/api/config", true, "", "", ""); w.Code != 401 {
		t.Fatal("revoked session reused", w.Code)
	}
}

func TestAdministratorCookiesNeverReachBackends(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, cookie := range r.Cookies() {
			if strings.HasPrefix(cookie.Name, "__Host-gateforge_") {
				t.Error("administrator cookie reached backend")
			}
		}
		if r.Header.Get("X-GateForge-CSRF") != "" {
			t.Error("administrator CSRF token reached backend")
		}
		if cookie, err := r.Cookie("application"); err != nil || cookie.Value != "keep-me" {
			t.Error("application cookie removed")
		}
		w.Header().Add("Set-Cookie", "__Host-gateforge_admin=overwrite; Path=/; Secure")
		w.Header().Add("Set-Cookie", "__Host-gateforge_login=overwrite; Path=/; Secure")
		w.Header().Add("Set-Cookie", "application=updated; Path=/; Secure")
		w.Header().Set("Content-Security-Policy", "default-src * 'unsafe-inline'")
		w.Header().Set("Clear-Site-Data", "\"cookies\"")
		w.WriteHeader(200)
	}))
	defer backend.Close()
	a, err := New(storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/service", Upstream: backend.URL}}}, nil, gateway.Options{}, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	r := httptest.NewRequest("GET", "https://gateway/service", nil)
	r.Header.Set("Cookie", "__Host-gateforge_admin=private-session; __Host-gateforge_login=private-login; application=keep-me")
	r.Header.Set("X-GateForge-CSRF", "private-csrf")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != "application" {
		t.Fatal("backend could overwrite administrator cookies")
	}
	if !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox;") || w.Header().Get("Clear-Site-Data") != "" {
		t.Fatal("backend can affect administrator origin")
	}
}
