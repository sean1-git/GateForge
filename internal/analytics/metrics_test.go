package analytics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSnapshotsDuringConcurrentUpdatesAndReloads(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-done:
				return
			default:
				for _, row := range m.Snapshot().Routes {
					// Each row must be internally consistent during a scrape.
					if row.Bytes != row.Requests*2 || row.Errors != row.Requests {
						t.Errorf("inconsistent snapshot: %+v", row)
						return
					}
				}
			}
		}
	}()
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Multiple handler generations share the same route counters.
			h := m.Wrap(fmt.Sprintf("/route/%d", i%4), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(503)
				_, _ = w.Write([]byte("no"))
			}))
			req := httptest.NewRequest("GET", "/", nil)
			for n := 0; n < 100; n++ {
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		}(i)
	}
	wg.Wait()
	close(done)
	<-readerDone
	s := m.Snapshot()
	if len(s.Routes) != 4 {
		t.Fatalf("got %d routes", len(s.Routes))
	}
	for _, row := range s.Routes {
		if row.Requests != 800 || row.Bytes != 1600 || row.Errors != 800 {
			t.Fatalf("lost updates: %+v", row)
		}
	}
	s.Routes[0].Buckets[0] = ^uint64(0)
	if m.Snapshot().Routes[0].Buckets[0] == ^uint64(0) {
		t.Fatal("snapshot aliases live counters")
	}
}

func TestMetricsBoundedAcrossHandlerGenerations(t *testing.T) {
	m := New()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	m.Wrap("/unused", next)
	if len(m.Snapshot().Routes) != 0 {
		t.Fatal("unused route was recorded")
	}
	req := httptest.NewRequest("GET", "/", nil)
	for i := 0; i < 600; i++ {
		m.Wrap(fmt.Sprintf("/route/%d", i), next).ServeHTTP(httptest.NewRecorder(), req)
	}
	s := m.Snapshot()
	if len(s.Routes) != 513 {
		t.Fatalf("unbounded cardinality: %d", len(s.Routes))
	}
	var total uint64
	for _, row := range s.Routes {
		total += row.Requests
	}
	if total != 600 {
		t.Fatalf("lost overflow requests: %d", total)
	}
}

func TestConcurrentMetricsAndStreaming(t *testing.T) {
	m := New()
	h := m.Wrap("/users", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-GateForge-Cache", "HIT")
		w.WriteHeader(503)
		w.Write([]byte("no"))
	}))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/private-id?secret=not-recorded", nil))
		}()
	}
	wg.Wait()
	s := m.Snapshot()
	if len(s.Routes) != 1 || s.Routes[0].Requests != 100 || s.Routes[0].Errors != 100 || s.Routes[0].CacheHits != 100 {
		t.Fatalf("bad snapshot: %+v", s)
	}
	w := httptest.NewRecorder()
	m.Prometheus(w)
	if strings.Contains(w.Body.String(), "private-id") || strings.Contains(w.Body.String(), "secret") {
		t.Error("sensitive request data recorded")
	}
	stream := m.Wrap("/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("event"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
	}))
	w = httptest.NewRecorder()
	stream.ServeHTTP(w, httptest.NewRequest("GET", "/stream", nil))
	if !w.Flushed {
		t.Error("metrics wrapper broke flushing")
	}
}
