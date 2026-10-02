// Package explain records bounded, credential-free gateway decision timelines.
package explain

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const Capacity = 100
const maxEvents = 64
const Header = "X-GateForge-Request-ID"

type Event struct {
	Stage     string  `json:"stage"`
	Outcome   string  `json:"outcome"`
	Reason    string  `json:"reason"`
	ElapsedMS float64 `json:"elapsed_ms"`
}
type Record struct {
	ID         string    `json:"id"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Revision   int64     `json:"revision"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS float64   `json:"duration_ms"`
	Status     int       `json:"status"`
	Complete   bool      `json:"complete"`
	Events     []Event   `json:"events"`
	Truncated  bool      `json:"truncated"`
}
type Store struct {
	mu          sync.Mutex
	rows        [Capacity]Record
	next, count int
}
type key struct{}
type timeline struct {
	mu      sync.Mutex
	started time.Time
	record  Record
}

func ID(ctx context.Context) string {
	if t, _ := ctx.Value(key{}).(*timeline); t != nil {
		return t.record.ID
	}
	return ""
}

func bounded(s string) string {
	if len(s) > 512 {
		return strings.Clone(s[:512])
	}
	return s
}

// Add accepts only code-owned reasons and configuration metadata. Never pass
// request headers, URLs, identities, bodies, or raw error strings here.
func Add(ctx context.Context, stage, outcome, reason string) {
	t, _ := ctx.Value(key{}).(*timeline)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.record.Events) >= maxEvents {
		t.record.Truncated = true
		return
	}
	t.record.Events = append(t.record.Events, Event{bounded(stage), bounded(outcome), bounded(reason), float64(time.Since(t.started).Microseconds()) / 1000})
}
func Route(ctx context.Context, prefix string) {
	if t, _ := ctx.Value(key{}).(*timeline); t != nil {
		t.mu.Lock()
		t.record.Route = bounded(prefix)
		t.mu.Unlock()
	}
}
func (s *Store) Snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]Record, 0, s.count)
	for i := 0; i < s.count; i++ {
		r := s.rows[(s.next-1-i+Capacity)%Capacity]
		r.Events = append([]Event(nil), r.Events...)
		rows = append(rows, r)
	}
	return rows
}

// Serve records one application request. Administrative and health traffic is
// excluded by the caller. A restart drops the buffer; it is not an audit log.
func (s *Store) Serve(w http.ResponseWriter, r *http.Request, revision int64, next http.Handler) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		next.ServeHTTP(w, r)
		return
	}
	method := r.Method
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
	default:
		method = "OTHER"
	}
	started := time.Now() // Preserve the monotonic clock for elapsed times.
	t := &timeline{started: started, record: Record{ID: hex.EncodeToString(id[:]), Method: method, Revision: revision, StartedAt: started.UTC(), Events: make([]Event, 0, 16)}}
	r = r.WithContext(context.WithValue(r.Context(), key{}, t))
	rec := &response{ResponseWriter: w, id: t.record.ID}
	finished := false
	defer func() {
		outcome, reason := "complete", "Response handling finished."
		if !finished || rec.failed || r.Context().Err() != nil {
			outcome, reason = "interrupted", "Response handling was interrupted; delivery is not confirmed."
		}
		Add(r.Context(), "response", outcome, reason)
		t.mu.Lock()
		t.record.Complete = outcome == "complete"
		t.record.Status = rec.status
		if rec.status == 0 && t.record.Complete {
			t.record.Status = 200
		}
		t.record.DurationMS = float64(time.Since(t.started).Microseconds()) / 1000
		s.mu.Lock()
		s.rows[s.next] = t.record
		s.next = (s.next + 1) % Capacity
		if s.count < Capacity {
			s.count++
		}
		s.mu.Unlock()
		t.mu.Unlock()
	}()
	rec.Header().Set(Header, rec.id)
	next.ServeHTTP(rec, r)
	finished = true
}

type response struct {
	http.ResponseWriter
	id     string
	status int
	failed bool
}

func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *response) WriteHeader(code int) {
	w.Header().Set(Header, w.id) // Upstreams and cached responses cannot spoof it.
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *response) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		w.failed = true
	}
	return n, err
}
func (w *response) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if http.NewResponseController(w.ResponseWriter).Flush() != nil {
		w.failed = true
	}
}
func (w *response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		w.failed = true
	} else {
		w.status = 101
	}
	return c, b, err
}
