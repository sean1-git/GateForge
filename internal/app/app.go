package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gateforge/internal/adminauth"
	"gateforge/internal/analytics"
	"gateforge/internal/config"
	"gateforge/internal/experiment"
	"gateforge/internal/explain"
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
	Lab           experiment.Manager
	Ingress       *security.Ingress
	Requests      explain.Store
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
	if options.Concurrency == nil {
		options.Concurrency = security.NewConcurrency(128, 16)
	}
	if options.Metrics == nil {
		options.Metrics = analytics.New()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if adminToken != "" && len(adminToken) < 32 {
		return nil, errors.New("GATEFORGE_ADMIN_TOKEN must have at least 32 characters")
	}
	a := &App{Ingress: security.NewIngress(600, 120), Store: store, Options: options, Logger: logger, AdminToken: adminToken, TLSOffloaded: offloaded}
	h, err := gateway.NewRoutesWithOptions(snapshot.Routes, logger, options)
	if err != nil {
		return nil, err
	}
	a.current.Store(&state{h, snapshot})
	return a, nil
}
func (a *App) Close() {
	a.Lab.Close()
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
	if a.Ingress != nil && !a.Ingress.Allow(w, r) {
		return
	}
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
		a.serveAdmin(rec, r)
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
		// Cache hits bypass the proxy's response hook, and older entries may lack
		// current protections. Enforce the same boundary at the outer handler.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		current := a.current.Load()
		if r.URL.Path == "/healthz" {
			current.handler.ServeHTTP(w, r)
			return
		}
		a.Requests.Serve(w, adminauth.StripCredentials(r), current.snapshot.Revision, current.handler)
	}
}
