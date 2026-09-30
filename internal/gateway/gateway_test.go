package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentProxyBodiesRemainIsolated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(r.URL.Query().Get("value"), 7000))
	}))
	defer upstream.Close()
	h, err := New(upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.(io.Closer).Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Different lengths and contents catch stale data and shared buffers.
			value := strings.Repeat(string(rune('a'+i)), i+1)
			want := strings.Repeat(value, 7000)
			for n := 0; n < 12; n++ {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", "http://gateway/item?value="+value, nil))
				if w.Code != 200 || w.Body.String() != want {
					t.Errorf("response for worker %d corrupted: status=%d bytes=%d", i, w.Code, w.Body.Len())
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestProxyForwardsRequestAndResponse(t *testing.T) {
	type requestDetails struct {
		method, path, rawPath, query, body, header string
	}
	received := make(chan requestDetails, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request body: %v", err)
		}
		received <- requestDetails{r.Method, r.URL.Path, r.URL.RawPath, r.URL.Query().Encode(), string(body), r.Header.Get("X-Request-Test")}
		w.Header().Set("X-Upstream-Test", "preserved")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "{\"created\":true}\n")
	}))
	defer upstream.Close()
	proxy := newTestServer(t, upstream.URL)
	defer proxy.Close()

	const query = "tag=go&tag=api&name=hello%20world"
	const body = "{\"name\":\"example\"}"
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/items/a%2Fb?"+query, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Test", "preserved")
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d; want %d", resp.StatusCode, http.StatusCreated)
	}

	got := <-received
	// The standard proxy may normalize query encoding and key order. Values,
	// including repeated parameters in their original order, must survive.
	want := requestDetails{http.MethodPost, "/v1/items/a/b", "/v1/items/a%2Fb", "name=hello+world&tag=go&tag=api", body, "preserved"}
	if got != want {
		t.Errorf("upstream request = %+v; want %+v", got, want)
	}
	if got := resp.Header.Get("X-Upstream-Test"); got != "preserved" {
		t.Errorf("upstream header = %q; want preserved", got)
	}
	if got := readBody(t, resp); got != "{\"created\":true}\n" {
		t.Errorf("response body = %q", got)
	}
}

func TestProxyJoinsBasePathAndRewritesHost(t *testing.T) {
	for _, tc := range []struct {
		name, base, path, rawPath string
	}{
		{"base path", "/api", "/api/items/a/b", "/api/items/a%2Fb"},
		{"trailing slash", "/api/", "/api/items/a/b", "/api/items/a%2Fb"},
		{"escaped base path", "/base%2Froot", "/base/root/items/a/b", "/base%2Froot/items/a%2Fb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type requestDetails struct{ path, rawPath, host string }
			received := make(chan requestDetails, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- requestDetails{r.URL.Path, r.URL.RawPath, r.Host}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			proxy := newTestServer(t, upstream.URL+tc.base)
			defer proxy.Close()

			req, err := http.NewRequest(http.MethodGet, proxy.URL+"/items/a%2Fb", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "public.example.test"
			resp, err := proxy.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("status = %d; want %d", resp.StatusCode, http.StatusNoContent)
			}
			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := <-received, (requestDetails{tc.path, tc.rawPath, target.Host}); got != want {
				t.Errorf("upstream request = %+v; want %+v", got, want)
			}
		})
	}
}

func TestProxyReplacesUntrustedForwardingHeaders(t *testing.T) {
	received := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	proxy := newTestServer(t, upstream.URL)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodGet, proxy.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "public.example.test"
	req.Header.Set("Forwarded", "for=203.0.113.8;host=spoof.example;proto=https")
	req.Header.Set("X-Forwarded-For", "203.0.113.8")
	req.Header.Set("X-Forwarded-Host", "spoof.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d; want %d", resp.StatusCode, http.StatusNoContent)
	}
	headers := <-received
	if got := headers.Get("Forwarded"); got != "" {
		t.Errorf("Forwarded = %q; want removed", got)
	}
	if got := net.ParseIP(headers.Get("X-Forwarded-For")); got == nil || !got.IsLoopback() {
		t.Errorf("X-Forwarded-For = %q; want the actual loopback peer", headers.Get("X-Forwarded-For"))
	}
	if got := headers.Get("X-Forwarded-Host"); got != "public.example.test" {
		t.Errorf("X-Forwarded-Host = %q; want public.example.test", got)
	}
	if got := headers.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q; want http", got)
	}
}

func TestHealthDoesNotCallUpstream(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	proxy := newTestServer(t, upstream.URL)
	defer proxy.Close()

	for _, tc := range []struct {
		method string
		status int
		body   string
	}{
		{http.MethodGet, http.StatusOK, "{\"status\":\"ok\"}\n"},
		{http.MethodHead, http.StatusOK, ""},
		{http.MethodPost, http.StatusMethodNotAllowed, ""},
	} {
		t.Run(tc.method, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, proxy.URL+"/healthz", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := proxy.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("status = %d; want %d", resp.StatusCode, tc.status)
			}
			body := readBody(t, resp)
			if tc.status == http.StatusOK {
				if got := resp.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q; want application/json", got)
				}
				if body != tc.body {
					t.Errorf("body = %q; want %q", body, tc.body)
				}
			} else if got := resp.Header.Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q; want GET, HEAD", got)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("upstream calls = %d; want 0", got)
	}
}

func TestUnavailableUpstreamReturnsGenericBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	upstream.Close()
	proxy := newTestServer(t, upstream.URL)
	defer proxy.Close()

	resp, err := proxy.Client().Get(proxy.URL + "/api")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d; want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}
	if got := readBody(t, resp); got != "{\"error\":\"bad gateway\"}\n" {
		t.Errorf("response body = %q; want generic error without connection details", got)
	}

	health, err := proxy.Client().Get(proxy.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("health status with unavailable upstream = %d; want %d", health.StatusCode, http.StatusOK)
	}
}

func TestProxyStreamsResponse(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "data: second\n\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	proxy := newTestServer(t, upstream.URL)
	defer proxy.Close()
	defer close(release)
	client := proxy.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Get(proxy.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read initial event before upstream completes: %v", err)
	}
	if line != "data: first\n" {
		t.Errorf("first event line = %q", line)
	}
}

func TestNewValidatesUpstream(t *testing.T) {
	for _, upstream := range []string{
		"",
		"localhost:9000",
		"/relative",
		"ftp://example.com",
		"http://",
		"http:///missing-host",
		"http://user:password@example.com",
		"http://example.com?token=secret",
		"http://example.com?",
		"http://example.com#fragment",
		"http:opaque",
		"http://example.com/%zz",
	} {
		t.Run("reject "+upstream, func(t *testing.T) {
			if _, err := New(upstream, nil); err == nil {
				t.Errorf("New(%q, nil) succeeded; want invalid upstream error", upstream)
			}
		})
	}
	for _, upstream := range []string{
		"http://localhost:9000",
		"https://example.com",
		"http://127.0.0.1:9000/base/path",
		"http://[::1]:9000",
		"http://example.com/base%2Fpath/",
	} {
		t.Run("accept "+upstream, func(t *testing.T) {
			if handler, err := New(upstream, nil); err != nil || handler == nil {
				t.Errorf("New(%q, nil) = (%v, %v); want handler and no error", upstream, handler, err)
			}
		})
	}
}

func newTestServer(t *testing.T, upstream string) *httptest.Server {
	t.Helper()
	handler, err := New(upstream, nil)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}
	return httptest.NewServer(handler)
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}
