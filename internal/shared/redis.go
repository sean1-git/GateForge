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
	if route.Cache != nil {
		next = r.cache(route, next)
	}
	if route.RateLimit == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		identity := security.Principal(req.Context())
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
			failure(w, 429, "rate limit exceeded")
			return
		}
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
		if !cacheableRequest(req) {
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
				w.Header().Set("X-GateForge-Cache", "HIT")
				w.Header().Set("Age", strconv.Itoa(e.Age+int(time.Since(e.Stored).Seconds())))
				w.WriteHeader(200)
				_, _ = w.Write(e.Body)
				return
			}
		}
		w.Header().Set("X-GateForge-Cache", "MISS")
		capture := &capture{ResponseWriter: w, limit: route.Cache.MaxBodyBytes}
		next.ServeHTTP(capture, req)
		ttl := cacheTTL(w.Header(), route.Cache.TTLSeconds)
		if capture.status != 200 || capture.disabled || ttl <= 0 {
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
		_ = r.Client.Set(ctx, key, data, time.Duration(ttl)*time.Second).Err()
		cancel()
	})
}
func cacheableRequest(r *http.Request) bool {
	if r.Method != "GET" || r.ContentLength != 0 || r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" {
		return false
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "if-") {
			return false
		}
	}
	cc := strings.ToLower(r.Header.Get("Cache-Control"))
	return !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") && !strings.Contains(cc, "max-age=0") && r.Header.Get("Pragma") == ""
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
