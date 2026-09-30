package analytics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

var bounds = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 120}

type Row struct {
	Route           string   `json:"route"`
	Requests        uint64   `json:"requests"`
	Errors          uint64   `json:"errors"`
	ClientErrors    uint64   `json:"client_errors"`
	CacheHits       uint64   `json:"cache_hits"`
	CacheMisses     uint64   `json:"cache_misses"`
	Bytes           uint64   `json:"bytes"`
	DurationSeconds float64  `json:"duration_seconds"`
	MeanMS          float64  `json:"mean_ms"`
	Buckets         []uint64 `json:"buckets"`
}
type Snapshot struct {
	StartedAt time.Time `json:"started_at"`
	Routes    []Row     `json:"routes"`
	Bounds    []float64 `json:"latency_bounds_seconds"`
}
type Metrics struct {
	mu      sync.Mutex
	started time.Time
	rows    map[string]*metricRow
}

// Each route owns its counters so unrelated routes never contend on updates.
type metricRow struct {
	mu sync.Mutex
	Row
}

func New() *Metrics { return &Metrics{started: time.Now(), rows: map[string]*metricRow{}} }
func (m *Metrics) Wrap(route string, next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	var once sync.Once
	var row *metricRow
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &response{ResponseWriter: w}
		defer func() {
			status := rec.status
			if status == 0 {
				status = 200
				if r.Context().Err() != nil {
					status = 499
				}
			}
			duration := time.Since(start)
			// Resolve once per handler, lazily, so unused routes do not appear in
			// snapshots. Reloaded handlers reuse the same bounded route counters.
			once.Do(func() { row = m.row(route) })
			row.record(status, rec.bytes, duration, w.Header().Get("X-GateForge-Cache"))
		}()
		next.ServeHTTP(rec, r)
	})
}
func (m *Metrics) row(route string) *metricRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[route]; !ok {
		if len(m.rows) >= 512 {
			route = "_other"
		}
		if m.rows[route] == nil {
			m.rows[route] = &metricRow{Row: Row{Route: route, Buckets: make([]uint64, len(bounds))}}
		}
	}
	return m.rows[route]
}
func (row *metricRow) record(status int, n uint64, duration time.Duration, cache string) {
	row.mu.Lock()
	defer row.mu.Unlock()
	row.Requests++
	row.Bytes += n
	row.DurationSeconds += duration.Seconds()
	if status >= 500 {
		row.Errors++
	} else if status >= 400 {
		row.ClientErrors++
	}
	if cache == "HIT" {
		row.CacheHits++
	} else if cache == "MISS" {
		row.CacheMisses++
	}
	for i, b := range bounds {
		if duration.Seconds() <= b {
			row.Buckets[i]++
		}
	}
}
func (m *Metrics) Snapshot() Snapshot {
	m.mu.Lock()
	rows := make([]*metricRow, 0, len(m.rows))
	for _, r := range m.rows {
		rows = append(rows, r)
	}
	m.mu.Unlock()
	s := Snapshot{StartedAt: m.started, Routes: make([]Row, 0, len(rows)), Bounds: append([]float64(nil), bounds...)}
	for _, r := range rows {
		r.mu.Lock()
		row := r.Row
		row.Buckets = append([]uint64(nil), r.Buckets...)
		r.mu.Unlock()
		if row.Requests > 0 {
			row.MeanMS = row.DurationSeconds * 1000 / float64(row.Requests)
		}
		s.Routes = append(s.Routes, row)
	}
	sort.Slice(s.Routes, func(i, j int) bool { return s.Routes[i].Route < s.Routes[j].Route })
	return s
}
func (m *Metrics) Prometheus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# TYPE gateforge_requests_total counter\n# TYPE gateforge_server_errors_total counter\n# TYPE gateforge_client_errors_total counter\n# TYPE gateforge_cache_hits_total counter\n# TYPE gateforge_request_duration_seconds histogram")
	for _, r := range m.Snapshot().Routes {
		label := strconv.Quote(r.Route)
		fmt.Fprintf(w, "gateforge_requests_total{route=%s} %d\ngateforge_server_errors_total{route=%s} %d\ngateforge_client_errors_total{route=%s} %d\ngateforge_cache_hits_total{route=%s} %d\n", label, r.Requests, label, r.Errors, label, r.ClientErrors, label, r.CacheHits)
		for i, b := range bounds {
			fmt.Fprintf(w, "gateforge_request_duration_seconds_bucket{route=%s,le=%q} %d\n", label, strconv.FormatFloat(b, 'f', -1, 64), r.Buckets[i])
		}
		fmt.Fprintf(w, "gateforge_request_duration_seconds_bucket{route=%s,le=\"+Inf\"} %d\ngateforge_request_duration_seconds_sum{route=%s} %g\ngateforge_request_duration_seconds_count{route=%s} %d\n", label, r.Requests, label, r.DurationSeconds, label, r.Requests)
	}
}

type response struct {
	http.ResponseWriter
	status int
	bytes  uint64
}

func (r *response) Unwrap() http.ResponseWriter { return r.ResponseWriter }
func (r *response) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	if r.status == 0 {
		r.status = code
		r.ResponseWriter.WriteHeader(code)
	}
}
func (r *response) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(200)
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += uint64(n)
	return n, err
}
func (r *response) Flush() {
	if r.status == 0 {
		r.WriteHeader(200)
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}
func (r *response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	r.status = 101
	return http.NewResponseController(r.ResponseWriter).Hijack()
}
