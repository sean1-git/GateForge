package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gateforge/internal/explain"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/storage"
)

func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/api/policies" {
		a.policyAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/api/requests" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			reply(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		reply(w, 200, map[string]any{"requests": a.Requests.Snapshot(), "capacity": explain.Capacity, "scope": "instance", "retention": "memory"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.Path == "/admin/api/audit" && r.Method == "GET" {
		store, ok := a.Store.(interface {
			ListAudit(context.Context, int64, int) (storage.AuditPage, error)
		})
		if !ok {
			reply(w, 503, map[string]string{"error": "audit storage unavailable"})
			return
		}
		var before int64
		var err error
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, err = strconv.ParseInt(raw, 10, 64)
		}
		if err != nil || before < 0 {
			reply(w, 400, map[string]string{"error": "invalid audit cursor"})
			return
		}
		page, err := store.ListAudit(ctx, before, 50)
		if err != nil {
			reply(w, 503, map[string]string{"error": "audit storage unavailable"})
			return
		}
		reply(w, 200, page)
		return
	}
	if r.URL.Path == "/admin/api/metrics" && r.Method == "GET" {
		snapshot, err := a.Options.Metrics.Shared(ctx)
		if err != nil {
			reply(w, 503, map[string]string{"error": "shared metrics storage unavailable"})
			return
		}
		reply(w, 200, snapshot)
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
