// Package gateway forwards HTTP requests to upstreams selected by path prefix.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"gateforge/internal/security"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

const proxyBufferSize = 32 * 1024

// Share copy buffers across routes and configuration reloads. ReverseProxy owns
// each buffer until the response finishes, including streaming responses.
var responseBuffers proxyBufferPool

type proxyBufferPool struct{ pool sync.Pool }

func (p *proxyBufferPool) Get() []byte {
	if b := p.pool.Get(); b != nil {
		return b.(*[proxyBufferSize]byte)[:]
	}
	return make([]byte, proxyBufferSize)
}

func (p *proxyBufferPool) Put(b []byte) {
	if cap(b) == proxyBufferSize {
		// Store a pointer rather than boxing a slice on every response.
		p.pool.Put((*[proxyBufferSize]byte)(b[:proxyBufferSize]))
	}
}

// New builds a gateway with a local health endpoint and one fixed upstream.
// An upstream path prefix is joined with each incoming request path.
func New(upstream string, logger *slog.Logger) (http.Handler, error) {
	return NewRoutes([]Route{{Prefix: "/", Upstream: upstream}}, logger)
}

func newProxy(pool *backendPool, logger *slog.Logger) http.Handler {
	proxy := &httputil.ReverseProxy{
		BufferPool: &responseBuffers,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: "gateway.invalid"})
			// ReverseProxy removes incoming forwarding headers before Rewrite.
			r.SetXForwarded()
			r.Out.Header.Del("X-API-Key")
			r.Out.Header.Del("X-GateForge-Subject")
			if p := security.Principal(r.In.Context()); p != "" {
				r.Out.Header.Set("X-GateForge-Subject", p)
			}
		},
		Transport: pool,
		ErrorLog:  slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("upstream request failed", "method", r.Method, "route", pool.route.Prefix)
			if errors.Is(err, context.DeadlineExceeded) {
				writeJSON(w, 504, `{"error":"upstream timeout"}`)
			} else if errors.Is(err, errNoHealthy) {
				writeJSON(w, 503, `{"error":"no healthy upstream"}`)
			} else {
				writeJSON(w, http.StatusBadGateway, `{"error":"bad gateway"}`)
			}
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pool.route.TimeoutMS > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(pool.route.TimeoutMS)*time.Millisecond)
			defer cancel()
			r = r.WithContext(ctx)
		}
		proxy.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintln(w, body)
}
