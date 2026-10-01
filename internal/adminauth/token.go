package adminauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

const tokenIssuer = "gateforge:administrator-token"

// TokenLogin exchanges the root credential once for a revocable browser session.
// It must be mounted behind the distributed login limiter and HTTPS enforcement.
type TokenLogin struct {
	sessions       *Sessions
	expected       [32]byte
	subject, email string
	logger         *slog.Logger
}

func NewTokenLogin(s *Sessions, token string, logger *slog.Logger) (*TokenLogin, error) {
	origin, err := CanonicalOrigin(s.Origin)
	if err != nil {
		return nil, err
	}
	if s.Store == nil || len(token) < 32 || len(token) > 4096 {
		return nil, errors.New("token sessions require storage and a 32..4096 character administrator token")
	}
	s.Origin = origin
	subject := Hash("gateforge-token-generation\x00" + token)
	s.RequiredIdentity = Hash(tokenIssuer + "\x00" + subject)
	email := s.RequiredIdentity + "@token.invalid"
	s.AllowedEmails = map[string]bool{email: true}
	if logger == nil {
		logger = slog.Default()
	}
	return &TokenLogin{s, sha256.Sum256([]byte(token)), subject, email, logger}, nil
}

func (h *TokenLogin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if r.URL.Path != "/admin/auth/token" {
		fail(404, "endpoint not found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(405, "method not allowed")
		return
	}
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.Origin {
		fail(403, "invalid request origin")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		fail(400, "send credentials only in the request body")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var input struct {
		Token string `json:"token"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "invalid sign-in request")
		return
	}
	provided := sha256.Sum256([]byte(input.Token))
	input.Token = ""
	if subtle.ConstantTimeCompare(h.expected[:], provided[:]) != 1 {
		h.logger.Warn("administrator sign-in rejected")
		fail(401, "invalid administrator credential")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err = h.sessions.Issue(ctx, w, tokenIssuer, h.subject, h.email); err != nil {
		fail(503, "administrator session storage unavailable")
		return
	}
	h.logger.Info("administrator signed in", "administrator_id", h.sessions.RequiredIdentity)
	w.WriteHeader(http.StatusNoContent)
}
