package app

import (
	"encoding/json"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserPoliciesDoNotExposeOrReplaceBackendAddresses(t *testing.T) {
	secretHost := "http://private-backend.internal:9234/private-path"
	s := &fakeStore{snapshot: storage.Snapshot{Revision: 1, Routes: []config.Route{{Prefix: "/items", Upstream: secretHost}}}}
	a, err := New(s.snapshot, s, gateway.Options{}, nil, strings.Repeat("a", 40), true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://gateway"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40))
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/admin/api/policies", "/admin/api/backends"} {
		w := call("GET", path, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "private-backend") || strings.Contains(w.Body.String(), "9234") {
			t.Fatal("browser received backend address")
		}
	}
	var input policySnapshot
	json.Unmarshal(call("GET", "/admin/api/policies", "").Body.Bytes(), &input)
	input.Routes[0].TimeoutMS = 1234
	body, _ := json.Marshal(input)
	w := call("PUT", "/admin/api/policies", string(body))
	if w.Code != 200 || s.snapshot.Routes[0].Upstream != secretHost || s.snapshot.Routes[0].TimeoutMS != 1234 {
		t.Fatal("policy update lost topology", w.Code)
	}
	if call("PUT", "/admin/api/policies", string(body)).Code != 409 {
		t.Fatal("stale policy accepted")
	}
	if call("PUT", "/admin/api/policies", `{"revision":2,"routes":[{"prefix":"/items","upstream":"http://attacker"}]}`).Code != 400 {
		t.Fatal("browser changed backend destination")
	}
}
func TestIngressLimitsInvalidCredentialsBeforeAuth(t *testing.T) {
	a := requestApp(t, []config.Route{{Prefix: "/orders", Upstream: "http://127.0.0.1:1", Auth: "api_key"}}, gateway.Options{Auth: &security.Authenticator{Keys: explainKeys{}}})
	a.Ingress = security.NewIngress(2, 2)
	for _, status := range []int{401, 401, 429} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", "https://gateway/orders", nil))
		if w.Code != status {
			t.Fatal(w.Code)
		}
	}
}
func TestStaticUIBlocksConfidentialFilesAndSourceMaps(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "assets"), 0700)
	for _, file := range []string{".env", "config.json", "assets/app.js.map", "assets/.env", "assets/secrets.txt"} {
		os.WriteFile(filepath.Join(dir, file), []byte("private-value"), 0600)
	}
	h := StaticUI(dir)
	for _, path := range []string{"/.env", "/config.json", "/assets/", "/assets/app.js.map", "/assets/.env", "/assets/secrets.txt"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || strings.Contains(w.Body.String(), "private-value") {
			t.Fatal("confidential static file served", path)
		}
	}
}
func TestPublicResponseHidesBackendDiagnostics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "private-server")
		w.Header().Set("X-API-Key", "private-key")
		w.WriteHeader(500)
		w.Write([]byte("postgres://private-password@internal-db/stack-trace"))
	}))
	defer backend.Close()
	a := requestApp(t, []config.Route{{Prefix: "/", Upstream: backend.URL}}, gateway.Options{})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "https://gateway/", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") || w.Header().Get("Server") != "" || w.Header().Get("X-API-Key") != "" {
		t.Fatal("backend diagnostic leak")
	}
	data, _ := json.Marshal(a.Requests.Snapshot())
	if strings.Contains(string(data), strings.TrimPrefix(backend.URL, "http://")) {
		t.Fatal("request explanation contains backend address")
	}
}
