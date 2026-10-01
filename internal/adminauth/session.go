// Package adminauth owns administrator identities and browser sessions.
// Service API keys and service JWTs never grant administrator access.
package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const SessionCookie = "__Host-gateforge_admin"
const SessionLifetime = 8 * time.Hour

var ErrUnauthorized = errors.New("administrator sign-in required")

type Identity struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type Session struct {
	Identity  Identity  `json:"identity"`
	ExpiresAt time.Time `json:"expires_at"`
	CSRFToken string    `json:"csrf_token"`
}

type SessionStore interface {
	CreateAdminSession(context.Context, string, string, string, string, time.Time) (Identity, error)
	AdminSession(context.Context, string) (Session, error)
	DeleteAdminSession(context.Context, string) error
}

// A random session secret is stored only as a hash. The CSRF token is derived
// from it, so neither database records nor a copied CSRF token grant access.
func Random() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func csrf(raw string) string { return Hash("gateforge-admin-csrf\x00" + raw) }

type Sessions struct {
	Store         SessionStore
	Origin        string
	AllowedEmails map[string]bool
	// Token sessions must belong to the currently configured token generation.
	RequiredIdentity string
}

func CanonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("administrator public URL must be an HTTPS origin")
	}
	return "https://" + u.Host, nil
}

func (s *Sessions) Issue(ctx context.Context, w http.ResponseWriter, issuer, subject, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !s.AllowedEmails[email] || issuer == "" || subject == "" || len(subject) > 256 {
		return ErrUnauthorized
	}
	raw, err := Random()
	if err != nil {
		return err
	}
	expires := time.Now().Add(SessionLifetime)
	if _, err = s.Store.CreateAdminSession(ctx, issuer, subject, email, Hash(raw), expires); err != nil {
		return err
	}
	http.SetCookie(w, sessionCookie(raw, int(SessionLifetime.Seconds())))
	return nil
}

func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: SessionCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

func rawSession(r *http.Request) (string, error) {
	var value string
	for _, cookie := range r.Cookies() {
		if cookie.Name != SessionCookie {
			continue
		}
		if value != "" || len(cookie.Value) != 43 {
			return "", ErrUnauthorized
		}
		decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
		if err != nil || len(decoded) != 32 {
			return "", ErrUnauthorized
		}
		value = cookie.Value
	}
	if value == "" {
		return "", ErrUnauthorized
	}
	return value, nil
}

func (s *Sessions) Authenticate(ctx context.Context, r *http.Request) (Session, error) {
	raw, err := rawSession(r)
	if err != nil {
		return Session{}, err
	}
	session, err := s.Store.AdminSession(ctx, Hash(raw))
	if err != nil {
		return Session{}, err
	}
	if !time.Now().Before(session.ExpiresAt) || !s.AllowedEmails[session.Identity.Email] {
		return Session{}, ErrUnauthorized
	}
	if s.RequiredIdentity != "" {
		if session.Identity.ID != s.RequiredIdentity {
			return Session{}, ErrUnauthorized
		}
		session.Identity.Email = ""
		session.Identity.Name = "Administrator"
	}
	session.CSRFToken = csrf(raw)
	return session, nil
}

func (s *Sessions) ValidWrite(r *http.Request, session Session) bool {
	return len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == s.Origin &&
		len(r.Header.Values("X-GateForge-CSRF")) == 1 && session.CSRFToken != "" &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-GateForge-CSRF")), []byte(session.CSRFToken)) == 1
}

func (s *Sessions) Revoke(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	raw, err := rawSession(r)
	if err != nil {
		return err
	}
	if err = s.Store.DeleteAdminSession(ctx, Hash(raw)); err != nil {
		return err
	}
	http.SetCookie(w, sessionCookie("", -1))
	return nil
}

// Administrator cookies belong to the gateway, never to a proxied service.
// Preserve unrelated application cookies and avoid cloning cookie-free requests.
func StripCredentials(r *http.Request) *http.Request {
	cookies := r.Cookies()
	strip := len(r.Header.Values("X-GateForge-CSRF")) != 0
	for _, c := range cookies {
		if c.Name == SessionCookie || c.Name == loginCookie {
			strip = true
		}
	}
	if !strip {
		return r
	}
	clean := r.Clone(r.Context())
	clean.Header.Del("Cookie")
	clean.Header.Del("X-GateForge-CSRF")
	for _, c := range cookies {
		if c.Name != SessionCookie && c.Name != loginCookie {
			clean.AddCookie(c)
		}
	}
	return clean
}

// The API and admin UI currently share an origin. A compromised API backend
// must not overwrite admin cookies or execute HTML scripts in that origin.
// Applications that need executable upstream HTML require a separate origin.
func ProtectUpstream(response *http.Response) error {
	values := response.Header.Values("Set-Cookie")
	if len(values) > 0 {
		response.Header.Del("Set-Cookie")
		for _, value := range values {
			name, _, _ := strings.Cut(value, "=")
			name = strings.TrimSpace(name)
			if name != SessionCookie && name != loginCookie {
				response.Header.Add("Set-Cookie", value)
			}
		}
	}
	response.Header.Del("Clear-Site-Data")
	response.Header.Del("Service-Worker-Allowed")
	response.Header.Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	response.Header.Set("X-Content-Type-Options", "nosniff")
	return nil
}
