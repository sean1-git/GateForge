package security

import (
	"context"
	"gateforge/internal/explain"
	"net/http"
	"sync"
)

type tenantKey struct{}

func Tenant(ctx context.Context) string { value, _ := ctx.Value(tenantKey{}).(string); return value }

// Concurrency is an instance-wide admission bound, shared across routes and reloads.
// There is no waiting queue. Slots cover the complete response, including streaming.
type Concurrency struct {
	mu               sync.Mutex
	total, perTenant int
	active           map[string]int
	stats            ConcurrencyStats
}
type ConcurrencyStats struct {
	Active         int    `json:"active"`
	Peak           int    `json:"peak"`
	Accepted       uint64 `json:"accepted"`
	TenantRejected uint64 `json:"tenant_rejected"`
	GlobalRejected uint64 `json:"global_rejected"`
	Limit          int    `json:"limit"`
	PerTenant      int    `json:"per_tenant"`
}

func NewConcurrency(total, perTenant int) *Concurrency {
	if total < 1 || perTenant < 1 || perTenant > total {
		panic("invalid concurrency limits")
	}
	return &Concurrency{total: total, perTenant: perTenant, active: map[string]int{}, stats: ConcurrencyStats{Limit: total, PerTenant: perTenant}}
}
func (c *Concurrency) Snapshot() ConcurrencyStats { c.mu.Lock(); defer c.mu.Unlock(); return c.stats }
func (c *Concurrency) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := Tenant(r.Context())
		if tenant == "" {
			tenant = Principal(r.Context())
		}
		if tenant == "" {
			tenant = "anonymous"
		}
		c.mu.Lock()
		status := 0
		if c.active[tenant] >= c.perTenant {
			status = 429
			c.stats.TenantRejected++
		} else if c.stats.Active >= c.total {
			status = 503
			c.stats.GlobalRejected++
		}
		if status == 0 {
			c.active[tenant]++
			c.stats.Active++
			c.stats.Accepted++
			if c.stats.Active > c.stats.Peak {
				c.stats.Peak = c.stats.Active
			}
		}
		c.mu.Unlock()
		if status != 0 {
			explain.Add(r.Context(), "concurrency", "rejected", "In-flight request allowance exhausted; no backend contacted and no request queued.")
			w.Header().Set("Retry-After", "1")
			respond(w, status, "concurrent request limit exceeded")
			return
		}
		defer func() {
			c.mu.Lock()
			c.active[tenant]--
			if c.active[tenant] == 0 {
				delete(c.active, tenant)
			}
			c.stats.Active--
			c.mu.Unlock()
		}()
		explain.Add(r.Context(), "concurrency", "accepted", "Tenant and instance have room for this request; the slot is held until response completion.")
		next.ServeHTTP(w, r)
	})
}
