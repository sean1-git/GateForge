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

// Keep network destinations server-owned so policy edits cannot redirect the
// gateway to arbitrary hosts or expose private backend addresses in the browser.
type routePolicy struct {
	SourcePrefix string            `json:"source_prefix,omitempty"`
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
		result.Routes = append(result.Routes, routePolicy{SourcePrefix: r.Prefix, Prefix: r.Prefix, Auth: r.Auth, Scope: r.Scope, TimeoutMS: r.TimeoutMS, MaxBodyBytes: r.MaxBodyBytes, Retries: r.Retries, RateLimit: r.RateLimit, Cache: r.Cache, BackendCount: count})
	}
	return result
}
func (a *App) policyAPI(w http.ResponseWriter, r *http.Request) {
	preview := r.URL.Path == "/admin/api/policies/preview"
	if preview {
		a.previewPolicies(w, r)
		return
	}
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
	routes, err := mergePolicies(current, input)
	if err != nil {
		reply(w, 400, map[string]string{"error": err.Error()})
		return
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

func mergePolicies(current storage.Snapshot, input policySnapshot) ([]config.Route, error) {
	if len(input.Routes) != len(current.Routes) {
		return nil, errors.New("policy edits must preserve existing backend pools")
	}
	byPrefix := map[string]config.Route{}
	for _, r := range current.Routes {
		byPrefix[r.Prefix] = r
	}
	routes := make([]config.Route, 0, len(input.Routes))
	for _, p := range input.Routes {
		source := p.SourcePrefix
		if source == "" {
			source = p.Prefix
		}
		route, ok := byPrefix[source]
		if !ok {
			return nil, errors.New("unknown or duplicate source_prefix")
		}
		delete(byPrefix, source)
		route.Prefix, route.Auth, route.Scope, route.TimeoutMS, route.MaxBodyBytes, route.Retries = p.Prefix, p.Auth, p.Scope, p.TimeoutMS, p.MaxBodyBytes, p.Retries
		route.RateLimit, route.Cache = p.RateLimit, p.Cache
		routes = append(routes, route)
	}
	return routes, nil
}
func (a *App) previewPolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		reply(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var input policySnapshot
	if !decode(w, r, &input) {
		return
	}
	current := a.current.Load().snapshot
	if input.Revision != current.Revision {
		reply(w, 409, map[string]string{"error": "configuration changed; refresh before previewing"})
		return
	}
	routes, err := mergePolicies(current, input)
	if err != nil {
		reply(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if gateway.ValidateRoutes(routes, a.Options) != nil {
		reply(w, 400, map[string]string{"error": "invalid route policy"})
		return
	}
	samples := a.Requests.Preview(routes)
	for i := range samples {
		for _, policy := range input.Routes {
			if samples[i].AfterRoute == policy.Prefix && policy.SourcePrefix != "" && policy.SourcePrefix != samples[i].BeforeRoute {
				samples[i].RoutingChanged = true
				samples[i].Reason += " The proposed route selects a different existing backend pool."
			}
		}
	}
	reply(w, 200, map[string]any{"revision": current.Revision, "samples": samples, "scope": "instance", "note": "Offline historical evidence only. No traffic, probes, credential revalidation or configuration writes. Quotas, cache state, timing, revocation changes and future traffic are not predicted."})
}
