package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"gateforge/internal/analytics"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/shared"
	"gateforge/internal/storage"
	"gateforge/internal/testutil"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in bounded stress exercise against disposable databases and backends.
// Closed-loop throughput here is evidence for this test host, not a Cloud Run SLA.
func TestCapacityWithTwoTLSGateways(t *testing.T) {
	if os.Getenv("GATEFORGE_CAPACITY_TEST") != "1" {
		t.Skip("opt-in capacity matrix")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, testutil.DatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	redis, err := shared.Open(os.Getenv("GATEFORGE_TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	var firstDown atomic.Bool
	backend := func(fault bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fault && firstDown.Load() {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=30")
			w.Write([]byte(`{"sample":true}`))
		}))
	}
	first, second := backend(true), backend(false)
	defer first.Close()
	defer second.Close()
	routes := []config.Route{{Prefix: "/users", Upstreams: []string{first.URL, second.URL}, Auth: "api_key", Retries: 1, TimeoutMS: 1500, Health: &config.Health{Path: "/healthz", IntervalSeconds: 1, TimeoutMS: 100}, RateLimit: &config.RateLimit{Requests: 1000000, WindowSeconds: 60}}, {Prefix: "/catalog", Upstream: second.URL, Cache: &config.Cache{TTLSeconds: 30, MaxBodyBytes: 1024}}}
	auth, err := security.New(db, config.JWT{}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := db.CreateKey(ctx, "capacity fixture", []string{"/users"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	servers := []*httptest.Server{}
	apps := []*App{}
	for _, id := range []string{"capacity-one", "capacity-two"} {
		metrics := analytics.New()
		metrics.EnablePersistence(db, id)
		a, err := New(storage.Snapshot{Revision: 1, Routes: routes}, db, gateway.Options{Auth: auth, Redis: redis, Metrics: metrics}, logger, "", false)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		// The load generator shares one peer address. Measure proxy capacity with
		// a high allowance, matching the route quota; limiter correctness has its
		// own tests and must not turn this exercise into a benchmark of 429s.
		a.Ingress = security.NewIngress(1000000, 120)
		s := httptest.NewTLSServer(a)
		defer s.Close()
		apps = append(apps, a)
		servers = append(servers, s)
	}
	// httptest certificates are trusted through the server's generated client roots.
	transport := servers[0].Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.MaxIdleConnsPerHost = 100
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	defer transport.CloseIdleConnections()
	for _, path := range []string{"/catalog", "/users"} {
		for _, concurrency := range []int{1, 10, 40, 100} {
			var mu sync.Mutex
			latencies := []float64{}
			failed := 0
			statuses := map[int]int{}
			start := time.Now()
			deadline := start.Add(2 * time.Second)
			var wg sync.WaitGroup
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					for time.Now().Before(deadline) {
						began := time.Now()
						r, _ := http.NewRequest("GET", servers[i%2].URL+path, nil)
						if path == "/users" {
							r.Header.Set("X-API-Key", secret)
						}
						res, err := client.Do(r)
						ok := err == nil
						status := 0 // transport failure
						if err == nil {
							status = res.StatusCode
							ok = res.StatusCode == 200
							_, err = io.Copy(io.Discard, res.Body)
							res.Body.Close()
							ok = ok && err == nil
						}
						mu.Lock()
						statuses[status]++
						if !ok {
							failed++
						}
						latencies = append(latencies, time.Since(began).Seconds()*1000)
						mu.Unlock()
					}
				}(i)
			}
			wg.Wait()
			sort.Float64s(latencies)
			if len(latencies) == 0 {
				t.Fatal("capacity exercise sent no requests")
			}
			report, _ := json.Marshal(map[string]any{"scenario": path, "concurrency": concurrency, "requests": len(latencies), "errors": failed, "statuses": statuses, "rps": float64(len(latencies)) / time.Since(start).Seconds(), "p95_ms": latencies[(len(latencies)-1)*95/100], "p99_ms": latencies[(len(latencies)-1)*99/100]})
			t.Log("CAPACITY " + string(report))
			// Saturation is a measured result, not a correctness assertion. The
			// low-load baseline and recovery below must still serve every request.
			if concurrency == 1 && failed > 0 {
				t.Fatalf("baseline failed: %s errors %d", path, failed)
			}
		}
	}
	firstDown.Store(true)
	time.Sleep(1200 * time.Millisecond)
	for i := 0; i < 100; i++ {
		r, _ := http.NewRequest("GET", servers[i%2].URL+"/users", nil)
		r.Header.Set("X-API-Key", secret)
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal("replica loss failed request", res.StatusCode)
		}
	}
	for _, a := range apps {
		if err = a.Options.Metrics.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := db.ReadMetrics(ctx)
	if err != nil || len(snapshot.Routes) != 2 {
		t.Fatal("shared metrics unavailable after capacity test", err)
	}
	t.Log("FAILURE: remaining backend served 100 authenticated requests across two gateways after replica became unhealthy")
}
