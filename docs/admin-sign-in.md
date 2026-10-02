# Administrator authentication and sessions

GateForge supports a secure token-to-session login and Google OpenID Connect for individual administrators. Select the mode explicitly; installing the code alone does not change an existing deployment.

## Secure administrator-token login

Set `GATEFORGE_ADMIN_AUTH=token_session`, `GATEFORGE_PUBLIC_URL` to the exact HTTPS origin, and a cryptographically random `GATEFORGE_ADMIN_TOKEN` of at least 32 characters. PostgreSQL and Redis are required; invalid configuration fails startup. The Cloud Run template selects this mode and requires `__PUBLIC_URL__` to be rendered. Local Compose retains legacy mode for its existing CLI validation tools; enable this mode explicitly when using browser sessions locally.

The browser posts the token once to `/admin/auth/token` in a bounded JSON body over HTTPS. It clears the input immediately, never stores the token in browser storage, and subsequently authenticates with a `Secure`, `HttpOnly`, host-only cookie. Only the session-bound CSRF value remains readable by JavaScript. The server uses constant-time credential comparison. Login rejects foreign/missing Origins, query-string credentials and bearer headers.

Redis allows **10 login requests per minute globally**, shared across instances and client addresses, returning 429 with `Retry-After`. A Redis outage blocks login. This intentionally conservative limit can temporarily deny new logins during abuse; existing sessions remain usable. Sessions expire after eight hours, are bounded to eight per token generation, and are revoked server-side on logout. Rotating the configured token and retiring old revisions denies sessions issued by the previous token generation. Service API keys/JWTs and the old administrator bearer header cannot authorize `/admin/api/*`; every state-changing administrator request requires both the pinned Origin and a valid CSRF header.

This mode still represents one shared administrator identity, not individual accounts or MFA. Use the Google mode below for individual allowlisted access. The token remains usable for the read-only `/metrics` collector endpoint. Token and session contents are not logged; login failures and successful administrator actions are logged without credentials.

HTTPS encrypts credentials in transit. Session secrets and service API keys are stored as hashes, not reversible encrypted values. The root administrator token is supplied through Secret Manager in Cloud Run. The Cloud SQL connector encrypts its remote database connection, and Redis uses certificate-verified TLS. Local Compose database/cache traffic stays on its local container network without TLS; do not reuse that configuration for remote endpoints. Encryption does not replace authorization: holding the administrator token grants administrator access.

Use the canonical public URL for login and writes. Sessions do not transfer between the two Cloud Run hostnames. To sign in from a script, POST JSON with the exact `Origin`, retain the response cookie, GET `/admin/auth/session` to obtain `csrf_token`, and send that value as `X-GateForge-CSRF` with the cookie and Origin for writes. Never put the token in URLs or shell command arguments.

## Individual Google access model

- `GATEFORGE_ADMIN_AUTH=google` enables individual browser sessions and rejects the old administrator bearer token on `/admin/api/*`.
- An exact email allowlist controls who can sign in. There is no public registration. Each allowed account has full administrator access; read-only roles are not implemented.
- Google validates credentials. GateForge verifies the ID token's signature, issuer, audience, expiry, nonce, authorized party, verified email, and nonempty subject. It never stores Google passwords, access tokens or refresh tokens.
- On first sign-in, PostgreSQL binds the approved email to Google's stable subject identifier. A different subject cannot silently take over the email. Changing that binding requires an explicit operational review.
- Sessions last eight hours without sliding renewal. PostgreSQL stores only session-secret hashes. Each account has at most eight active sessions; signing in again evicts the oldest when necessary.
- Browser cookies are `Secure`, `HttpOnly`, host-only, `SameSite=Lax`, and use the `__Host-` prefix. Refreshing the dashboard restores a valid session. Signing out deletes it server-side; a failed deletion is reported instead of claiming success.
- Every write requires the configured exact Origin and a session-bound CSRF header. The frontend holds the CSRF token in memory; it cannot read the session cookie. Neither localStorage nor sessionStorage contains credentials.
- Administrator session/login cookies and the CSRF header are stripped before proxying service traffic; unrelated application cookies are preserved.
- Backend responses cannot overwrite reserved administrator cookies or clear browser site data. Proxied responses receive a restrictive sandbox CSP because service APIs and administration share an origin. JSON/API clients are unaffected, but executing upstream HTML applications through this origin is intentionally unsupported; put such applications on a separate origin.
- Removing an email from configuration denies its existing sessions after the updated configuration is deployed. Database outages deny administrator access.
- Login flows use state, browser binding, nonce, PKCE S256, five-minute expiry, and atomic one-time consumption. Redis limits login/callback requests to 30 per minute per direct peer across instances; a Redis outage blocks sign-in. Users behind the Cloud Run proxy can share this quota.

