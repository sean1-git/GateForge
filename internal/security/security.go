package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"gateforge/internal/config"
	"gateforge/internal/explain"
	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidKey = errors.New("invalid API key")

type Key struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefixes  []string  `json:"prefixes"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	Revoked   bool      `json:"revoked"`
}

type KeyReader interface {
	LookupKey(context.Context, string) (Key, error)
}
type principalKey struct{}

func Principal(ctx context.Context) string { p, _ := ctx.Value(principalKey{}).(string); return p }
func Hash(raw string) string               { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gf_" + base64.RawURLEncoding.EncodeToString(b), nil
}

type Authenticator struct {
	Keys             KeyReader
	PublicKey        *rsa.PublicKey
	Issuer, Audience string
	TLSOffloaded     bool
}

func New(keys KeyReader, c config.JWT, offloaded bool) (*Authenticator, error) {
	a := &Authenticator{Keys: keys, Issuer: c.Issuer, Audience: c.Audience, TLSOffloaded: offloaded}
	if c.PublicKeyFile != "" {
		data, err := os.ReadFile(c.PublicKeyFile)
		if err != nil {
			return nil, err
		}
		a.PublicKey, err = jwt.ParseRSAPublicKeyFromPEM(data)
		if err != nil {
			return nil, errors.New("JWT public key must be an RSA PEM key")
		}
		if a.PublicKey.N.BitLen() < 2048 {
			return nil, errors.New("JWT RSA key must have at least 2048 bits")
		}
		if c.Issuer == "" || c.Audience == "" {
			return nil, errors.New("JWT issuer and audience are required")
		}
	}
	return a, nil
}

func (a *Authenticator) ValidateRoute(r config.Route) error {
	switch r.Auth {
	case "", "public":
	case "api_key":
		if a == nil || a.Keys == nil {
			return errors.New("api_key routes require PostgreSQL")
		}
	case "jwt":
		if a == nil || a.PublicKey == nil {
			return errors.New("jwt routes require an RSA public key, issuer and audience")
		}
	case "either":
		if a == nil || a.Keys == nil || a.PublicKey == nil {
			return errors.New("either routes require both API key and JWT configuration")
		}
	default:
		return errors.New("auth must be public, api_key, jwt, or either")
	}
	if r.Scope != "" && r.Auth != "jwt" && r.Auth != "either" {
		return errors.New("scope is only valid for JWT routes")
	}
	return nil
}

func (a *Authenticator) Wrap(route config.Route, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reject := func(status int, reason string) {
			explain.Add(r.Context(), "authentication", "rejected", reason)
			respond(w, status, reason)
		}
		// Clients cannot assert an identity that a backend may trust.
		r.Header.Del("X-GateForge-Subject")
		if route.Auth == "" || route.Auth == "public" {
			explain.Add(r.Context(), "authentication", "skipped", "This route is public; no gateway credential is required.")
			next.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil && !a.TLSOffloaded {
			reject(426, "HTTPS required")
			return
		}
		if len(r.Header.Values("X-API-Key")) > 1 || len(r.Header.Values("Authorization")) > 1 {
			reject(401, "invalid credentials")
			return
		}
		raw, auth := r.Header.Get("X-API-Key"), r.Header.Get("Authorization")
		if raw != "" && auth != "" {
			reject(401, "supply one credential")
			return
		}
		var principal string
		if raw != "" && (route.Auth == "api_key" || route.Auth == "either") {
			if len(raw) > 256 {
				reject(401, "invalid credentials")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			key, err := a.Keys.LookupKey(ctx, Hash(raw))
			cancel()
			if err != nil {
				if errors.Is(err, ErrInvalidKey) {
					reject(401, "invalid credentials")
				} else {
					reject(503, "authentication unavailable")
				}
				return
			}
			if key.Revoked || !time.Now().Before(key.ExpiresAt) {
				reject(401, "invalid credentials")
				return
			}
			allowed := false
			for _, p := range key.Prefixes {
				if p == route.Prefix {
					allowed = true
				}
			}
			if !allowed {
				reject(403, "key is not authorized for this route")
				return
			}
			explain.Add(r.Context(), "authentication", "accepted", "API key is valid, unexpired, not revoked, and authorized for this route.")
			principal = "key:" + key.ID
		} else if strings.HasPrefix(auth, "Bearer ") && (route.Auth == "jwt" || route.Auth == "either") {
			if len(auth) > 16384 {
				reject(401, "invalid credentials")
				return
			}
			claims := jwt.MapClaims{}
			token, err := jwt.ParseWithClaims(strings.TrimPrefix(auth, "Bearer "), claims, func(*jwt.Token) (any, error) { return a.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(a.Issuer), jwt.WithAudience(a.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
			if err != nil || !token.Valid {
				reject(401, "invalid credentials")
				return
			}
			subject, _ := claims.GetSubject()
			if subject == "" || len(subject) > 256 || strings.ContainsAny(subject, "\r\n") {
				reject(401, "invalid credentials")
				return
			}
			if route.Scope != "" {
				scopes, _ := claims["scope"].(string)
				found := false
				for _, s := range strings.Fields(scopes) {
					if s == route.Scope {
						found = true
					}
				}
				if !found {
					reject(403, "required scope missing")
					return
				}
			}
			explain.Add(r.Context(), "authentication", "accepted", "JWT signature, issuer, audience, time claims, subject and required route scope passed validation.")
			principal = "jwt:" + Hash(a.Issuer+"\x00"+subject)
		} else {
			w.Header().Set("WWW-Authenticate", "Bearer")
			reject(401, "credentials required")
			return
		}
		// A hash-based principal avoids forwarding raw tokens or personally identifying JWT claims.
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
		r.Header.Del("X-API-Key")
		r.Header.Del("Authorization")
		next.ServeHTTP(w, r)
	})
}

func respond(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
}
