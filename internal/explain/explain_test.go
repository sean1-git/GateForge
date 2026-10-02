package explain

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestBoundedConcurrentRecordsAndSnapshotIsolation(t *testing.T) {
	var store Store
	var wg sync.WaitGroup
	for i := 0; i < 220; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "/private-id?token=secret", nil), 7, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for j := 0; j < 80; j++ {
					Add(r.Context(), "test", "passed", "Bounded event")
				}
				w.WriteHeader(204)
			}))
			store.Snapshot()
		}()
	}
	wg.Wait()
	rows := store.Snapshot()
	if len(rows) != Capacity {
		t.Fatal(len(rows))
	}
	ids := map[string]bool{}
	for _, row := range rows {
		if ids[row.ID] || len(row.ID) != 32 || len(row.Events) != maxEvents || !row.Truncated || !row.Complete || row.Status != 204 || row.Revision != 7 {
			t.Fatalf("invalid record: %+v", row)
		}
		ids[row.ID] = true
	}
	rows[0].Events[0].Reason = "modified"
	if store.Snapshot()[0].Events[0].Reason == "modified" {
		t.Fatal("snapshot aliases store")
	}
}

func TestResponseIDAndInterruptedStream(t *testing.T) {
	var store Store
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("abort was swallowed")
			}
		}()
		store.Serve(w, httptest.NewRequest("GET", "/", nil), 1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(Header, "spoofed")
			w.WriteHeader(103)
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}))
	}()
	row := store.Snapshot()[0]
	if row.Complete || row.Status != 200 || row.ID != w.Header().Get(Header) || row.ID == "spoofed" || row.Events[len(row.Events)-1].Outcome != "interrupted" {
		t.Fatalf("invalid interrupted record: %+v", row)
	}
}
