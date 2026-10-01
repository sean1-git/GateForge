package adminauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const GoogleIssuer = "https://accounts.google.com"
const loginCookie = "__Host-gateforge_login"

type LoginFlow struct {
	StateHash, BrowserHash, Nonce, Verifier string
	ExpiresAt                               time.Time
}
type FlowStore interface {
	SaveAdminLogin(context.Context, LoginFlow) error
	ConsumeAdminLogin(context.Context, string, string) (LoginFlow, error)
}
type Google struct {
	Sessions *Sessions
	Flows    FlowStore
	OAuth    oauth2.Config
	Verifier *oidc.IDTokenVerifier
	Client   *http.Client
	Logger   *slog.Logger
}

func NewGoogle(ctx context.Context, sessions *Sessions, flows FlowStore, clientID, secret string, logger *slog.Logger) (*Google, error) {
	origin, err := CanonicalOrigin(sessions.Origin)
	if err != nil {
		return nil, err
	}
	if clientID == "" || secret == "" || len(sessions.AllowedEmails) == 0 {
		return nil, errors.New("Google sign-in requires client credentials and an administrator allowlist")
	}
	sessions.Origin = origin
	client := &http.Client{Timeout: 8 * time.Second}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), GoogleIssuer)
	if err != nil {
		return nil, errors.New("Google sign-in discovery unavailable")
	}
	return &Google{Sessions: sessions, Flows: flows, Client: client, Logger: logger,
		OAuth:    oauth2.Config{ClientID: clientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: origin + "/admin/auth/callback", Scopes: []string{oidc.ScopeOpenID, "email"}},
		Verifier: provider.Verifier(&oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{"RS256"}})}, nil
}

func (g *Google) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.URL.Path == "/admin/auth/login" {
		g.begin(ctx, w, r)
		return
	}
	if r.URL.Path == "/admin/auth/callback" {
		g.callback(ctx, w, r)
		return
	}
	http.NotFound(w, r)
}

func (g *Google) begin(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	state, err := Random()
	if err != nil {
		g.fail(w, r, "randomness unavailable")
		return
	}
	browser, err := Random()
	if err != nil {
		g.fail(w, r, "randomness unavailable")
		return
	}
	nonce, err := Random()
	if err != nil {
		g.fail(w, r, "randomness unavailable")
		return
	}
	verifier, err := Random()
	if err != nil {
		g.fail(w, r, "randomness unavailable")
		return
	}
	flow := LoginFlow{StateHash: Hash(state), BrowserHash: Hash(browser), Nonce: nonce, Verifier: verifier, ExpiresAt: time.Now().Add(5 * time.Minute)}
	if err = g.Flows.SaveAdminLogin(ctx, flow); err != nil {
		g.fail(w, r, "login storage unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: browser, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, g.OAuth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "select_account")), http.StatusFound)
}

func (g *Google) callback(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if len(r.URL.RawQuery) > 8192 || len(q["state"]) != 1 || len(q.Get("state")) != 43 || len(q["code"]) != 1 || q.Get("code") == "" || q.Get("error") != "" {
		g.fail(w, r, "invalid callback")
		return
	}
	var browser string
	for _, c := range r.Cookies() {
		if c.Name == loginCookie {
			if browser != "" || len(c.Value) != 43 {
				g.fail(w, r, "invalid browser binding")
				return
			}
			browser = c.Value
		}
	}
	if browser == "" {
		g.fail(w, r, "missing browser binding")
		return
	}
	flow, err := g.Flows.ConsumeAdminLogin(ctx, Hash(q.Get("state")), Hash(browser))
	if err != nil || !time.Now().Before(flow.ExpiresAt) {
		g.fail(w, r, "invalid or expired login flow")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	token, err := g.OAuth.Exchange(oidc.ClientContext(ctx, g.Client), q.Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		g.fail(w, r, "code exchange failed")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		g.fail(w, r, "identity token missing")
		return
	}
	id, err := g.Verifier.Verify(ctx, raw)
	if err != nil {
		g.fail(w, r, "identity verification failed")
		return
	}
	if subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(flow.Nonce)) != 1 {
		g.fail(w, r, "nonce mismatch")
		return
	}
	var claims struct {
		Email           string `json:"email"`
		Verified        bool   `json:"email_verified"`
		AuthorizedParty string `json:"azp"`
	}
	if id.Claims(&claims) != nil || !claims.Verified || claims.Email == "" || (claims.AuthorizedParty != "" && claims.AuthorizedParty != g.OAuth.ClientID) {
		g.fail(w, r, "unverified identity")
		return
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if err = g.Sessions.Issue(ctx, w, GoogleIssuer, id.Subject, email); err != nil {
		g.fail(w, r, "account not authorized or session storage unavailable")
		return
	}
	if g.Logger != nil {
		g.Logger.Info("administrator signed in", "administrator_id", Hash(GoogleIssuer+"\x00"+id.Subject))
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (g *Google) fail(w http.ResponseWriter, r *http.Request, reason string) {
	if g.Logger != nil {
		g.Logger.Warn("administrator sign-in failed", "reason", reason)
	}
	// Provider errors, codes, credentials and account details never enter the URL or logs.
	http.Redirect(w, r, "/admin/?signin=failed", http.StatusSeeOther)
}
