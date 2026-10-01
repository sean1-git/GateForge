package adminauth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenExchangeAndRotation(t *testing.T) {
	secret, _ := Random()
	db := &memorySessions{data: map[string]Session{}}
	sessions := &Sessions{Store: db, Origin: "https://gateway.example"}
	var logs bytes.Buffer
	login, err := NewTokenLogin(sessions, secret, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, origin, media, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://gateway.example"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		login.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(map[string]string{"token": secret})
	for _, tc := range []struct {
		name, method, path, origin, media, body string
		status                                  int
	}{
		{"wrong token", "POST", "/admin/auth/token", sessions.Origin, "application/json", `{"token":"wrong"}`, 401},
		{"service key", "POST", "/admin/auth/token", sessions.Origin, "application/json", `{"token":"gf_service_credential"}`, 401},
		{"cross origin", "POST", "/admin/auth/token", "https://evil.example", "application/json", string(body), 403},
		{"missing origin", "POST", "/admin/auth/token", "", "application/json", string(body), 403},
		{"form", "POST", "/admin/auth/token", sessions.Origin, "application/x-www-form-urlencoded", string(body), 415},
		{"get", "GET", "/admin/auth/token", sessions.Origin, "application/json", string(body), 405},
		{"query", "POST", "/admin/auth/token?token=avoid-query", sessions.Origin, "application/json", string(body), 400},
		{"extra object", "POST", "/admin/auth/token", sessions.Origin, "application/json", string(body) + `{}`, 400},
		{"oversize", "POST", "/admin/auth/token", sessions.Origin, "application/json", `{"token":"` + strings.Repeat("x", 9000) + `"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(tc.method, tc.path, tc.origin, tc.media, tc.body)
			if w.Code != tc.status || len(w.Result().Cookies()) != 0 {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
	if len(db.data) != 0 {
		t.Fatal("invalid login persisted a session")
	}
	db.unavailable = true
	if w := call("POST", "/admin/auth/token", sessions.Origin, "application/json", string(body)); w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatal("storage failure issued session")
	}
	db.unavailable = false
	w := call("POST", "/admin/auth/token", sessions.Origin, "application/json", string(body))
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.MaxAge != 28800 {
		t.Fatal("unsafe cookie")
	}
	r := httptest.NewRequest("GET", "https://gateway.example/admin/auth/session", nil)
	r.AddCookie(cookie)
	current, err := sessions.Authenticate(r.Context(), r)
	if err != nil || current.Identity.Name != "Administrator" || current.Identity.Email != "" {
		t.Fatal("session did not authenticate safely", err)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), cookie.Value) {
		t.Fatal("credential logged")
	}
	if _, ok := db.data[cookie.Value]; ok {
		t.Fatal("raw session persisted")
	}
	rotated, _ := Random()
	if _, err = NewTokenLogin(sessions, rotated, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Authenticate(context.Background(), r); err != ErrUnauthorized {
		t.Fatal("rotation retained old session")
	}
}
