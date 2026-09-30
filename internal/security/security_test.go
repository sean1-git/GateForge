package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"gateforge/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type testKeys struct {
	key  Key
	err  error
	hash string
}

func (k *testKeys) LookupKey(_ context.Context, hash string) (Key, error) {
	k.hash = hash
	return k.key, k.err
}
func TestAPIKeyProtection(t *testing.T) {
	keys := &testKeys{key: Key{ID: "one", Prefixes: []string{"/users"}, ExpiresAt: time.Now().Add(time.Hour)}}
	a := &Authenticator{Keys: keys}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Principal(r.Context()) != "key:one" {
			t.Error("missing principal")
		}
		if r.Header.Get("X-API-Key") != "" {
			t.Error("credential forwarded")
		}
		w.WriteHeader(204)
	})
	h := a.Wrap(config.Route{Prefix: "/users", Auth: "api_key"}, next)
	for _, tc := range []struct {
		name  string
		setup func(*http.Request)
		code  int
	}{
		{"valid", func(r *http.Request) {}, 204},
		{"HTTP", func(r *http.Request) { r.TLS = nil; r.Header.Set("X-Forwarded-Proto", "https") }, 426},
		{"missing", func(r *http.Request) { r.Header.Del("X-API-Key") }, 401},
		{"both credentials", func(r *http.Request) { r.Header.Set("Authorization", "Bearer anything") }, 401},
		{"wrong route", func(r *http.Request) { keys.key.Prefixes = []string{"/orders"} }, 403},
		{"expired", func(r *http.Request) { keys.key.ExpiresAt = time.Now().Add(-time.Second) }, 401},
		{"revoked", func(r *http.Request) { keys.key.Revoked = true }, 401},
		{"database unavailable", func(r *http.Request) { keys.err = errors.New("offline") }, 503},
		{"unknown", func(r *http.Request) { keys.err = ErrInvalidKey }, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys.key.Prefixes = []string{"/users"}
			keys.key.ExpiresAt = time.Now().Add(time.Hour)
			keys.key.Revoked = false
			keys.err = nil
			r := httptest.NewRequest("GET", "https://gateway/users", nil)
			r.TLS = &tls.ConnectionState{}
			r.Header.Set("X-API-Key", "secret")
			tc.setup(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("got %d want %d", w.Code, tc.code)
			}
		})
	}
	if keys.hash == "secret" {
		t.Error("plaintext key sent to database")
	}
}
func TestJWTValidation(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a := &Authenticator{PublicKey: &private.PublicKey, Issuer: "issuer", Audience: "gateway"}
	h := a.Wrap(config.Route{Prefix: "/users", Auth: "jwt", Scope: "users:read"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Principal(r.Context()) == "" {
			t.Error("missing principal")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		name   string
		mutate func(jwt.MapClaims)
		method jwt.SigningMethod
		code   int
	}{
		{"valid", func(jwt.MapClaims) {}, jwt.SigningMethodRS256, 204},
		{"expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, jwt.SigningMethodRS256, 401},
		{"no expiry", func(c jwt.MapClaims) { delete(c, "exp") }, jwt.SigningMethodRS256, 401},
		{"wrong issuer", func(c jwt.MapClaims) { c["iss"] = "other" }, jwt.SigningMethodRS256, 401},
		{"wrong audience", func(c jwt.MapClaims) { c["aud"] = "other" }, jwt.SigningMethodRS256, 401},
		{"no subject", func(c jwt.MapClaims) { delete(c, "sub") }, jwt.SigningMethodRS256, 401},
		{"not yet valid", func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Hour).Unix() }, jwt.SigningMethodRS256, 401},
		{"no scope", func(c jwt.MapClaims) { delete(c, "scope") }, jwt.SigningMethodRS256, 403},
		{"wrong algorithm", func(jwt.MapClaims) {}, jwt.SigningMethodHS256, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{"iss": "issuer", "aud": "gateway", "sub": "user1", "exp": time.Now().Add(time.Hour).Unix(), "scope": "users:read"}
			tc.mutate(claims)
			var key any = private
			if tc.method == jwt.SigningMethodHS256 {
				key = []byte("not-the-rsa-key")
			}
			raw, err := jwt.NewWithClaims(tc.method, claims).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "https://gateway/users", nil)
			r.Header.Set("Authorization", "Bearer "+raw)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("got %d want %d", w.Code, tc.code)
			}
		})
	}
}
