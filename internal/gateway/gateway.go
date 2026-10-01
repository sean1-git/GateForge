// Package gateway forwards HTTP requests to upstreams selected by path prefix.
package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"gateforge/internal/adminauth"
	"gateforge/internal/security"
	"io"
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
		Transport:      pool,
		ModifyResponse: adminauth.ProtectUpstream,
		ErrorLog:       slog.NewLogLogger(logger.Handler(), slog.LevelError),
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
		// Bound memory and reject oversized/chunked bodies before any upstream
		// side effect. Streaming uploads are intentionally unsupported.
		if r.ContentLength > pool.route.MaxBodyBytes {
			writeJSON(w, 413, `{"error":"request body too large"}`)
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			body := http.MaxBytesReader(w, r.Body, pool.route.MaxBodyBytes)
			data, err := io.ReadAll(body)
			body.Close()
			if err != nil {
				var large *http.MaxBytesError
				var timeout interface{ Timeout() bool }
				if errors.As(err, &large) {
					writeJSON(w, 413, `{"error":"request body too large"}`)
				} else if errors.As(err, &timeout) && timeout.Timeout() {
					writeJSON(w, 408, `{"error":"request body timeout"}`)
				} else {
					writeJSON(w, 400, `{"error":"could not read request body"}`)
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			r.ContentLength = int64(len(data))
			r.TransferEncoding = nil
		}
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
