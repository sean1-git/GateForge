package adminauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type memorySessions struct {
	data        map[string]Session
	unavailable bool
}

func (m *memorySessions) CreateAdminSession(_ context.Context, issuer, subject, email, hash string, expires time.Time) (Identity, error) {
	if m.unavailable {
		return Identity{}, errors.New("database unavailable")
	}
	id := Identity{ID: Hash(issuer + "\x00" + subject), Email: email}
	m.data[hash] = Session{Identity: id, ExpiresAt: expires}
	return id, nil
}
func (m *memorySessions) AdminSession(_ context.Context, hash string) (Session, error) {
	if m.unavailable {
		return Session{}, errors.New("database unavailable")
	}
	s, ok := m.data[hash]
	if !ok {
		return Session{}, ErrUnauthorized
	}
	return s, nil
}
func (m *memorySessions) DeleteAdminSession(_ context.Context, hash string) error {
	if m.unavailable {
		return errors.New("database unavailable")
	}
	delete(m.data, hash)
	return nil
}

func TestSessionLifecycleAndForgery(t *testing.T) {
	store := &memorySessions{data: map[string]Session{}}
	s := Sessions{Store: store, Origin: "https://gateway.example", AllowedEmails: map[string]bool{"owner@example.com": true}}
	w := httptest.NewRecorder()
	if err := s.Issue(context.Background(), w, "https://issuer.example", "subject", "stranger@example.com"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("non-member received a session")
	}
	if len(store.data) != 0 {
		t.Fatal("unauthorized session persisted")
	}
	if err := s.Issue(context.Background(), w, "https://issuer.example", "subject", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie protections missing")
	}
	if _, ok := store.data[cookie.Value]; ok {
		t.Fatal("raw session stored")
	}
	r := httptest.NewRequest("POST", "https://gateway.example/admin/api/keys", nil)
	r.AddCookie(cookie)
	session, err := s.Authenticate(r.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if s.ValidWrite(r, session) {
		t.Fatal("write without CSRF accepted")
	}
	r.Header.Set("Origin", s.Origin)
	r.Header.Set("X-GateForge-CSRF", session.CSRFToken)
	if !s.ValidWrite(r, session) {
		t.Fatal("legitimate write denied")
	}
	r.Header.Set("Origin", "https://attacker.example")
	if s.ValidWrite(r, session) {
		t.Fatal("cross-origin write accepted")
	}
	r.Header.Set("Origin", s.Origin)
	r.Header.Set("X-GateForge-CSRF", "wrong")
	if s.ValidWrite(r, session) {
		t.Fatal("forged CSRF accepted")
	}
	store.unavailable = true
	if _, err = s.Authenticate(r.Context(), r); err == nil {
		t.Fatal("database outage allowed access")
	}
	if err = s.Revoke(r.Context(), httptest.NewRecorder(), r); err == nil {
		t.Fatal("failed revocation reported success")
	}
	store.unavailable = false
	delete(s.AllowedEmails, "owner@example.com")
	if _, err = s.Authenticate(r.Context(), r); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("removed member retained access")
	}
	s.AllowedEmails["owner@example.com"] = true
	stored := store.data[Hash(cookie.Value)]
	stored.ExpiresAt = time.Now().Add(-time.Second)
	store.data[Hash(cookie.Value)] = stored
	if _, err = s.Authenticate(r.Context(), r); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expired session accepted")
	}
	if err = s.Revoke(r.Context(), httptest.NewRecorder(), r); err != nil {
		t.Fatal(err)
	}
	if len(store.data) != 0 {
		t.Fatal("logout did not revoke stored session")
	}
}

func TestDuplicateCookiesAndOriginConfiguration(t *testing.T) {
	r := httptest.NewRequest("GET", "https://gateway.example", nil)
	raw, _ := Random()
	r.AddCookie(sessionCookie(raw, 60))
	r.AddCookie(sessionCookie(raw, 60))
	if _, err := rawSession(r); err == nil {
		t.Fatal("ambiguous cookie accepted")
	}
	for _, origin := range []string{"http://gateway.example", "https://user:pass@gateway.example", "https://gateway.example/path", "https://gateway.example?query=1", "https://gateway.example#fragment"} {
		if _, err := CanonicalOrigin(origin); err == nil {
			t.Errorf("unsafe origin %q accepted", origin)
		}
	}
	if origin, err := CanonicalOrigin("https://gateway.example/"); err != nil || origin != "https://gateway.example" {
		t.Fatal(origin, err)
	}
}
