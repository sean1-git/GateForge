package security

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ingress bounds work before authentication, including invalid credentials.
// It uses the connection peer, never caller-controlled forwarding headers.
// Route quotas in Redis remain the distributed per-principal limit.
type Ingress struct {
	mu                 sync.Mutex
	window             time.Time
	counts             map[string]int
	Application, Admin int
}

func NewIngress(application, admin int) *Ingress {
	return &Ingress{Application: application, Admin: admin, counts: map[string]int{}, window: time.Now()}
}
func (l *Ingress) Allow(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		return true
	}
	admin := strings.HasPrefix(r.URL.Path, "/admin/") || r.URL.Path == "/metrics"
	if admin && !strings.HasPrefix(r.URL.Path, "/admin/api/") && !strings.HasPrefix(r.URL.Path, "/admin/auth/") && r.URL.Path != "/metrics" {
		return true
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	key, limit := "application:"+Hash(peer), l.Application
	if admin {
		key, limit = "admin:"+Hash(peer), l.Admin
	}
	l.mu.Lock()
	now := time.Now()
	if now.Sub(l.window) >= time.Minute {
		l.counts = map[string]int{}
		l.window = now
	}
	count, known := l.counts[key]
	allowed := count < limit && (known || len(l.counts) < 4096)
	if allowed {
		l.counts[key] = count + 1
	}
	retry := int(time.Until(l.window.Add(time.Minute)).Seconds()) + 1
	l.mu.Unlock()
	if allowed {
		return true
	}
	if retry < 1 {
		retry = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	respond(w, http.StatusTooManyRequests, "request limit exceeded")
	return false
}
