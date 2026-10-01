package app

import (
	"bytes"
	"compress/gzip"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/storage"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminLimitsAndSafeRecovery(t *testing.T) {
	token := strings.Repeat("a", 40)
	s := &fakeStore{snapshot: storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/users", Upstream: "http://localhost:9001"}}}}
	var logs bytes.Buffer
	a, err := New(s.snapshot, s, gateway.Options{}, slog.New(slog.NewJSONHandler(&logs, nil)), token, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, tc := range []struct {
		path, method, body string
		status             int
	}{
		{"/admin/api/keys?limit=201", "GET", "", 400}, {"/admin/api/keys?cursor=invalid", "GET", "", 400},
		{"/admin/api/config", "PUT", `{"revision":1,"routes":[],"extra":"` + strings.Repeat("x", 1<<20) + `"}`, 413},
	} {
		r := httptest.NewRequest(tc.method, "https://gateway"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	a.UI = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("sensitive-panic-value") })
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "https://gateway/admin/", nil))
	if w.Code != 500 || !strings.Contains(logs.String(), "request panicked") || strings.Contains(logs.String(), "sensitive-panic-value") {
		t.Fatal("unsafe panic handling")
	}
}
func TestCompressedPublicAssets(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "assets"), 0700)
	data := []byte("console.log('public asset');")
	os.WriteFile(filepath.Join(dir, "assets", "app.js"), data, 0600)
	var packed bytes.Buffer
	z := gzip.NewWriter(&packed)
	z.Write(data)
	z.Close()
	os.WriteFile(filepath.Join(dir, "assets", "app.js.gz"), packed.Bytes(), 0600)
	h := StaticUI(dir)
	for _, tc := range []struct {
		accept     string
		compressed bool
	}{{"gzip", true}, {"gzip;q=0", false}, {"gzip;q=0.5", true}, {"br", false}} {
		r := httptest.NewRequest("GET", "https://gateway/assets/app.js", nil)
		r.Header.Set("Accept-Encoding", tc.accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || (w.Header().Get("Content-Encoding") == "gzip") != tc.compressed {
			t.Fatal("incorrect gzip negotiation", tc.accept, w.Code)
		}
		if tc.compressed {
			reader, err := gzip.NewReader(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(reader)
			reader.Close()
			if !bytes.Equal(got, data) {
				t.Fatal("corrupted compressed asset")
			}
		}
	}
}
