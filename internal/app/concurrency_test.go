package app

import (
	"context"
	"fmt"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/shared"
	"gateforge/internal/storage"
	"gateforge/internal/testutil"
	"github.com/alicebob/miniredis/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSimultaneousUsersAcrossGateways(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	redisURL := os.Getenv("GATEFORGE_TEST_REDIS_URL")
	if redisURL == "" {
		mini := miniredis.RunT(t)
		redisURL = "redis://" + mini.Addr()
	}
	redisStore, err := shared.Open(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer redisStore.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer backend.Close()
	prefix := fmt.Sprintf("/simultaneous-%d", time.Now().UnixNano())
	routes := []config.Route{{Prefix: prefix, Upstream: backend.URL, Auth: "api_key", RateLimit: &config.RateLimit{Requests: 20, WindowSeconds: 60}}}
	var servers []*httptest.Server
	for i := 0; i < 2; i++ {
		a, err := New(storage.Snapshot{Revision: 1, Routes: routes}, db, gateway.Options{Auth: &security.Authenticator{Keys: db}, Redis: redisStore}, nil, "", false)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		server := httptest.NewTLSServer(a)
		defer server.Close()
		servers = append(servers, server)
	}
	var secrets []string
	for i := 0; i < 2; i++ {
		_, secret, err := db.CreateKey(ctx, "simultaneous-client", []string{prefix}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	var allowed, denied [2]atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			user := i % 2
			server := servers[(i/2)%2]
			req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+prefix, nil)
			req.Header.Set("X-API-Key", secrets[user])
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			switch resp.StatusCode {
			case 204:
				allowed[user].Add(1)
			case 429:
				denied[user].Add(1)
			default:
				t.Errorf("unexpected status %d", resp.StatusCode)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < 2; i++ {
		if allowed[i].Load() != 20 || denied[i].Load() != 30 {
			t.Fatalf("client %d: accepted=%d limited=%d", i, allowed[i].Load(), denied[i].Load())
		}
	}
	t.Log("100 simultaneous HTTPS requests across two gateways: independent per-key quotas held")
}
