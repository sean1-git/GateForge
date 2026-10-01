package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errNoHealthy = errors.New("no healthy upstream")

type backend struct {
	target  *url.URL
	healthy atomic.Bool
}
type backendPool struct {
	route                     Route
	backends                  []*backend
	transport, writeTransport *http.Transport
	counter                   atomic.Uint64
	cancel                    context.CancelFunc
	done                      sync.WaitGroup
}
type BackendStatus struct {
	Route   string `json:"route"`
	URL     string `json:"url"`
	Healthy bool   `json:"healthy"`
	Checked bool   `json:"health_checks_enabled"`
}

func parseTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("upstream must be a valid HTTP or HTTPS URL")
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.Opaque != "" {
		return nil, fmt.Errorf("upstream must be an absolute HTTP or HTTPS URL with a host")
	}
	if target.User != nil || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" {
		return nil, fmt.Errorf("upstream must not contain credentials, a query, or a fragment")
	}
	return target, nil
}
func newPool(route Route) (*backendPool, error) {
	if route.MaxBodyBytes < 0 || route.MaxBodyBytes > 16<<20 {
		return nil, fmt.Errorf("max_body_bytes must be 0..16777216 (0 defaults to 1 MiB)")
	}
	if route.MaxBodyBytes == 0 {
		route.MaxBodyBytes = 1 << 20
	}
	if route.TimeoutMS == 0 {
		route.TimeoutMS = 10000
	}
	targets := route.Upstreams
	if route.Upstream != "" {
		if len(targets) > 0 {
			return nil, fmt.Errorf("use upstream or upstreams, not both")
		}
		targets = []string{route.Upstream}
	}
	if len(targets) == 0 || len(targets) > 32 {
		return nil, fmt.Errorf("each route requires 1..32 upstreams")
	}
	if route.Retries < 0 || route.Retries > 3 {
		return nil, fmt.Errorf("retries must be 0..3")
	}
	if route.TimeoutMS < 0 || route.TimeoutMS > 120000 {
		return nil, fmt.Errorf("timeout_ms must be 0..120000")
	}
	if h := route.Health; h != nil {
		if !strings.HasPrefix(h.Path, "/") || path.Clean(h.Path) != h.Path || strings.ContainsAny(h.Path, "?#%\\") || h.IntervalSeconds < 1 || h.IntervalSeconds > 60 || h.TimeoutMS < 100 || h.TimeoutMS > 10000 {
			return nil, fmt.Errorf("health requires a clean path, interval_seconds 1..60 and timeout_ms 100..10000")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.MaxIdleConnsPerHost = 32
	writes := transport.Clone()
	writes.DisableKeepAlives = true
	p := &backendPool{route: route, transport: transport, writeTransport: writes}
	seen := map[string]bool{}
	for _, raw := range targets {
		u, err := parseTarget(raw)
		if err != nil {
			return nil, err
		}
		if seen[u.String()] {
			return nil, fmt.Errorf("duplicate upstream")
		}
		seen[u.String()] = true
		b := &backend{target: u}
		b.healthy.Store(route.Health == nil)
		p.backends = append(p.backends, b)
	}
	return p, nil
}
func (p *backendPool) start() {
	if p.route.Health == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	for _, b := range p.backends {
		p.done.Add(1)
		go func(b *backend) {
			defer p.done.Done()
			ticker := time.NewTicker(time.Duration(p.route.Health.IntervalSeconds) * time.Second)
			defer ticker.Stop()
			for {
				p.probe(ctx, b)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(b)
	}
}
func (p *backendPool) probe(ctx context.Context, b *backend) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.route.Health.TimeoutMS)*time.Millisecond)
	defer cancel()
	target := *b.target
	target.Path = p.route.Health.Path
	target.RawPath = ""
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	// RoundTrip deliberately does not follow redirects or send credentials.
	resp, err := p.transport.RoundTrip(req)
	healthy := err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300
	if resp != nil {
		resp.Body.Close()
	}
	b.healthy.Store(healthy)
}
func (p *backendPool) close() {
	if p.cancel != nil {
		p.cancel()
		p.done.Wait()
	}
	p.transport.CloseIdleConnections()
	p.writeTransport.CloseIdleConnections()
}
func (p *backendPool) RoundTrip(req *http.Request) (*http.Response, error) {
	safe := (req.Method == "GET" || req.Method == "HEAD") && (req.Body == nil || req.Body == http.NoBody) && req.Header.Get("Upgrade") == ""
	maxAttempts := 1
	if safe {
		maxAttempts += p.route.Retries
	}
	start := int(p.counter.Add(1)-1) % len(p.backends)
	attempts := 0
	var lastErr error = errNoHealthy
	for n := 0; n < len(p.backends) && attempts < maxAttempts; n++ {
		b := p.backends[(start+n)%len(p.backends)]
		if !b.healthy.Load() {
			continue
		}
		attempts++
		outgoing := req.Clone(req.Context())
		copyURL := *req.URL
		outgoing.URL = &copyURL
		rewritten := httputil.ProxyRequest{In: req, Out: outgoing}
		rewritten.SetURL(b.target)
		transport := p.transport
		if !safe {
			transport = p.writeTransport
		}
		response, err := transport.RoundTrip(outgoing)
		transient := err != nil || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504
		if transient && p.route.Health != nil {
			b.healthy.Store(false)
		}
		if !transient || !safe || attempts >= maxAttempts || req.Context().Err() != nil {
			return response, err
		}
		// Only close/discard a response if there actually is another healthy candidate.
		another := false
		for j := n + 1; j < len(p.backends); j++ {
			if p.backends[(start+j)%len(p.backends)].healthy.Load() {
				another = true
				break
			}
		}
		if !another {
			return response, err
		}
		if response != nil {
			response.Body.Close()
		}
		lastErr = err
		if lastErr == nil {
			lastErr = errors.New("upstream returned a retryable error")
		}
	}
	return nil, lastErr
}
