package explain

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"gateforge/internal/config"
	"net/http"
	"strings"
)

// Process-keyed fingerprints let previews compare routing and grants without
// retaining raw paths or permissions. Keeping them private and process-local
// prevents the API from exposing reusable identifiers for sensitive inputs.
type evidence struct {
	paths, grants                             [][32]byte
	pathKnown, grantsKnown, secure, ambiguous bool
	credential, validation                    string
}

func (s *Store) fingerprint(value string) [32]byte {
	h := hmac.New(sha256.New, s.salt[:])
	h.Write([]byte(value))
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
func (s *Store) capture(t *timeline, r *http.Request) {
	s.seed.Do(func() { _, err := rand.Read(s.salt[:]); s.saltOK = err == nil })
	t.fingerprint = s.fingerprint
	e := &t.record.sample
	e.credential = "none"
	if r.Header.Get("X-API-Key") != "" {
		e.credential = "api_key"
	}
	if r.Header.Get("Authorization") != "" {
		if e.credential != "none" {
			e.ambiguous = true
		}
		e.credential = "jwt"
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			e.validation = "invalid"
		}
	}
	e.ambiguous = e.ambiguous || len(r.Header.Values("X-API-Key")) > 1 || len(r.Header.Values("Authorization")) > 1
	e.secure = r.TLS != nil
	if !s.saltOK || len(r.URL.Path) > 4096 {
		return
	}
	e.paths = append(e.paths, s.fingerprint("path:/"))
	p := strings.TrimSuffix(r.URL.Path, "/")
	for i := 1; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if len(e.paths) >= 64 {
				e.paths = nil
				return
			}
			e.paths = append(e.paths, s.fingerprint("path:"+p[:i]))
		}
	}
	e.pathKnown = true
}

// Authentication reuses the gateway's completed validation so recording evidence
// cannot add credential checks or change the request's authorization outcome.
// Previews describe that observation, not the caller's current permissions.
func Authentication(ctx context.Context, secure bool, validation string, grants []string) {
	t, _ := ctx.Value(key{}).(*timeline)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := &t.record.sample
	e.secure = secure
	if validation != "" {
		e.validation = validation
	}
	if validation == "valid" {
		e.grantsKnown = len(grants) <= 256
		for i, g := range grants {
			if i >= 256 {
				break
			}
			e.grants = append(e.grants, t.fingerprint("grant:"+g))
		}
	}
}

type PreviewRow struct {
	ID                   string `json:"id"`
	Method               string `json:"method"`
	BeforeRoute          string `json:"before_route"`
	AfterRoute           string `json:"after_route"`
	BeforeAuth           string `json:"before_auth"`
	AfterAuth            string `json:"after_auth"`
	RoutingChanged       bool   `json:"routing_changed"`
	AuthorizationChanged bool   `json:"authorization_changed"`
	Unknown              bool   `json:"unknown"`
	Reason               string `json:"reason"`
}

func contains(values [][32]byte, want [32]byte) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func (s *Store) Preview(routes []config.Route) []PreviewRow {
	rows := []PreviewRow{}
	for _, record := range s.Snapshot() {
		e := record.sample
		row := PreviewRow{ID: record.ID, Method: record.Method, BeforeRoute: record.Route, BeforeAuth: "unknown", AfterAuth: "unknown"}
		if record.Route == "" {
			row.BeforeAuth = "not_reached"
		}
		for _, event := range record.Events {
			if event.Stage == "authentication" {
				switch event.Outcome {
				case "accepted", "skipped":
					row.BeforeAuth = "allowed"
				case "rejected":
					row.BeforeAuth = "denied"
					if event.Reason == "authentication unavailable" {
						row.BeforeAuth = "unknown"
					}
				}
			}
		}
		var matched *config.Route
		if e.pathKnown {
			for i := range routes {
				route := &routes[i]
				if contains(e.paths, s.fingerprint("path:"+route.Prefix)) && (matched == nil || len(route.Prefix) > len(matched.Prefix)) {
					matched = route
				}
			}
		}
		if !e.pathKnown {
			row.Unknown = true
			row.Reason = "Routing evidence exceeded the private sample bound."
		} else if matched == nil {
			row.AfterAuth = "not_reached"
			row.Reason = "No proposed route matches."
		} else {
			row.AfterRoute = matched.Prefix
			mode := matched.Auth
			switch {
			case mode == "" || mode == "public":
				row.AfterAuth = "allowed"
				row.Reason = "The proposed route is public."
			case !e.secure || e.ambiguous || e.credential == "none":
				row.AfterAuth = "denied"
				row.Reason = "HTTPS and one compatible credential are required."
			case mode != "either" && mode != e.credential:
				row.AfterAuth = "denied"
				row.Reason = "The credential type does not satisfy the proposed policy."
			case e.validation == "invalid":
				row.AfterAuth = "denied"
				row.Reason = "The credential failed validation when sampled."
			case e.validation != "valid":
				row.Unknown = true
				row.Reason = "This credential was not validated successfully in the original request."
			default:
				grant := matched.Prefix
				if e.credential == "jwt" {
					grant = matched.Scope
				}
				if (e.credential == "jwt" && grant == "") || contains(e.grants, s.fingerprint("grant:"+grant)) {
					row.AfterAuth = "allowed"
					row.Reason = "Recorded validation and permissions satisfy the proposed policy."
				} else if e.grantsKnown {
					row.AfterAuth = "denied"
					row.Reason = "Recorded permissions do not include the proposed route or scope."
				} else {
					row.Unknown = true
					row.Reason = "Permission evidence exceeded the private sample bound."
				}
			}
		}
		row.RoutingChanged = e.pathKnown && row.BeforeRoute != row.AfterRoute
		row.AuthorizationChanged = row.BeforeAuth != "unknown" && row.AfterAuth != "unknown" && row.BeforeAuth != row.AfterAuth
		row.Unknown = row.Unknown || row.BeforeAuth == "unknown" || row.AfterAuth == "unknown"
		rows = append(rows, row)
	}
	return rows
}
