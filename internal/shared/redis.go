package shared

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gateforge/internal/config"
	"gateforge/internal/explain"
	"gateforge/internal/security"
	"github.com/redis/go-redis/v9"
)

type Redis struct{ Client *redis.Client }

func Open(url string) (*Redis, error) {
	o, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_URL")
	}
	o.DialTimeout = 2 * time.Second
	o.ReadTimeout = time.Second
	o.WriteTimeout = time.Second
	o.MaxRetries = -1
	o.ContextTimeoutEnabled = true
	return &Redis{redis.NewClient(o)}, nil
}
func (r *Redis) Close() error                   { return r.Client.Close() }
func (r *Redis) Ping(ctx context.Context) error { return r.Client.Ping(ctx).Err() }

// INCR and expiry execute together, so a crashed gateway cannot leave an immortal counter.
var limitScript = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end; return {n,redis.call('PTTL',KEYS[1])}`)

func Validate(route config.Route, r *Redis) error {
	if route.RateLimit != nil {
		p := route.RateLimit
		if r == nil {
			return fmt.Errorf("rate limiting requires REDIS_URL")
		}
		if p.Requests < 1 || p.Requests > 1000000 || p.WindowSeconds < 1 || p.WindowSeconds > 86400 {
			return fmt.Errorf("rate limit requires 1..1000000 requests and a 1..86400 second window")
		}
	}
	if route.Cache != nil {
		c := route.Cache
		if r == nil {
			return fmt.Errorf("caching requires REDIS_URL")
		}
		if route.Auth != "" && route.Auth != "public" {
			return fmt.Errorf("shared caching is only supported on public routes")
		}
		if c.TTLSeconds < 1 || c.TTLSeconds > 3600 || c.MaxBodyBytes < 1 || c.MaxBodyBytes > 1<<20 {
			return fmt.Errorf("cache TTL must be 1..3600 seconds and max body 1..1048576 bytes")
		}
	}
	return nil
}

func (r *Redis) Wrap(route config.Route, next http.Handler) http.Handler {
	return r.wrap(route, next, "")
}

// WrapGlobal bounds a shared credential's login attempts even across different
// client addresses. Existing sessions remain usable when this quota is exhausted.
func (r *Redis) WrapGlobal(route config.Route, next http.Handler) http.Handler {
	return r.wrap(route, next, "global")
}

func (r *Redis) wrap(route config.Route, next http.Handler, fixedIdentity string) http.Handler {
	if route.Cache != nil {
		next = r.cache(route, next)
	} else {
		downstream := next
		next = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			explain.Add(req.Context(), "cache", "skipped", "Response caching is not configured for this route.")
			downstream.ServeHTTP(w, req)
		})
	}
	if route.RateLimit == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			explain.Add(req.Context(), "rate_limit", "skipped", "No request quota is configured for this route.")
			next.ServeHTTP(w, req)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		identity := security.Principal(req.Context())
		if fixedIdentity != "" {
			identity = fixedIdentity
		}
		if identity == "" {
			ip, _, err := net.SplitHostPort(req.RemoteAddr)
			if err != nil {
				ip = req.RemoteAddr
			}
			identity = "ip:" + ip
		}
		key := "gf:limit:" + security.Hash(route.Prefix+"\x00"+identity)
		ctx, cancel := context.WithTimeout(req.Context(), time.Second)
		values, err := limitScript.Run(ctx, r.Client, []string{key}, route.RateLimit.WindowSeconds*1000).Int64Slice()
		cancel()
		if err != nil || len(values) != 2 {
			explain.Add(req.Context(), "rate_limit", "rejected", "Shared quota storage is unavailable; the gateway fails closed.")
			failure(w, 503, "rate limiter unavailable")
			return
		}
		remaining := int64(route.RateLimit.Requests) - values[0]
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(route.RateLimit.Requests))
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		if values[0] > int64(route.RateLimit.Requests) {
			seconds := (values[1] + 999) / 1000
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			explain.Add(req.Context(), "rate_limit", "rejected", "The shared request allowance for this window is exhausted.")
			failure(w, 429, "rate limit exceeded")
			return
		}
		explain.Add(req.Context(), "rate_limit", "accepted", fmt.Sprintf("Shared quota allowed the request; %d of %d requests remain in this window.", remaining, route.RateLimit.Requests))
		next.ServeHTTP(w, req)
	})
}

type entry struct {
	Header   http.Header `json:"header"`
	Body     []byte      `json:"body"`
	Stored   time.Time   `json:"stored"`
	Lifetime int         `json:"lifetime"`
	Age      int         `json:"age"`
}

func (r *Redis) cache(route config.Route, next http.Handler) http.Handler {
	routeBytes, _ := json.Marshal(route)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if reason := cacheSkipReason(req); reason != "" {
			explain.Add(req.Context(), "cache", "skipped", reason)
			next.ServeHTTP(w, req)
			return
		}
		key := "gf:cache:v2:" + security.Hash(string(routeBytes)+"\x00"+req.Host+"\x00"+req.URL.RequestURI()+"\x00"+strings.Join(req.Header.Values("Accept"), "\x00")+"\x01"+strings.Join(req.Header.Values("Accept-Encoding"), "\x00"))
		ctx, cancel := context.WithTimeout(req.Context(), 500*time.Millisecond)
		data, err := r.Client.Get(ctx, key).Bytes()
		cancel()
		if err == nil && len(data) < 2<<20 {
			var e entry
			if json.Unmarshal(data, &e) == nil && time.Since(e.Stored) < time.Duration(e.Lifetime)*time.Second && len(e.Body) <= route.Cache.MaxBodyBytes {
				for k, vs := range e.Header {
					w.Header()[k] = vs
				}
				explain.Add(req.Context(), "cache", "hit", "A fresh public response was found in Redis; no backend request is needed.")
				w.Header().Set("X-GateForge-Cache", "HIT")
				w.Header().Set("Age", strconv.Itoa(e.Age+int(time.Since(e.Stored).Seconds())))
				w.WriteHeader(200)
				_, _ = w.Write(e.Body)
				return
			}
		}
		if err != nil && err != redis.Nil {
			explain.Add(req.Context(), "cache", "unavailable", "Cache lookup failed; forwarding to a backend.")
		} else {
			explain.Add(req.Context(), "cache", "miss", "No usable fresh cached response was found; forwarding to a backend.")
		}
		w.Header().Set("X-GateForge-Cache", "MISS")
		capture := &capture{ResponseWriter: w, limit: route.Cache.MaxBodyBytes}
		next.ServeHTTP(capture, req)
		ttl := cacheTTL(w.Header(), route.Cache.TTLSeconds)
		if capture.status != 200 || capture.disabled || ttl <= 0 {
			explain.Add(req.Context(), "cache_store", "skipped", "Response was not a complete bounded 200 response with an eligible public cache policy.")
			return
		}
		headers := http.Header{}
		// Only representation metadata is replayed; credentials and request-specific headers never are.
		for _, name := range []string{"Content-Type", "Content-Encoding", "Cache-Control", "ETag", "Last-Modified", "Vary"} {
			if vs := w.Header().Values(name); len(vs) > 0 {
				headers[name] = append([]string(nil), vs...)
			}
		}
		age, _ := strconv.Atoi(w.Header().Get("Age"))
		data, err = json.Marshal(entry{Header: headers, Body: capture.body.Bytes(), Stored: time.Now(), Lifetime: ttl, Age: age})
		if err != nil {
			return
		}
		ctx, cancel = context.WithTimeout(req.Context(), 500*time.Millisecond)
		err = r.Client.Set(ctx, key, data, time.Duration(ttl)*time.Second).Err()
		if err != nil {
			explain.Add(req.Context(), "cache_store", "unavailable", "Response could not be saved to Redis.")
		} else {
			explain.Add(req.Context(), "cache_store", "stored", fmt.Sprintf("Public response saved for up to %d seconds.", ttl))
		}
		cancel()
	})
}
func cacheableRequest(r *http.Request) bool { return cacheSkipReason(r) == "" }
func cacheSkipReason(r *http.Request) string {
	if r.Method != "GET" {
		return "Only GET responses are eligible for shared caching."
	}
	if r.ContentLength != 0 {
		return "Requests with a body are not eligible for shared caching."
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" || r.Header.Get("Cookie") != "" {
		return "Request credentials or cookies prevent shared caching."
	}
	if r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" {
		return "Range and protocol upgrade requests bypass the cache."
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "if-") {
			return "Conditional requests bypass the cache."
		}
	}
	cc := strings.ToLower(r.Header.Get("Cache-Control"))
	if strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") || strings.Contains(cc, "max-age=0") || r.Header.Get("Pragma") != "" {
		return "Client cache directives require bypassing the shared cache."
	}
	return ""
}

func cacheTTL(h http.Header, limit int) int {
	if len(h.Values("Set-Cookie")) > 0 || h.Get("Trailer") != "" || strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		return 0
	}
	for _, part := range strings.Split(strings.Join(h.Values("Vary"), ","), ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		if p != "" && p != "accept" && p != "accept-encoding" {
			return 0
		}
	}
	public := false
	maxAge := -1
	sharedAge := -1
	for _, part := range strings.Split(strings.ToLower(strings.Join(h.Values("Cache-Control"), ",")), ",") {
		p := strings.TrimSpace(part)
		if p == "public" {
			public = true
		}
		if p == "private" || strings.HasPrefix(p, "private=") || p == "no-store" || strings.HasPrefix(p, "no-cache") {
			return 0
		}
		if strings.HasPrefix(p, "max-age=") {
			n, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(p, "max-age="), "\""))
			if err != nil || n < 0 {
				return 0
			}
			maxAge = n
		}
		if strings.HasPrefix(p, "s-maxage=") {
			n, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(p, "s-maxage="), "\""))
			if err != nil || n < 0 {
				return 0
			}
			sharedAge = n
		}
	}
	if sharedAge >= 0 {
		maxAge = sharedAge
	}
	if !public || maxAge <= 0 {
		return 0
	}
	if maxAge < limit {
		limit = maxAge
	}
	if h.Get("Age") != "" {
		age, err := strconv.Atoi(h.Get("Age"))
		if err != nil || age < 0 || age >= maxAge {
			return 0
		}
		if remaining := maxAge - age; remaining < limit {
			limit = remaining
		}
	}
	return limit
}

type capture struct {
	http.ResponseWriter
	status, limit int
	body          bytes.Buffer
	disabled      bool
}

func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }
func (c *capture) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		c.disabled = true
		c.ResponseWriter.WriteHeader(status)
		return
	}
	if c.status == 0 {
		c.status = status
		c.ResponseWriter.WriteHeader(status)
	}
}
func (c *capture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(200)
	}
	n, err := c.ResponseWriter.Write(p)
	if err != nil {
		c.disabled = true
	}
	if !c.disabled {
		if c.body.Len()+n > c.limit {
			c.disabled = true
			c.body.Reset()
		} else {
			c.body.Write(p[:n])
		}
	}
	return n, err
}
func (c *capture) Flush() {
	c.disabled = true
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}
func failure(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, message)
}
