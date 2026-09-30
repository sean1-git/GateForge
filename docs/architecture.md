# GateForge architecture and behavior

GateForge now has the application features from the project roadmap. Cloud deployment and full Docker execution require their own verification; see `validation.md` for what has actually been run.

```text
Client -- HTTPS --> Go gateway -- HTTP(S) --> healthy service replica
                       |
                       +-- PostgreSQL: versioned routes, hashed API keys
                       +-- Redis: shared quotas, eligible public responses
                       +-- React dashboard: authenticated control and instance metrics
```

## 1. Security

The Go listener terminates TLS 1.2 or newer using `-tls-cert` and `-tls-key`. Certificate files are reloaded at most once a minute for renewal. Local certificates last 30 days and are for development only. The optional loopback-only health listener exposes just `/healthz` and `/readyz`.

Each route declares `auth`: `public`, `api_key`, `jwt`, or `either`. An omitted value retains legacy public routing. `either` accepts one valid credential type, not both at once. Protected routes reject unencrypted requests. The explicit `GATEFORGE_TLS_OFFLOADED=true` setting is only for deployment behind an ingress that enforces HTTPS and prevents direct access to the gateway listener; incoming forwarding headers alone never enable this behavior.

API keys contain 256 random bits and are shown once at creation. PostgreSQL stores their SHA-256 hashes, route permissions, expiry and revocation status. Permissions name exact configured route prefixes: permission for `/users` does not grant a separate `/users/admin` route. Every authenticated request checks PostgreSQL; a database failure returns 503 rather than accepting a key from stale local state.

JWT support uses a configured RSA public key (minimum 2048 bits), RS256 only, expected issuer, expected audience, expiration, optional not-before/issued-at validation, and a nonempty subject. A route can also require a space-delimited `scope` claim. GateForge validates tokens issued by your identity provider; it does not implement user login or mint production JWTs. Public keys are loaded at startup. Automatic JWKS discovery/rotation is not implemented; replace the key and restart when rotating it.

Validated API credentials are removed before forwarding. An internal, non-personal principal identifier is placed in `X-GateForge-Subject`; clients cannot supply that trusted identity themselves. Backend services must still apply their own object-level authorization (for example, whether a user can access order 42).

Paths containing dot segments, duplicate separators, backslashes, control characters, or ambiguous double encoding are rejected before routing. This prevents normalization from moving a request across a public/protected route boundary. Encoded slashes count as path separators for matching. Normal escaped path data is forwarded unchanged.

The dashboard and admin API use a separate 32-character-minimum `GATEFORGE_ADMIN_TOKEN`. Generate it randomly and rotate it through your secret configuration. It is never an API key, and ordinary JWT/API-key holders cannot administer GateForge. The React app stores this token only in memory. No cookies or cross-origin API access are enabled. Route edits use optimistic revision checks; stale writes return 409. Database-backed instances refresh valid configuration every five seconds and retain the last valid snapshot during database outages.

## 2. Redis

Rate limiting is a per-route, per-identity fixed-window counter. Authenticated requests use a key ID or hashed JWT issuer/subject; public requests use the direct peer IP. Client-supplied forwarding headers do not control identity. A Lua script atomically increments and sets the counter expiry, and replicas share the same counter. Requests over the limit return 429 with `Retry-After`; Redis failures return 503 on routes with rate limiting. Fixed windows can allow bursts around a window boundary. Behind another proxy, anonymous users sharing its peer address share a quota; use authenticated identities for per-user limits.

Caching is explicitly enabled per public route and only applies to GET without a body, cookies, credentials, conditional/range requests, upgrade headers, or request cache-bypass directives. Only successful, non-streaming responses marked `Cache-Control: public` with positive `max-age`/`s-maxage` are stored. `private`, `no-store`, `no-cache`, cookies, trailers, and unsupported `Vary` fields prevent storage. `Vary` supports `Accept` and `Accept-Encoding`; all other variations bypass storage. Response bodies are capped at 1 MiB (configurable downward), and the TTL is capped by both the route and upstream policy. Entries distinguish route configuration, host, path, query, Accept, and Accept-Encoding. Cache hits report `X-GateForge-Cache: HIT` and `Age`.

