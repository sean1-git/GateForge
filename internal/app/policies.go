package app

import (
	"context"
	"errors"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/storage"
	"io"
	"net/http"
	"time"
)

// The browser edits policies only. Network destinations stay on the server.
type routePolicy struct {
	Prefix       string            `json:"prefix"`
	Auth         string            `json:"auth,omitempty"`
	Scope        string            `json:"scope,omitempty"`
	TimeoutMS    int               `json:"timeout_ms,omitempty"`
	MaxBodyBytes int64             `json:"max_body_bytes,omitempty"`
	Retries      int               `json:"retries,omitempty"`
	RateLimit    *config.RateLimit `json:"rate_limit,omitempty"`
	Cache        *config.Cache     `json:"cache,omitempty"`
	BackendCount int               `json:"backend_count"`
}
type policySnapshot struct {
	Revision int64         `json:"revision"`
	Routes   []routePolicy `json:"routes"`
}

func policies(s storage.Snapshot) policySnapshot {
	result := policySnapshot{Revision: s.Revision, Routes: make([]routePolicy, 0, len(s.Routes))}
	for _, r := range s.Routes {
		count := len(r.Upstreams)
		if r.Upstream != "" {
			count = 1
		}
		result.Routes = append(result.Routes, routePolicy{r.Prefix, r.Auth, r.Scope, r.TimeoutMS, r.MaxBodyBytes, r.Retries, r.RateLimit, r.Cache, count})
	}
	return result
}
func (a *App) policyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		reply(w, 200, policies(a.current.Load().snapshot))
		return
	}
	if r.Method != "PUT" {
		w.Header().Set("Allow", "GET, PUT")
		reply(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.Store == nil {
		reply(w, 503, map[string]string{"error": "configuration storage unavailable"})
		return
	}
	var input policySnapshot
	if !decode(w, r, &input) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current := a.current.Load().snapshot
	if input.Revision != current.Revision {
		reply(w, 409, map[string]string{"error": "configuration changed; refresh before saving"})
		return
	}
	if len(input.Routes) != len(current.Routes) {
		reply(w, 400, map[string]string{"error": "policy edits must preserve existing routes"})
		return
	}
	byPrefix := map[string]config.Route{}
	for _, route := range current.Routes {
		byPrefix[route.Prefix] = route
	}
	routes := make([]config.Route, 0, len(input.Routes))
	for _, p := range input.Routes {
		route, ok := byPrefix[p.Prefix]
		if !ok {
			reply(w, 400, map[string]string{"error": "unknown or duplicate route"})
			return
		}
		delete(byPrefix, p.Prefix)
		route.Auth, route.Scope, route.TimeoutMS, route.MaxBodyBytes, route.Retries = p.Auth, p.Scope, p.TimeoutMS, p.MaxBodyBytes, p.Retries
		route.RateLimit, route.Cache = p.RateLimit, p.Cache
		routes = append(routes, route)
	}
	h, err := gateway.NewRoutesWithOptions(routes, a.Logger, a.Options)
	if err != nil {
		reply(w, 400, map[string]string{"error": "invalid route policy; check authentication, quotas, cache and timeout settings"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	s, err := a.Store.Save(ctx, input.Revision, routes)
	if err != nil {
		if c, ok := h.(io.Closer); ok {
			c.Close()
		}
		status := 503
		if errors.Is(err, storage.ErrConflict) {
			status = 409
		}
		reply(w, status, map[string]string{"error": "configuration could not be saved; refresh and retry"})
		return
	}
	a.swap(h, s)
	reply(w, 200, policies(s))
}
