package gateway

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// Includes a real loopback upstream, but discards downstream bodies to isolate
// proxy overhead from client response buffering. No TLS, Redis or database.
func BenchmarkProxyResponse(b *testing.B) {
	for _, size := range []int{1024, 64 * 1024} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			body := bytes.Repeat([]byte("x"), size)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, _ = w.Write(body)
			}))
			defer upstream.Close()
			h, err := New(upstream.URL, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer h.(io.Closer).Close()
			req := httptest.NewRequest("GET", "http://gateway/item", nil)
			w := &discardResponse{header: make(http.Header)}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				clear(w.header)
				w.status = 0
				h.ServeHTTP(w, req)
				if w.status != 200 {
					b.Fatalf("status = %d", w.status)
				}
			}
		})
	}
}

type discardResponse struct {
	header http.Header
	status int
}

func (w *discardResponse) Header() http.Header       { return w.header }
func (w *discardResponse) WriteHeader(status int)    { w.status = status }
func (*discardResponse) Write(p []byte) (int, error) { return len(p), nil }
