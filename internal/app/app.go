package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gateforge/internal/analytics"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/storage"
)

type Store interface {
	Ping(context.Context) error
	Load(context.Context) (storage.Snapshot, error)
	Save(context.Context, int64, []config.Route) (storage.Snapshot, error)
	CreateKey(context.Context, string, []string, time.Time) (security.Key, string, error)
	ListKeys(context.Context) ([]security.Key, error)
	RevokeKey(context.Context, string) error
}
type state struct {
	handler  http.Handler
	snapshot storage.Snapshot
}
type App struct {
	current      atomic.Pointer[state]
	mu           sync.Mutex
	Store        Store
	Options      gateway.Options
	Logger       *slog.Logger
	AdminToken   string
	TLSOffloaded bool
	UI           http.Handler
}

func New(snapshot storage.Snapshot, store Store, options gateway.Options, logger *slog.Logger, adminToken string, offloaded bool) (*App, error) {
	if options.Metrics == nil {
		options.Metrics = analytics.New()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if adminToken != "" && len(adminToken) < 32 {
		return nil, errors.New("GATEFORGE_ADMIN_TOKEN must have at least 32 characters")
	}
	a := &App{Store: store, Options: options, Logger: logger, AdminToken: adminToken, TLSOffloaded: offloaded}
	h, err := gateway.NewRoutesWithOptions(snapshot.Routes, logger, options)
	if err != nil {
		return nil, err
	}
	a.current.Store(&state{h, snapshot})
	return a, nil
}
func (a *App) Close() {
	if h, ok := a.current.Load().handler.(io.Closer); ok {
		h.Close()
	}
}
func (a *App) Reload(ctx context.Context) error {
	if a.Store == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := a.Store.Load(ctx)
	if err != nil {
		return err
	}
	if s.Revision <= a.current.Load().snapshot.Revision {
		return nil
	}
	h, err := gateway.NewRoutesWithOptions(s.Routes, a.Logger, a.Options)
	if err != nil {
		return err
	}
	a.swap(h, s)
	return nil
}
func (a *App) swap(h http.Handler, s storage.Snapshot) {
	old := a.current.Swap(&state{h, s})
	if old != nil {
		if c, ok := old.handler.(io.Closer); ok {
			c.Close()
		}
	}
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !gateway.ValidRequestPath(r.URL.Path) {
		reply(w, 400, map[string]string{"error": "noncanonical path"})
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.TLS != nil || a.TLSOffloaded {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	switch {
	case r.URL.Path == "/readyz":
		if h, ok := a.current.Load().handler.(interface{ Ready() bool }); ok && !h.Ready() {
			reply(w, 503, map[string]string{"status": "no healthy upstream for one or more routes"})
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			reply(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if a.Store != nil && a.Store.Ping(ctx) != nil {
			reply(w, 503, map[string]string{"status": "not ready"})
			return
		}
		if a.Options.Redis != nil && a.Options.Redis.Ping(ctx) != nil {
			reply(w, 503, map[string]string{"status": "not ready"})
			return
		}
		reply(w, 200, map[string]string{"status": "ready"})
	case strings.HasPrefix(r.URL.Path, "/admin/api/") || r.URL.Path == "/metrics":
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil && !a.TLSOffloaded {
			reply(w, 426, map[string]string{"error": "HTTPS required"})
			return
		}
		expected := sha256.Sum256([]byte(a.AdminToken))
		provided := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if a.AdminToken == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			reply(w, 401, map[string]string{"error": "administrator token required"})
			return
		}
		a.admin(w, r)
	case r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/"):
		if r.TLS == nil && !a.TLSOffloaded {
			reply(w, 426, map[string]string{"error": "HTTPS required"})
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
			return
		}
		if a.UI == nil {
			reply(w, 503, map[string]string{"error": "dashboard build unavailable"})
			return
		}
		a.UI.ServeHTTP(w, r)
	default:
		a.current.Load().handler.ServeHTTP(w, r)
	}
}
func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.Path == "/admin/api/metrics" && r.Method == "GET" {
		reply(w, 200, a.Options.Metrics.Snapshot())
		return
	}
	if r.URL.Path == "/metrics" && r.Method == "GET" {
		a.Options.Metrics.Prometheus(w)
		return
	}
	if r.URL.Path == "/admin/api/backends" && r.Method == "GET" {
		if h, ok := a.current.Load().handler.(interface {
			Backends() []gateway.BackendStatus
		}); ok {
			reply(w, 200, h.Backends())
		} else {
			reply(w, 200, []gateway.BackendStatus{})
		}
		return
	}
	if r.URL.Path == "/admin/api/config" {
		if r.Method == "GET" {
			reply(w, 200, a.current.Load().snapshot)
			return
		}
		if r.Method == "PUT" {
			if a.Store == nil {
				reply(w, 503, map[string]string{"error": "PostgreSQL is required to save configuration"})
				return
			}
			var input storage.Snapshot
			if !decode(w, r, &input) {
				return
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			h, err := gateway.NewRoutesWithOptions(input.Routes, a.Logger, a.Options)
			if err != nil {
				reply(w, 400, map[string]string{"error": err.Error()})
				return
			}
			s, err := a.Store.Save(ctx, input.Revision, input.Routes)
			if err != nil {
				if c, ok := h.(io.Closer); ok {
					c.Close()
				}
				if errors.Is(err, storage.ErrConflict) {
					reply(w, 409, map[string]string{"error": err.Error()})
				} else {
					reply(w, 503, map[string]string{"error": "configuration storage unavailable"})
				}
				return
			}
			a.swap(h, s)
			reply(w, 200, s)
			return
		}
		reply(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.Store == nil {
		reply(w, 503, map[string]string{"error": "PostgreSQL is required"})
		return
	}
	if r.URL.Path == "/admin/api/keys" && r.Method == "GET" {
		keys, err := a.Store.ListKeys(ctx)
		if err != nil {
			reply(w, 503, map[string]string{"error": "key storage unavailable"})
			return
		}
		reply(w, 200, keys)
		return
	}
	if r.URL.Path == "/admin/api/keys" && r.Method == "POST" {
		var input struct {
			Name      string    `json:"name"`
			Prefixes  []string  `json:"prefixes"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if !decode(w, r, &input) {
			return
		}
		if len(input.Name) == 0 || len(input.Name) > 128 || len(input.Prefixes) == 0 || !input.ExpiresAt.After(time.Now()) || input.ExpiresAt.After(time.Now().Add(90*24*time.Hour)) {
			reply(w, 400, map[string]string{"error": "provide a name, route prefixes, and an expiry within 90 days"})
			return
		}
		for _, prefix := range input.Prefixes {
			found := false
			for _, route := range a.current.Load().snapshot.Routes {
				if route.Prefix == prefix && (route.Auth == "api_key" || route.Auth == "either") {
					found = true
				}
			}
			if !found {
				reply(w, 400, map[string]string{"error": "each prefix must name a route that accepts API keys"})
				return
			}
		}
		key, raw, err := a.Store.CreateKey(ctx, input.Name, input.Prefixes, input.ExpiresAt)
		if err != nil {
			reply(w, 503, map[string]string{"error": "key storage unavailable"})
			return
		}
		reply(w, 201, map[string]any{"key": key, "secret": raw})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/api/keys/") && r.Method == "DELETE" {
		err := a.Store.RevokeKey(ctx, strings.TrimPrefix(r.URL.Path, "/admin/api/keys/"))
		if errors.Is(err, security.ErrInvalidKey) {
			reply(w, 404, map[string]string{"error": "key not found"})
			return
		}
		if err != nil {
			reply(w, 503, map[string]string{"error": "key storage unavailable"})
			return
		}
		w.WriteHeader(204)
		return
	}
	reply(w, 404, map[string]string{"error": "endpoint not found"})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		reply(w, 400, map[string]string{"error": "invalid JSON request"})
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		reply(w, 400, map[string]string{"error": "expected one JSON object"})
		return false
	}
	return true
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
