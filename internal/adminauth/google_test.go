package adminauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type memoryFlows struct{ rows map[string]LoginFlow }

func (m *memoryFlows) SaveAdminLogin(_ context.Context, f LoginFlow) error {
	m.rows[f.StateHash] = f
	return nil
}
func (m *memoryFlows) ConsumeAdminLogin(_ context.Context, state, browser string) (LoginFlow, error) {
	f, ok := m.rows[state]
	if !ok || f.BrowserHash != browser || !time.Now().Before(f.ExpiresAt) {
		return LoginFlow{}, ErrUnauthorized
	}
	delete(m.rows, state)
	return f, nil
}

func TestGoogleCodeFlowVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "replay", "wrong-browser", "wrong-state", "wrong-nonce", "wrong-audience", "wrong-issuer", "unverified-email", "unapproved-email", "expired-token", "wrong-authorized-party", "forged-signature", "missing-subject"} {
		t.Run(scenario, func(t *testing.T) {
			flows := &memoryFlows{rows: map[string]LoginFlow{}}
			store := &memorySessions{data: map[string]Session{}}
			sessions := &Sessions{Store: store, Origin: "https://gateway.example", AllowedEmails: map[string]bool{"owner@example.com": true}}
			var identityToken string
			var expectedVerifier string
			exchanges := 0
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				exchanges++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("code_verifier") != expectedVerifier || r.Form.Get("redirect_uri") != "https://gateway.example/admin/auth/callback" || r.Form.Get("code") != "test-code" {
					t.Error("PKCE, redirect or code missing from exchange")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"access_token": "disposable-test-access", "token_type": "Bearer", "id_token": identityToken})
			}))
			defer provider.Close()
			g := &Google{Sessions: sessions, Flows: flows, Client: provider.Client(), OAuth: oauth2.Config{ClientID: "test-client", ClientSecret: "test-only-client-secret", RedirectURL: sessions.Origin + "/admin/auth/callback", Scopes: []string{"openid", "email"}, Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: provider.URL, AuthStyle: oauth2.AuthStyleInParams}}, Verifier: oidc.NewVerifier(GoogleIssuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "test-client", SupportedSigningAlgs: []string{"RS256"}})}
			start := httptest.NewRecorder()
			g.ServeHTTP(start, httptest.NewRequest("GET", sessions.Origin+"/admin/auth/login", nil))
			if start.Code != 302 {
				t.Fatal("login redirect failed")
			}
			location, _ := url.Parse(start.Header().Get("Location"))
			query := location.Query()
			if query.Get("code_challenge_method") != "S256" || query.Get("nonce") == "" || query.Get("state") == "" || query.Get("scope") != "openid email" {
				t.Fatal("missing authorization protections")
			}
			flow := flows.rows[Hash(query.Get("state"))]
			expectedVerifier = flow.Verifier
			if query.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(expectedVerifier) {
				t.Fatal("incorrect PKCE challenge")
			}
			claims := jwt.MapClaims{"iss": GoogleIssuer, "aud": "test-client", "sub": "google-subject", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": flow.Nonce, "email": "owner@example.com", "email_verified": true, "azp": "test-client"}
			switch scenario {
			case "wrong-nonce":
				claims["nonce"] = "other"
			case "wrong-audience":
				claims["aud"] = "service-api"
			case "wrong-issuer":
				claims["iss"] = "https://attacker.example"
			case "unverified-email":
				claims["email_verified"] = false
			case "unapproved-email":
				claims["email"] = "stranger@example.com"
			case "expired-token":
				claims["exp"] = time.Now().Add(-time.Minute).Unix()
			case "wrong-authorized-party":
				claims["azp"] = "other-client"
			case "missing-subject":
				delete(claims, "sub")
			}
			identityToken, err = jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "forged-signature" {
				pieces := strings.Split(identityToken, ".")
				pieces[2] = "AAAA"
				identityToken = strings.Join(pieces, ".")
			}
			state := query.Get("state")
			if scenario == "wrong-state" {
				state, _ = Random()
			}
			callback := httptest.NewRequest("GET", sessions.Origin+"/admin/auth/callback?state="+state+"&code=test-code", nil)
			cookie := start.Result().Cookies()[0]
			if scenario == "wrong-browser" {
				cookie.Value, _ = Random()
			}
			callback.AddCookie(cookie)
			result := httptest.NewRecorder()
			g.ServeHTTP(result, callback)
			if scenario == "success" || scenario == "replay" {
				if result.Header().Get("Location") != "/admin/" || len(store.data) != 1 {
					t.Fatal("verified approved identity denied")
				}
				if scenario == "replay" {
					second := httptest.NewRecorder()
					g.ServeHTTP(second, callback)
					if second.Header().Get("Location") != "/admin/?signin=failed" || exchanges != 1 || len(store.data) != 1 {
						t.Fatal("callback replay accepted")
					}
				}
			} else if result.Header().Get("Location") != "/admin/?signin=failed" || len(store.data) != 0 {
				t.Fatalf("invalid sign-in accepted: %s", scenario)
			}
		})
	}
}
