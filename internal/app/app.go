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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gateforge/internal/adminauth"
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
	CreateKey(context.Context, string, []string, time.Time, ...string) (security.Key, string, error)
	ListKeys(context.Context, storage.KeyQuery) (storage.KeyPage, error)
	RevokeKey(context.Context, string) error
}
type state struct {
	handler  http.Handler
	snapshot storage.Snapshot
}
type App struct {
	current       atomic.Pointer[state]
	mu            sync.Mutex
	Store         Store
	Options       gateway.Options
	Logger        *slog.Logger
	AdminToken    string
	TLSOffloaded  bool
	UI            http.Handler
	AdminSessions *adminauth.Sessions
	AdminLogin    http.Handler
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
	var s storage.Snapshot
	var err error
	if store, ok := a.Store.(interface {
		LoadAfter(context.Context, int64) (storage.Snapshot, error)
	}); ok {
		s, err = store.LoadAfter(ctx, a.current.Load().snapshot.Revision)
	} else {
		s, err = a.Store.Load(ctx)
	}
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
	rec := &responseStatus{ResponseWriter: w}
	w = rec
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			a.Logger.Error("request panicked", "method", r.Method)
			if rec.status != 0 {
				panic(http.ErrAbortHandler)
			}
			reply(w, 500, map[string]string{"error": "internal server error"})
		}
		if rec.status >= 500 {
			a.Logger.Error("request failed", "method", r.Method, "status", rec.status)
		}
	}()
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
	case strings.HasPrefix(r.URL.Path, "/admin/auth/"):
		a.adminAuthentication(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/api/") || r.URL.Path == "/metrics":
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil && !a.TLSOffloaded {
			reply(w, 426, map[string]string{"error": "HTTPS required"})
			return
		}
		if a.AdminSessions != nil && r.URL.Path != "/metrics" {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			session, err := a.AdminSessions.Authenticate(ctx, r)
			if err != nil {
				adminSessionError(w, err)
				return
			}
			if r.Header.Get("Authorization") != "" {
				reply(w, 401, map[string]string{"error": "use administrator session authentication"})
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" && !a.AdminSessions.ValidWrite(r, session) {
				reply(w, 403, map[string]string{"error": "invalid request origin or CSRF token"})
				return
			}
			a.admin(w, r)
			if r.Method != "GET" && r.Method != "HEAD" {
				a.Logger.Info("administrator action", "administrator_id", session.Identity.ID, "method", r.Method, "path", r.URL.Path, "status", rec.status)
			}
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
		// Also protect responses served directly from Redis, including older entries.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		a.current.Load().handler.ServeHTTP(w, adminauth.StripCredentials(r))
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
		limit := 50
		var err error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		if err != nil {
			reply(w, 400, map[string]string{"error": "invalid limit"})
			return
		}
		query, err := storage.ParseKeyQuery(limit, r.URL.Query().Get("cursor"))
		if err != nil {
			reply(w, 400, map[string]string{"error": err.Error()})
			return
		}
		keys, err := a.Store.ListKeys(ctx, query)
		if err != nil {
			reply(w, 503, map[string]string{"error": "key storage unavailable"})
			return
		}
		reply(w, 200, keys)
		return
	}
	if r.URL.Path == "/admin/api/keys" && r.Method == "POST" {
		requestID := r.Header.Get("Idempotency-Key")
		if len(r.Header.Values("Idempotency-Key")) > 1 || !validRequestID(requestID) {
			reply(w, 400, map[string]string{"error": "Idempotency-Key must be 16..128 ASCII letters, digits, hyphens or underscores"})
			return
		}
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
		key, raw, err := a.Store.CreateKey(ctx, input.Name, input.Prefixes, input.ExpiresAt, requestID)
		if errors.Is(err, storage.ErrDuplicateRequest) || errors.Is(err, storage.ErrRequestConflict) {
			reply(w, 409, map[string]string{"error": err.Error(), "key_id": key.ID})
			return
		}
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, 413, map[string]string{"error": "request body exceeds 1 MiB"})
			return false
		}
		reply(w, 400, map[string]string{"error": "invalid JSON request"})
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		reply(w, 400, map[string]string{"error": "expected one JSON object"})
		return false
	}
	return true
}
func validRequestID(id string) bool {
	if id == "" {
		return true
	} // Existing API clients may omit deduplication.
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseStatus) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *responseStatus) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseStatus) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