Shared caching of authenticated responses is deliberately unsupported. Cache Redis failures bypass caching; a rate limit on the same route still fails closed. Freshness is TTL-based; mutations do not invalidate cached entries early. Use caching only for resources whose published freshness policy permits this. Redis runs with `noeviction` in the sample stack so cached data cannot silently evict quota counters; monitor memory and size it for traffic.

## 3. Reliability

Each route has either `upstream` or `upstreams`. A process-local round-robin counter distributes requests across healthy candidates. Optional active health checks start immediately and repeat at the configured interval; any 2xx is healthy. Probes do not follow redirects or carry credentials. A backend with configured checks receives no traffic until a check succeeds. Failed attempts mark it unavailable until a successful probe restores it. Without checks, a backend is considered available for selection; the dashboard identifies this distinction.

Only bodyless GET/HEAD requests without upgrades are eligible for retries. A route permits at most three retries, bounded by distinct available backends, on connection failures or 502/503/504. POST, PUT, PATCH and DELETE are not automatically replayed. Non-retryable requests use a transport without connection reuse to prevent implicit replay by Go's transport. Retries remain within the selected route; they do not fall through to a less-specific route.

Dial timeout is two seconds and response-header timeout is ten seconds. `timeout_ms` optionally sets a total route request deadline (including response streaming), up to 120 seconds. A zero value preserves the original streaming behavior without a total deadline. Exhausted deadlines return 504 before response headers are committed; later failures terminate the stream. No healthy backend returns 503. Other proxy connection failures return 502.

`/healthz` checks process liveness. `/readyz` checks configured dependencies and that every route has an available backend. Shutdown drains normal HTTP requests for ten seconds. Upgraded connections such as WebSockets are not drained.

## 4. Analytics and React

The dashboard at `/admin/` shows request counts, mean latency, 4xx/5xx responses, cache hits and backend status. It edits validated route JSON, issues scoped API keys and revokes keys. JSON metrics and Prometheus counters/histograms use route prefixes, not raw paths, query strings, credentials or JWT claims. Cardinality is bounded, and requests to operational endpoints are excluded from traffic metrics.

Metrics are per-instance, kept in memory, and reset on restart. The dashboard is a view of the instance serving it, not a durable cross-instance analytics database. Scrape the authenticated `/metrics` endpoint with Prometheus or a compatible collector for durable, aggregated reporting. That collector and long-term analytics storage are not deployed by this repository.

## 5. Containers and GCP

The Docker image builds the React assets and Go binaries, then runs the gateway as a non-root user. Compose provides persistent PostgreSQL/Redis volumes, internal-only backends, health checks, two optional gateway replicas, and a development TLS certificate. Only gateway ports are published, on loopback for local use.

The GCP scripts prepare a single Compute Engine VM with a static IP, an isolated VPC, HTTPS ingress, IAP-only SSH, deletion protection, and a retained boot disk. This is an initial deployment, not a highly available cloud topology. Database and Redis persistence survive container recreation on that VM; a host/disk failure still requires backup/restore. No managed Cloud SQL, Memorystore, multi-zone failover, certificate issuance, or automatic billing cap is claimed.

Primary references: [JWT validation options](https://golang-jwt.github.io/jwt/usage/parse/), [pgx PostgreSQL driver](https://github.com/jackc/pgx), [atomic Redis rate limiting](https://redis.io/docs/latest/develop/use-cases/rate-limiter/go/), [Docker on Debian](https://docs.docker.com/engine/install/debian/), [GCP IAP forwarding](https://docs.cloud.google.com/iap/docs/using-tcp-forwarding).