The existing `GATEFORGE_ADMIN_TOKEN` remains valid only for `/metrics` when Google mode is active, for compatibility with machine collectors. It cannot create keys or change routes. Rotate or unset it if no collector needs it. Service API keys and service JWTs never grant administrator access.

## Google OAuth configuration

In the existing Google Cloud project, configure a Google Auth Platform application and a **Web application** OAuth client. Request only `openid` and `email`. During testing, add the approved administrator as a test user. No Drive, Gmail, Cloud-management, or offline-access scopes are needed.

Register this exact authorized redirect URI for the current deployment:

```text
https://gateforge-ess7ryi2la-ew.a.run.app/admin/auth/callback
```

The client ID identifies the application; the client secret must remain in Secret Manager or an ignored local environment file. Do not paste the client secret into source code, a command-line argument, GitHub, or the browser dashboard. This server-rendered authorization-code redirect flow does not require Google's JavaScript widget or a relaxed Content Security Policy.

Set these values on the **gateway container**, preserving its existing database, Redis, network and secret settings:

| Variable | Value |
| --- | --- |
| `GATEFORGE_ADMIN_AUTH` | `google` |
| `GATEFORGE_PUBLIC_URL` | `https://gateforge-ess7ryi2la-ew.a.run.app` |
| `GATEFORGE_ADMIN_EMAILS` | Comma-separated approved email addresses; no wildcard/domain access |
| `GATEFORGE_GOOGLE_CLIENT_ID` | Web application's OAuth client ID |
| `GATEFORGE_GOOGLE_CLIENT_SECRET` | Secret Manager reference, not a literal in a committed manifest |

PostgreSQL, Redis, and HTTPS are required. Missing/invalid configuration fails startup; it never silently enables shared-token fallback. The alternate Cloud Run URL should not be used for administrator sign-in: authorization callbacks and write Origins are deliberately pinned to one canonical origin.

For local development, register `https://localhost:8443/admin/auth/callback` on a separate development OAuth client and set the public URL to `https://localhost:8443`. Trust the development certificate normally. Leaving `GATEFORGE_ADMIN_AUTH` unset, or setting it to `token`, preserves the existing local token workflow.

## Rollout and recovery

1. Create the OAuth client, confirm the approved account, and place its secret in Secret Manager. Grant the existing runtime service account access to that secret.
2. Build and test the release, then deploy the image with all five settings together. Keep automatic GitHub-to-cloud deployment disabled.
3. Verify the approved Google account can sign in, a different account is rejected, writes require CSRF, and sign-out invalidates the session. Check that a shared administrator token receives 401 from `/admin/api/config` in Google mode.
4. App logs record successful sign-ins, sign-outs and authenticated writes using a stable hashed administrator ID, method, path and status. They do not log cookies, ID tokens, authorization codes, provider error bodies or URL query strings. The identity table maps the ID to an approved email. Successful configuration/key/session changes also commit a transactional audit record; see [operations](operations.md) for its append-only protection and privileged-owner limitation.
5. Configure platform/reverse-proxy access logs to exclude or redact OAuth callback query strings: Cloud Run's own request logs are independent of the app logger. Authorization codes are short-lived and one-use but should not be retained unnecessarily.

Changing authentication configuration requires a deliberate deployment. Before removing token mode from an existing live service, test the Google client configuration. The old revision remains a recovery option if OAuth configuration is incorrect. After a database restore, delete all rows from `gateforge_admin_sessions` and `gateforge_admin_logins` in the restored database before allowing traffic, so backed-up sessions cannot be resurrected; keep `gateforge_admins` bindings.

## Validation

Tests cover cookie flags, session expiry, server-side logout, removed allowlist entries, database outages, CSRF and Origin checks, legacy-token rejection, callback replay, wrong browser/state/nonce, expired or forged ID tokens, incorrect issuer/audience/authorized party, unverified or unapproved email, and missing subject. PostgreSQL integration tests cover bounded sessions, subject binding, expiry and 20 simultaneous consumers of one login flow (exactly one succeeds). They run in disposable schemas, never against live data.

References: [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect), [go-oidc](https://github.com/coreos/go-oidc), [Go OAuth2 PKCE](https://pkg.go.dev/golang.org/x/oauth2#S256ChallengeOption).

The login screen uses Google's pre-approved light button image, served locally from `web/src/assets/google-sign-in.png`. Source: [Google branding guidelines](https://developers.google.com/identity/branding-guidelines), [original asset](https://developers.google.com/static/identity/gsi/web/images/standard-button-white.png). Google branding belongs to Google; the image is used only for its intended sign-in action.
