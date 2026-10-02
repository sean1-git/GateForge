package explain

import (
	"encoding/json"
	"gateforge/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOfflinePreviewUsesPrivateRoutingAndHistoricalAuth(t *testing.T) {
	var store Store
	request := httptest.NewRequest("GET", "https://gateway/orders/private-customer?secret=never-store", nil)
	request.Header.Set("X-API-Key", "do-not-retain")
	store.Serve(httptest.NewRecorder(), request, 1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Route(r.Context(), "/orders")
		Authentication(r.Context(), true, "valid", []string{"/orders"})
		Add(r.Context(), "authentication", "accepted", "accepted")
		w.WriteHeader(200)
	}))
	rows := store.Preview([]config.Route{{Prefix: "/orders/private-customer", Auth: "api_key"}})
	if len(rows) != 1 || !rows[0].RoutingChanged || !rows[0].AuthorizationChanged || rows[0].AfterAuth != "denied" {
		t.Fatalf("unexpected preview: %+v", rows)
	}
	data, _ := json.Marshal(store.Snapshot())
	for _, secret := range []string{"private-customer", "never-store", "do-not-retain", "grants", "paths", "sample"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("private evidence serialized")
		}
	}
	if store.Preview([]config.Route{{Prefix: "/order", Auth: "public"}})[0].AfterRoute != "" {
		t.Fatal("slash boundary mismatch")
	}
	if store.Preview([]config.Route{{Prefix: "/orders", Auth: "api_key"}})[0].AfterAuth != "allowed" {
		t.Fatal("existing permission not preserved")
	}
}
func TestPreviewDoesNotGuessUnvalidatedCredentials(t *testing.T) {
	var store Store
	r := httptest.NewRequest("GET", "https://gateway/public", nil)
	r.Header.Set("X-API-Key", "unvalidated")
	store.Serve(httptest.NewRecorder(), r, 1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Route(r.Context(), "/public")
		Add(r.Context(), "authentication", "skipped", "public")
	}))
	row := store.Preview([]config.Route{{Prefix: "/public", Auth: "api_key"}})[0]
	if !row.Unknown || row.AfterAuth != "unknown" || row.AuthorizationChanged {
		t.Fatal("guessed credential validity", row)
	}
}
