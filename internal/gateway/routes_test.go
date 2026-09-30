package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutesChooseService(t *testing.T) {
	backend := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, name)
		}))
		t.Cleanup(s.Close)
		return s
	}
	users, orders, admin, fallback := backend("users"), backend("orders"), backend("admin"), backend("fallback")
	for _, catchAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("catch-all=%v", catchAll), func(t *testing.T) {
			routes := []Route{{Prefix: "/users", Upstream: users.URL}, {Prefix: "/orders", Upstream: orders.URL}, {Prefix: "/users/admin", Upstream: admin.URL}}
			if catchAll {
				// A root route listed first must not shadow a more specific route.
				routes = append([]Route{{Prefix: "/", Upstream: fallback.URL}}, routes...)
			}
			handler, err := NewRoutes(routes, nil)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httptest.NewServer(handler)
			defer proxy.Close()
			for _, tc := range []struct{ path, body string }{
				{"/users", "users"}, {"/users/", "users"}, {"/users/42?active=true", "users"},
				{"/orders/7", "orders"}, {"/users/admin", "admin"}, {"/users/admin/42", "admin"},
				{"/users/administrator", "users"}, {"/users%2Fadmin/42", "admin"},
				{"/users-other", ""}, {"/Users", ""}, {"/missing", ""}, {"/", ""},
				{"/healthz", "{\"status\":\"ok\"}\n"},
			} {
				t.Run(tc.path, func(t *testing.T) {
					wantStatus, wantBody := http.StatusOK, tc.body
					if wantBody == "" {
						if catchAll {
							wantBody = "fallback"
						} else {
							wantStatus, wantBody = http.StatusNotFound, "{\"error\":\"route not found\"}\n"
						}
					}
					resp, err := proxy.Client().Get(proxy.URL + tc.path)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if body := readBody(t, resp); resp.StatusCode != wantStatus || body != wantBody {
						t.Errorf("got %d %q; want %d %q", resp.StatusCode, body, wantStatus, wantBody)
					}
				})
			}
		})
	}
}

func TestRoutePreservesRequestWithUpstreamBasePath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/users/a%2Fb" || r.URL.Query().Get("name") != "hello world" || string(body) != "payload" {
			t.Errorf("unexpected upstream request: %s %s body=%q", r.Method, r.URL, body)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "created")
	}))
	defer upstream.Close()
	handler, err := NewRoutes([]Route{{Prefix: "/users", Upstream: upstream.URL + "/api"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	resp, err := proxy.Client().Post(proxy.URL+"/users/a%2Fb?name=hello%20world", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body := readBody(t, resp); resp.StatusCode != http.StatusCreated || body != "created" {
		t.Errorf("got %d %q; want 201 created", resp.StatusCode, body)
	}
}

func TestRouteFailureDoesNotFallBackOrBreakOtherServices(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer working.Close()
	handler, err := NewRoutes([]Route{{Prefix: "/", Upstream: working.URL}, {Prefix: "/users", Upstream: down.URL}, {Prefix: "/orders", Upstream: working.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	for _, tc := range []struct {
		path   string
		status int
	}{{"/users/1", http.StatusBadGateway}, {"/orders/1", http.StatusNoContent}, {"/healthz", http.StatusOK}} {
		resp, err := proxy.Client().Get(proxy.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s: got %d; want %d", tc.path, resp.StatusCode, tc.status)
		}
	}
}

func TestRoutesRejectInvalidConfiguration(t *testing.T) {
	for _, prefix := range []string{"", "users", "/users/", "/users//admin", "/users/../orders", "/users/.", "/users?x=1", "/users#x", "/users%2Fadmin", "/users\\admin", "/user name", "/users\n", "/healthz"} {
		t.Run(prefix, func(t *testing.T) {
			if _, err := NewRoutes([]Route{{Prefix: prefix, Upstream: "http://127.0.0.1:9000"}}, nil); err == nil {
				t.Error("expected invalid prefix error")
			}
		})
	}
	for _, routes := range [][]Route{
		nil,
		{{Prefix: "/users", Upstream: "http://localhost:9001"}, {Prefix: "/users", Upstream: "http://localhost:9002"}},
		{{Prefix: "/users", Upstream: "http://localhost:9001"}, {Prefix: "/orders", Upstream: "ftp://localhost"}},
	} {
		if _, err := NewRoutes(routes, nil); err == nil {
			t.Errorf("accepted invalid routes: %+v", routes)
		}
	}
}

func TestNoncanonicalPathsCannotCrossAuthBoundary(t *testing.T) {
	h, err := New("http://127.0.0.1:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/public/../private", "/public/%2e%2e/private", "/public/%252e%252e/private", "/public//private", "/public%5cprivate"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Errorf("%s: got %d want 400", path, w.Code)
		}
	}
}
