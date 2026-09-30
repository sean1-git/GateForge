package analytics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Keep request construction and response storage out of the timed loop.
func BenchmarkMetricsParallel(b *testing.B) {
	for _, count := range []int{1, 64} {
		b.Run(fmt.Sprintf("routes=%d", count), func(b *testing.B) {
			m := New()
			handlers := make([]http.Handler, count)
			for i := range handlers {
				handlers[i] = m.Wrap(fmt.Sprintf("/service/%d", i), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}))
			}
			var worker atomic.Uint64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				h := handlers[(worker.Add(1)-1)%uint64(count)]
				req := httptest.NewRequest("GET", "/service/item", nil)
				w := &benchmarkWriter{header: make(http.Header)}
				for pb.Next() {
					h.ServeHTTP(w, req)
				}
			})
		})
	}
}

type benchmarkWriter struct{ header http.Header }

func (w *benchmarkWriter) Header() http.Header       { return w.header }
func (*benchmarkWriter) WriteHeader(int)             {}
func (*benchmarkWriter) Write(p []byte) (int, error) { return len(p), nil }
