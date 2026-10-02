package analytics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type brokenMetrics struct{}

func (brokenMetrics) SaveMetrics(context.Context, string, Snapshot) error { return errors.New("down") }
func (brokenMetrics) ReadMetrics(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("down")
}
func TestPersistenceFailurePreservesTrafficAndCounters(t *testing.T) {
	m := New()
	m.EnablePersistence(brokenMetrics{}, "test-instance")
	h := m.Wrap("/users", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://gateway/users", nil))
	if w.Code != 200 {
		t.Fatal("metrics failure broke traffic")
	}
	if m.Snapshot().Routes[0].Requests != 1 {
		t.Fatal("local counter lost")
	}
	if _, err := m.Shared(context.Background()); err == nil {
		t.Fatal("database outage reported healthy shared metrics")
	}
}
func TestAccumulateRetainsHistogramAndAvoidsDuplicates(t *testing.T) {
	first := Snapshot{StartedAt: time.Now(), Routes: []Row{{Route: "/users", Requests: 2, Errors: 1, DurationSeconds: .02, Buckets: []uint64{0, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}}}}
	total := Accumulate(Snapshot{}, Snapshot{}, first)
	total = Accumulate(total, first, first)
	second := Snapshot{StartedAt: first.StartedAt, Routes: []Row{{Route: "/users", Requests: 3, Errors: 1, DurationSeconds: .03, Buckets: []uint64{1, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}}}}
	total = Accumulate(total, first, second)
	if total.Routes[0].Requests != 3 || total.Routes[0].Buckets[0] != 1 || total.Routes[0].Buckets[1] != 3 || total.Routes[0].Errors != 1 {
		t.Fatal(total)
	}
}
