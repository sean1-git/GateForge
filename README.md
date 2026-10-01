# GateForge | API Gateway & Developer Platform

A Go reverse-proxy gateway that routes requests across services, validates credentials, enforces shared quotas, and exposes a React control dashboard. It centralizes these controls so each backend does not need to implement them separately.

[Try the public API](https://gateforge-ess7ryi2la-ew.a.run.app/catalog/items) · [Dashboard](https://gateforge-ess7ryi2la-ew.a.run.app/admin/) · [Interactive explainer](https://gateforge-ess7ryi2la-ew.a.run.app/admin/#learn)

Hosted on Google Cloud Run with PostgreSQL and private Redis. Cloud Run terminates HTTPS; local Docker deployments use gateway-managed TLS. The users, orders, and catalog backends return sample data. Protected routes require API keys, and administration requires a separate token. JWT validation is supported but needs an issuer configuration before use.

- **Go:** longest-prefix routing, round-robin balancing, active health checks, request-size limits, and bounded timeouts. Retries apply only to safe GET/HEAD requests; writes are not automatically replayed.
- **Redis:** atomic quotas shared across gateway instances and explicit public-response caching. Credential-bearing, private, and non-cacheable responses bypass the cache. Rate limiting fails closed if Redis is unavailable.
- **PostgreSQL:** persistent route revisions and hashed, scoped API keys. Revision checks prevent conflicting configuration saves; unique request IDs prevent duplicate key creation. Indexed cursor pagination bounds key-list queries.
- **React and Tailwind CSS:** responsive route editing, key management, backend health, and request metrics. Request cancellation, loading/error states, and submission locks handle slow or failed operations. Public JS/CSS assets use gzip compression.
- **Docker and GCP:** multi-stage images, private backend sidecars, Secret Manager credentials, structured logs, and external readiness monitoring. Cloud Build runs integration tests before manual deployment; automatic cloud deployment is disabled.

```text
cmd/             # Gateway, validation tools, and load generator
internal/        # Routing, authentication, storage, Redis, and metrics
web/             # React dashboard and interactive explainer
examples/echo/   # Sample backend service
configs/         # Route and authentication examples
scripts/         # Local setup and disposable test services
infra/gcp/       # Cloud Run and Compute Engine deployment files
docs/            # Architecture, deployment, validation, and benchmarks
```

## Run locally

Install Docker Desktop with Linux containers and Compose 2.24.4+. From the repository root in PowerShell:

```powershell
./scripts/setup-local.ps1
docker compose --profile scale up --build -d --wait
```

Open [localhost:8443/admin/](https://localhost:8443/admin/) after configuring trust for the development certificate. Use `GATEFORGE_ADMIN_TOKEN` from the generated, gitignored `.env`. Keep credentials out of Git. `/catalog/items` is public; `/users/42` and `/orders/7` require an API key. Certificate setup and Go-only instructions are in [deployment](docs/deployment.md).

## Verify

With Go 1.25+ and Node.js 24 installed:

```powershell
go test -timeout 120s ./...
go vet ./...
npm --prefix web ci
npm --prefix web test
npm --prefix web run build
```

Database integration tests require `GATEFORGE_TEST_DATABASE_URL`; otherwise they are skipped. Cloud validation uses disposable PostgreSQL/Redis and covers race detection, 100 simultaneous HTTPS requests, duplicate submissions, fail-closed behavior, and logical backup restoration. See [validation](docs/validation.md) and [safeguards](docs/reliability.md) for exact coverage.

The live demo uses one Cloud Run instance, zonal PostgreSQL, and Basic Redis. Metrics reset when an instance restarts; this is not a highly available production deployment. Next steps are shared metrics storage, redundant infrastructure, and managed-backup recovery drills. Budget alerts and instance limits do not impose a hard spending cap.

[Architecture](docs/architecture.md) · [Deployment](docs/deployment.md) · [Performance](docs/performance.md)
