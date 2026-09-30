package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode"

	"gateforge/internal/analytics"
	"gateforge/internal/config"
	"gateforge/internal/security"
	"gateforge/internal/shared"
)

// Route maps a path prefix to one upstream. The full request path is preserved.
type Route = config.Route

// Reject paths whose normalization could cross a route's authorization boundary.
// A remaining percent sign after decoding would permit ambiguous double decoding.
func ValidRequestPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\%") || strings.ContainsFunc(p, unicode.IsControl) {
		return false
	}
	return p == "/" || strings.TrimSuffix(p, "/") == path.Clean(p)
}

type Options struct {
	Auth    *security.Authenticator
	Redis   *shared.Redis
	Metrics *analytics.Metrics
}

type compiledRoute struct {
	prefix  string
	handler http.Handler
}

type runtime struct {
	handler http.Handler
	pools   []*backendPool
}

func (r *runtime) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.handler.ServeHTTP(w, req) }
func (r *runtime) Close() error {
	for _, p := range r.pools {
		p.close()
	}
	return nil
}
func (r *runtime) Backends() []BackendStatus {
	result := []BackendStatus{}
	for _, p := range r.pools {
		for _, b := range p.backends {
			result = append(result, BackendStatus{p.route.Prefix, b.target.String(), b.healthy.Load(), p.route.Health != nil})
		}
	}
	return result
}
func (r *runtime) Ready() bool {
	for _, p := range r.pools {
		found := false
		for _, b := range p.backends {
			if b.healthy.Load() {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// NewRoutes validates and snapshots routes before serving any requests.
// Matching is case-sensitive, uses decoded URL paths and respects slash boundaries.
// The longest matching prefix wins; "/" is an optional catch-all.
func NewRoutes(routes []Route, logger *slog.Logger) (http.Handler, error) {
	return NewRoutesWithOptions(routes, logger, Options{})
}

func NewRoutesWithOptions(routes []Route, logger *slog.Logger, options Options) (http.Handler, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("at least one route is required")
	}
	if len(routes) > 256 {
		return nil, fmt.Errorf("at most 256 routes are supported")
	}
	if logger == nil {
		logger = slog.Default()
	}
	runtime := &runtime{}
	ok := false
	defer func() {
		if !ok {
			runtime.Close()
		}
	}()
	compiled := make([]compiledRoute, 0, len(routes))
	seen := make(map[string]bool, len(routes))
	for i, route := range routes {
		prefix := route.Prefix
		if !strings.HasPrefix(prefix, "/") || path.Clean(prefix) != prefix ||
			strings.ContainsAny(prefix, "?#%\\") || strings.ContainsFunc(prefix, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
			return nil, fmt.Errorf("route %d: prefix must be a clean absolute path without a trailing slash, escapes, query, or fragment (except /)", i+1)
		}
		if prefix == "/healthz" || prefix == "/readyz" || prefix == "/metrics" || prefix == "/admin" || strings.HasPrefix(prefix, "/admin/") {
			return nil, fmt.Errorf("route %d: prefix is reserved for gateway operations", i+1)
		}
		if seen[prefix] {
			return nil, fmt.Errorf("route %d: duplicate prefix %q", i+1, prefix)
		}
		seen[prefix] = true
		if err := options.Auth.ValidateRoute(route); err != nil {
			return nil, fmt.Errorf("route %d: %w", i+1, err)
		}
		if err := shared.Validate(route, options.Redis); err != nil {
			return nil, fmt.Errorf("route %d: %w", i+1, err)
		}
		pool, err := newPool(route)
		if err != nil {
			return nil, fmt.Errorf("route %d (%s): %w", i+1, prefix, err)
		}
		runtime.pools = append(runtime.pools, pool)
		proxy := newProxy(pool, logger)
		compiled = append(compiled, compiledRoute{prefix, options.Metrics.Wrap(prefix, options.Auth.Wrap(route, options.Redis.Wrap(route, proxy)))})
	}
	sort.Slice(compiled, func(i, j int) bool { return len(compiled[i].prefix) > len(compiled[j].prefix) })
	unmatched := options.Metrics.Wrap("_unmatched", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, `{"error":"route not found"}`)
	}))

	runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ValidRequestPath(r.URL.Path) {
			writeJSON(w, 400, `{"error":"noncanonical path"}`)
			return
		}
		if r.URL.Path == "/healthz" {
			serveHealth(w, r)
			return
		}
		for _, route := range compiled {
			if route.prefix == "/" || r.URL.Path == route.prefix || strings.HasPrefix(r.URL.Path, route.prefix+"/") {
				route.handler.ServeHTTP(w, r)
				return
			}
		}
		unmatched.ServeHTTP(w, r)
	})
	for _, p := range runtime.pools {
		p.start()
	}
	ok = true
	return runtime, nil
}

func serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, `{"error":"method not allowed"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, `{"status":"ok"}`)
}
