# GateForge | API Gateway & Developer Platform

A Go reverse-proxy gateway and developer control dashboard that securely routes API requests, explains how each request was handled, and previews policy changes before applying them. It combines shared rate limits and caching with per-tenant concurrency limits and an isolated lab for testing real failures.

[Try the public API](https://gateforge-ess7ryi2la-ew.a.run.app/catalog/items) · [Dashboard](https://gateforge-ess7ryi2la-ew.a.run.app/admin/) · [Interactive explainer](https://gateforge-ess7ryi2la-ew.a.run.app/admin/#learn)

Hosted on Google Cloud Run with PostgreSQL and private Redis. Cloud Run terminates HTTPS; local Docker deployments use gateway-managed TLS. The users, orders, and catalog backends return sample data. Protected routes require API keys, and administration requires a separate token. JWT validation is supported but needs an issuer configuration before use.

- **Go:** longest-prefix routing, round-robin balancing, active health checks, request-size limits, and bounded timeouts. Retries apply only to safe GET/HEAD requests; writes are not automatically replayed.
- **Redis:** atomic quotas shared across gateway instances and explicit public-response caching. Credential-bearing, private, and non-cacheable responses bypass the cache. Rate limiting fails closed if Redis is unavailable.
- **PostgreSQL:** persistent route revisions, shared metrics, audit events, and hashed, scoped API keys. Revision checks prevent conflicting configuration saves; unique request IDs prevent duplicate key creation. Indexed cursor pagination bounds key-list queries.
- **React and Tailwind CSS:** request explanations, policy previews, failure experiments, key management, backend health, and request metrics. Request cancellation, loading/error states, and submission locks handle slow or failed operations. Public JS/CSS assets use gzip compression.
- **Docker and GCP:** multi-stage images, private backend sidecars, Secret Manager credentials, structured logs, and external readiness monitoring. Cloud Build runs integration tests before manual deployment; automatic cloud deployment is disabled.

```text
cmd/             # Gateway, validation tools, load generator, and evidence runner
internal/        # Routing, authentication, storage, Redis, and metrics
web/             # React dashboard and interactive explainer
examples/echo/   # Sample backend service
configs/         # Route and authentication examples
scripts/         # Local setup and disposable test services
infra/gcp/       # Cloud Run and Compute Engine deployment files
docs/            # Architecture, deployment, validation, and benchmarks
```

## What you can explore

- **Explain a request:** open **Requests → Explain** to see why authentication passed or failed, a route or backend was selected, caching was skipped, or a retry occurred.
- **Preview a policy:** open **Routes → Edit policies → Preview sampled requests** to compare routing and access decisions for up to 100 recent requests without sending them again. Editing the draft invalidates its preview; applying is a separate action. Missing evidence is reported as unknown.
- **Watch a failure:** open **Failure lab** to stop a disposable backend or introduce latency, then observe actual retries, timeouts, failover and recovery. Experiments use separate loopback servers and synthetic credentials, with no changes to application routes or backends.
- **Compare tenant fairness:** run the lab's shared-pool and per-tenant scenarios to see how one busy customer affects another. Application defaults allow 128 in-flight requests per instance and 16 per tenant; the demo uses smaller limits to make contention visible.
- **Export evidence:** download lab results or run `go run -buildvcs=true ./cmd/evidence -scenario all -repeat 3` to collect p50/p95/p99 latency, errors, retries and process-memory measurements with revision and runtime metadata.

Request samples, previews and concurrency limits are instance-local. Preview uses historical authentication facts; it does not revalidate credentials or predict future traffic. Lab servers are isolated from application state but share the gateway's CPU and memory. See [experiments and measurement limits](docs/experiments.md) for setup, privacy and interpretation.

## Measured results

| Improvement | Recorded result | Test scope |
| --- | --- | --- |
| Response-buffer reuse | **79.5% fewer allocated bytes per request**, from 41,245 B to 8,468 B | Local proxy microbenchmark, 1 KiB responses, one CPU; not total process memory |
| Per-route metrics locking | **3.5× metrics-recording throughput**, from 168.9 to 47.9 ns/op | Local microbenchmark, 8 workers, 64 configured routes; not overall gateway throughput |
| Per-tenant concurrency limits | Quiet-tenant success increased from **13% (7/55) to 100% (19/19)** | Short Cloud Run lab experiment with 12 busy workers and one quiet worker |

The allocation and metrics results are medians of three samples; see the [before/after benchmarks](docs/performance.md). The fairness result is one controlled synthetic run, documented with its [workload and denominators](docs/benchmarks/2026-10-02-experiments.md). These measurements demonstrate behavior under the stated workloads and do not establish a production capacity or availability guarantee.

## Run locally

Install Docker Desktop with Linux containers and Compose 2.24.4+. From the repository root in PowerShell:

```powershell
./scripts/setup-local.ps1
docker compose --profile scale up --build -d --wait
```

Open [localhost:8443/admin/](https://localhost:8443/admin/) after configuring trust for the development certificate. Use `GATEFORGE_ADMIN_TOKEN` from the generated, gitignored `.env`. Keep credentials out of Git. `/catalog/items` is public; `/users/42` and `/orders/7` require an API key. Certificate setup and Go-only instructions are in [deployment](docs/deployment.md).

## Verify

With Go 1.26+ and Node.js 24 installed:

```powershell
go test -timeout 120s ./...
go vet ./...
npm --prefix web ci
npm --prefix web test
npm --prefix web run build
```

Database integration tests require `GATEFORGE_TEST_DATABASE_URL`; otherwise they are skipped. Cloud validation uses disposable PostgreSQL/Redis and covers race detection, 100 simultaneous HTTPS requests, duplicate submissions, fail-closed behavior, and logical backup restoration. See [validation](docs/validation.md) and [safeguards](docs/reliability.md) for exact coverage.

The deployed demo uses one Cloud Run instance, zonal PostgreSQL, and Basic Redis. Aggregate traffic metrics persist in PostgreSQL and are shared across gateway instances; the newest unflushed measurements can be lost on a crash. Request explanations and lab results remain in memory and disappear on restart. This is not a highly available production deployment. Future work includes shared request diagnostics, distributed concurrency coordination, redundant infrastructure, and managed-backup recovery drills. Budget alerts and instance limits do not impose a hard spending cap.

[Administrator sign-in](docs/admin-sign-in.md) exchanges the admin token over HTTPS for a revocable HttpOnly session, with hashed storage, CSRF protection and distributed login limits. Individual Google sign-in is also supported after OAuth configuration; legacy token mode remains available for local CLI validation.

[Architecture](docs/architecture.md) · [Deployment](docs/deployment.md) · [Performance](docs/performance.md)

### Explain a real request

Open **Requests** in the administrator dashboard, send traffic through an application route, and choose **Refresh → Explain**. Search by matched route, method, status, or the `X-GateForge-Request-ID` returned with the response. The gateway generates that ID and forwards it to the selected backend; client and upstream values cannot replace the recorded ID.

The timeline records the actual routing, authentication, quota, cache, backend selection, retry, and response decisions. Events show elapsed time from entry into the application pipeline, not individual stage durations or network/TLS timing. Early exits only show stages that ran. Response-header status and interrupted delivery are distinguished.

`GET /admin/api/requests` uses the same HTTPS and administrator authentication requirements as the other admin endpoints and returns `Cache-Control: no-store`. Each process keeps the latest 100 finished request handlers in a memory ring, with at most 64 events per request. Raw request paths, query strings, headers, bodies, credentials, and client identities are not retained; the view shows the configured route prefix and an ordinal backend label. Health checks and administrative requests are excluded. Paths rejected by the outer canonical-path check never enter this application timeline.

Records disappear on restart and are not shared across instances. A dashboard refresh in a multi-instance deployment can reach a different buffer. Request explanations are diagnostic records, not durable audit events. Exporting or sharing them across instances is a separate future feature.

### Request limits and publication privacy

The gateway applies a bounded in-memory admission limit before authentication: 600 application requests and 120 administrator API/auth requests per connection peer per minute, per process. Invalid credentials count toward these limits. Caller-supplied forwarding headers cannot change the bucket. Health checks and static dashboard assets are exempt. Behind a reverse proxy, clients sharing its connection peer share this admission budget. These protective local limits supplement the Redis route quotas (60 users, 30 orders, 300 catalog requests per 60 seconds in the supplied stack); they are not distributed tenant quotas. Both layers return HTTP 429 with `Retry-After` when exhausted.

The dashboard reads `/admin/api/policies`, which omits backend URLs and health-probe destinations. Policy edits preserve server-side destinations and require the current revision. Backend health and request explanations expose ordinal labels only. The raw `/admin/api/config` endpoint remains an HTTPS, administrator-only configuration interface for trusted tooling; the browser does not request it. Provisioned API keys are returned once to the authenticated administrator, masked by default, held only in tab memory, and stored as hashes on the server.

Public demo responses no longer echo service names, paths, or query values. The proxy strips known technology/credential headers and replaces upstream 5xx diagnostic bodies with generic errors. Application backends must still avoid returning secrets in their own successful payloads or custom headers. Static serving permits only the built entry page and supported assets, excluding directory listings, environment files, and source maps.

Run `python scripts/check-publication.py` before publishing. CI rejects tracked confidential paths and scans Git history with a pinned Gitleaks image and redacted output. Ignored local environment files, credential files, certificates, database files, caches and build outputs stay outside Git and deployment build contexts. Secret scanning reduces accidental publication risk; it is not a guarantee that arbitrary sensitive business data will be detected.
