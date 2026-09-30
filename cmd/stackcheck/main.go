// stackcheck exercises the supplied disposable Compose stack without printing credentials.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	first := flag.String("url", "https://localhost:8443", "first gateway")
	second := flag.String("second-url", "https://localhost:8444", "second gateway")
	ca := flag.String("ca", ".local/certs/cert.pem", "CA PEM file")
	phase := flag.String("phase", "normal", "normal, redis-down, or database-down")
	flag.Parse()
	data, err := os.ReadFile(*ca)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return fmt.Errorf("invalid CA")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	call := func(base, method, path, apiKey string, payload any) (int, http.Header, []byte, error) {
		var body io.Reader
		if payload != nil {
			b, err := json.Marshal(payload)
			if err != nil {
				return 0, nil, nil, err
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Host = "gateway.test"
		if apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
		if len(path) >= 7 && path[:7] == "/admin/" {
			req.Header.Set("Authorization", "Bearer "+os.Getenv("GATEFORGE_ADMIN_TOKEN"))
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b, err
	}
	if *phase == "database-down" {
		status, _, _, err := call(*first, "GET", "/users/42", "gf_test_invalid_key", nil)
		if err != nil || status != 503 {
			return fmt.Errorf("database outage: status=%d error=%v", status, err)
		}
		fmt.Println("Database outage fails closed: passed")
		return nil
	}
	status, _, body, err := call(*first, "POST", "/admin/api/keys", "", map[string]any{"name": "stack-validation", "prefixes": []string{"/users"}, "expires_at": time.Now().Add(time.Hour)})
	if err != nil || status != 201 {
		return fmt.Errorf("create test key: status=%d error=%v", status, err)
	}
	var issued struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	if err = json.Unmarshal(body, &issued); err != nil {
		return err
	}
	defer func() { _, _, _, _ = call(*first, "DELETE", "/admin/api/keys/"+issued.Key.ID, "", nil) }()
	if *phase == "redis-down" {
		status, _, _, err = call(*first, "GET", "/users/42", issued.Secret, nil)
		if err != nil || status != 503 {
			return fmt.Errorf("Redis outage: status=%d error=%v", status, err)
		}
		fmt.Println("Redis outage fails closed: passed")
		return nil
	}
	if *phase != "normal" {
		return fmt.Errorf("unknown phase")
	}
	for _, base := range []string{*first, *second} {
		status, _, _, err = call(base, "GET", "/users/42", "", nil)
		if err != nil || status != 401 {
			return fmt.Errorf("missing key: status=%d error=%v", status, err)
		}
	}
	for i := 0; i < 61; i++ {
		base := *first
		if i%2 == 1 {
			base = *second
		}
		status, _, _, err = call(base, "GET", "/users/42", issued.Secret, nil)
		want := 200
		if i == 60 {
			want = 429
		}
		if err != nil || status != want {
			return fmt.Errorf("shared quota request %d: status=%d want=%d error=%v", i+1, status, want, err)
		}
	}
	path := fmt.Sprintf("/catalog/items?stackcheck=%d", time.Now().UnixNano())
	for i, base := range []string{*first, *second} {
		status, headers, _, err := call(base, "GET", path, "", nil)
		if err != nil || status != 200 {
			return fmt.Errorf("cache request failed: %d %v", status, err)
		}
		if i == 1 && headers.Get("X-GateForge-Cache") != "HIT" {
			return fmt.Errorf("cache entry was not shared")
		}
	}
	status, _, _, err = call(*first, "DELETE", "/admin/api/keys/"+issued.Key.ID, "", nil)
	if err != nil || status != 204 {
		return fmt.Errorf("revoke test key failed")
	}
	status, _, _, err = call(*second, "GET", "/users/42", issued.Secret, nil)
	if err != nil || status != 401 {
		return fmt.Errorf("revocation not enforced")
	}
	fmt.Println("TLS, authentication, distributed quota, shared cache, and revocation: passed")
	return nil
}
