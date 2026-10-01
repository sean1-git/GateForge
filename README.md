# GateForge | API Gateway & Developer Platform

A Go reverse-proxy API gateway with path routing, HTTPS termination, JWT/API-key authentication, PostgreSQL configuration, Redis quotas and public-response caching, backend health checks, and a React admin dashboard.

As a web application grows into multiple backend services, each service should not have to independently handle authentication, rate limits, routing, logging, security, and failures. GateForge provides one controlled entry point that manages those concerns for the entire system.

```text
Client -- HTTPS --> GateForge --> healthy service replica
                       |-- PostgreSQL: routes and hashed API keys
                       |-- Redis: distributed limits and cached responses
                       |-- React: configuration, keys and instance metrics
```

## Current status

The application is deployed on Google Cloud Run with persistent PostgreSQL and private Redis. Try the [public catalog demo](https://gateforge-ess7ryi2la-ew.a.run.app/catalog/items) or open the [administrator sign-in](https://gateforge-ess7ryi2la-ew.a.run.app/admin/). Administrator APIs and protected service routes require credentials. Validation includes real PostgreSQL/Redis integration, Linux race detection, simultaneous users, a logical backup restore rehearsal, and backend/database/cache outage checks in disposable environments. See [safeguards and limits](docs/reliability.md), [validation results](docs/validation.md), and [performance measurements](docs/performance.md).

| Capability | Behavior |
| --- | --- |
| Routing | Longest path-prefix match, exact path-segment boundaries, multiple services |
| Security | TLS 1.2+, RS256 JWT claims/scopes, expiring/revocable API keys stored as hashes |
| Persistence | PostgreSQL route revisions and key permissions; dashboard changes refresh across instances |
| Redis | Atomic shared rate limits; explicit, bounded public GET caching |
| Reliability | Round-robin healthy backends, active probes, timeouts, bounded safe retries |
| Administration | React route editor, key management, backend status and instance metrics |
| Operations | Readiness/liveness, protected Prometheus metrics, Docker Compose and GCP scripts |

The included users, orders and catalog services are echo demos. They do not implement business data. Analytics are in memory per gateway; durable aggregate analytics require a metrics collector. The Cloud Run demo has a one-instance limit, zonal PostgreSQL and Basic Redis; it is not highly available. Separate Compute Engine deployment scripts are also included.

## Explore how a gateway works

Open `https://localhost:8443/admin/#learn` while the local stack is running, or choose **How it works** in the dashboard. A link is also available on the sign-in screen; the explainer needs no administrator token.

The interactive diagrams let you follow a request, compare separate service limits with one shared budget, and distribute requests across healthy servers. Send individual requests or a burst, enable automatic playback, and take a simulated server offline. All traffic and counters in this view are illustrative and stay in the browser. Weighted 4:1:1 distribution is a teaching example; the running gateway uses round robin. Rate-limit budgets are examples, not live configuration.

## Run the full stack

Install Docker Desktop with Linux containers and Compose v2.24.4 or newer. In PowerShell:

```powershell
./scripts/setup-local.ps1
docker compose --profile scale up --build -d --wait
```

This creates private local credentials and starts PostgreSQL, Redis, demo services, and HTTPS gateways on ports 8443 and 8444. Open `https://localhost:8443/admin/` after reviewing/trusting the generated development certificate. Sign in using `GATEFORGE_ADMIN_TOKEN` from your gitignored `.env` file.

- `/catalog/items` is public and eligible for caching.
- `/users/42` and `/orders/7` require a key issued through the dashboard.
- The dashboard edits route configuration and shows traffic and backend health.

See [run and deployment instructions](docs/deployment.md) for certificate verification, JWT setup, tests, and GCP provisioning. The app does not read `.env` when run directly with Go; Compose loads it.

## Small Go-only routing demo

Requires Go 1.25 or newer; tested with Go 1.27.1. Run these in separate terminals:

```powershell
go run ./examples/echo -listen 127.0.0.1:9001 -name users
```

```powershell
go run ./examples/echo -listen 127.0.0.1:9002 -name orders
```

```powershell
go run ./cmd/gateway -config ./configs/routes.example.json
```

```powershell
curl.exe http://127.0.0.1:8080/users/42
curl.exe http://127.0.0.1:8080/orders/7
```

This smaller example enables public routing only. The full stack demonstrates authentication, persistence, caching and the dashboard. If PowerShell cannot find an installed Go binary, reopen the terminal or add `C:\Program Files\Go\bin` to that terminal's PATH.

## Verify

```powershell
go test -timeout 60s ./...
go vet ./...
go build -o bin/gateway.exe ./cmd/gateway
cd web
npm ci
npm test
npm run build
```

Node 24 is used for the dashboard. Database integration tests need `GATEFORGE_TEST_DATABASE_URL`; without it they are explicitly skipped. See [validation instructions](docs/deployment.md#validation) for two-gateway, load and failure tests.

Read [architecture and behavior](docs/architecture.md) for authorization rules, cache eligibility, retry guarantees and operational limits. GCP deployment requires your project, zone, domain/certificate, credentials and approval of cloud costs before execution.
