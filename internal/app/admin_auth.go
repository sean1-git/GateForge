package app

import (
	"context"
	"errors"
	"gateforge/internal/adminauth"
	"net/http"
	"time"
)

func adminSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, adminauth.ErrUnauthorized) {
		reply(w, 401, map[string]string{"error": "administrator sign-in required"})
		return
	}
	reply(w, 503, map[string]string{"error": "administrator session storage unavailable"})
}

func (a *App) adminAuthentication(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.TLS == nil && !a.TLSOffloaded {
		reply(w, 426, map[string]string{"error": "HTTPS required"})
		return
	}
	if r.URL.Path == "/admin/auth/config" && r.Method == "GET" {
		mode := "token"
		if a.AdminSessions != nil {
			mode = "google"
			if a.AdminSessions.RequiredIdentity != "" {
				mode = "token_session"
			}
		}
		reply(w, 200, map[string]string{"mode": mode})
		return
	}
	if a.AdminSessions == nil {
		reply(w, 404, map[string]string{"error": "individual sign-in is not configured"})
		return
	}
	if (r.URL.Path == "/admin/auth/login" || r.URL.Path == "/admin/auth/callback" || r.URL.Path == "/admin/auth/token") && a.AdminLogin != nil {
		a.AdminLogin.ServeHTTP(w, r)
		return
	}
	if r.URL.Path != "/admin/auth/session" && r.URL.Path != "/admin/auth/logout" {
		reply(w, 404, map[string]string{"error": "endpoint not found"})
		return
	}
	if (r.URL.Path == "/admin/auth/session" && r.Method != "GET") || (r.URL.Path == "/admin/auth/logout" && r.Method != "POST") {
		reply(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	session, err := a.AdminSessions.Authenticate(ctx, r)
	if err != nil {
		adminSessionError(w, err)
		return
	}
	if r.URL.Path == "/admin/auth/session" {
		reply(w, 200, session)
		return
	}
	if !a.AdminSessions.ValidWrite(r, session) {
		reply(w, 403, map[string]string{"error": "invalid request origin or CSRF token"})
		return
	}
	if err = a.AdminSessions.Revoke(ctx, w, r); err != nil {
		adminSessionError(w, err)
		return
	}
	a.Logger.Info("administrator signed out", "administrator_id", session.Identity.ID)
	w.WriteHeader(http.StatusNoContent)
}
