package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredHandler(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer backend.Close()
	configPath := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(configPath, []byte(`{"routes":[{"prefix":"/users","upstream":"`+backend.URL+`"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, configPath, path string
		upstreamSet            bool
	}{
		{"file", configPath, "/users/42", false},
		{"legacy default", "", "/anything", false},
		{"legacy explicit", "", "/anything", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, err := configuredHandler(tc.configPath, backend.URL, tc.upstreamSet, nil)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d; want 202", response.Code)
			}
		})
	}
	if _, err := configuredHandler(configPath, backend.URL, true, nil); err == nil {
		t.Error("accepted -config with explicit -upstream")
	}
	if _, err := configuredHandler(filepath.Join(t.TempDir(), "missing.json"), "", false, nil); err == nil {
		t.Error("accepted missing config file")
	}
}

func TestConfiguredHandlerRejectsBadFiles(t *testing.T) {
	for _, data := range []string{
		``, `null`, `{}`, `{"routes":[]}`, `{"routes":null}`, `{"routes":[`,
		`{"route":[]}`, `{"routes":[{"prefix":"/users","upsteam":"http://localhost:9001"}]}`,
		`{"routes":[{"prefix":"/users"}]}`,
		`{"routes":[{"prefix":"/users","upstream":"http://localhost:9001"}]} {}`,
		`{"routes":[{"prefix":"/users","upstream":"http://localhost:9001"}]} garbage`,
	} {
		t.Run(data, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "routes.json")
			if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := configuredHandler(configPath, "", false, nil); err == nil {
				t.Error("accepted invalid config")
			}
		})
	}
}
