# Preview, failure experiments and performance evidence

## Offline policy preview

Open **Routes → Edit policies → Preview sampled requests**. The proposed JSON identifies an existing pool with `source_prefix`; `prefix` can change its route match. Backend addresses remain server-only. The table compares routing and authorization on up to 100 recent completed requests from this instance. Applying is a separate action with an optimistic revision check; editing the draft invalidates its displayed preview. Existing API clients may still save directly.

`POST /admin/api/policies/preview` validates without starting probes, sending a request, consulting a credential store, reserving capacity, or updating configuration. It uses the original authentication facts and process-keyed HMAC fingerprints of slash-boundary prefixes and permissions. No raw path, query, body, key, JWT, subject, claim text, or fingerprint leaves the sampling store. Samples are bounded to 64 prefix fingerprints and 256 permission fingerprints and disappear on restart. Oversized evidence and credentials that were not validated originally produce **unknown**, not guessed approval. Results describe the historical sample; subsequent revocations/expiry, rate-limit consumption, cache state, backend health, concurrency and future requests are not predicted. Configured route names remain visible to administrators.

## Tenant concurrency

Admission defaults to **128 in-flight application requests per process, 16 per tenant**, shared across routes and policy reloads. Tenant exhaustion returns 429; total exhaustion returns 503. Both include `Retry-After: 1`. There is no waiting queue, and slots release on completion, cancellation or panic. Proxy transports also cap connections per backend host at 64. Bounds cover request handling and response streaming, not just response headers.

Administrators can assign `tenant_id` when creating API keys; all keys for the same tenant share their allowance. Existing/unassigned keys retain a separate allowance per key. JWT tenants come from the verified issuer plus signed `tenant_id`, falling back to the authenticated issuer/subject. Client-supplied tenant headers are ignored. API-key tenants and JWT tenants occupy separate namespaces; this version does not merge identities across authentication providers. Public requests share an anonymous tenant. These are process-local bulkheads, not a distributed concurrency lease or a guarantee against many simultaneous tenants; Redis request quotas remain distributed. `GET /admin/api/concurrency` exposes aggregate counters without identities.

## Isolated failure lab

**Failure lab** runs actual requests through the gateway implementation over locally trusted test TLS, using ephemeral loopback backends and generated fixture credentials. It cannot accept a URL, target a production backend, edit stored policies, or use real customer keys. Scenarios measure baseline, close one backend listener and restore it, inject 350 ms latency against a 150 ms total deadline, or compare an unpartitioned four-slot pool against one slot per tenant. The fairness scenario uses a two-second deadline and 80 ms backend work to separate admission pressure from the timeout scenario. A timeout that exhausts the entire deadline is not retried; the recorded decision explains why. Health probes detect recovery, and retry traces show real transport failures and distinct backend selections.

Experiments are administrator-only and use the normal session/CSRF boundary. Only one run per instance is accepted, with at most 13 clients, 2,000 measurements, 36 representative traces and a 12-second ceiling. Live polling holds a short request open so request-based Cloud Run CPU remains available. Closing the page can delay cleanup until the instance next receives CPU; it cannot extend the experiment's wall-clock deadline. The demo shares the gateway process and CPU, while its servers, pools, identities and records are isolated from production state. It is intentionally small, not a production chaos control.

## Reproducible end-to-end evidence

```powershell
go test -run '^$' -bench . -benchmem ./internal/gateway ./internal/analytics
go run -buildvcs=true ./cmd/evidence -scenario all -repeat 3 > .cache/evidence.json
```

The CLI accepts only the four fixed scenarios, never an arbitrary destination. The JSON report includes revision/build information, Go/OS/architecture/CPU settings, workload description, per-phase/per-tenant completed requests, successes, 429 rejections, errors, timeouts, actual retries, requests/second, p50/p95/p99/max latency, and process heap/allocation/goroutine measurements. Use the same commit, machine, Go version, `GOMAXPROCS`, scenario and repetition count for comparisons; retain separate runs rather than averaging percentiles.

This is a bounded **closed-loop** experiment: two workers for normal/failure cases; twelve busy workers and one quiet worker for fairness, each waiting 25 ms after completion. It does not establish an open-loop offered-load SLO and can underrepresent latency under sustained arrival pressure. Measurements include TLS and reading the complete body. Heap measurements include the synthetic clients, servers, gateway, tracing and other process activity; they are not isolated gateway memory or RSS. Initial TLS connections are included (no hidden warm-up), peaks are sampled at completions, and a capacity number is evidence for that host, not a universal claim. Download equivalent single-run evidence from the dashboard. CI exercises failure, timeout, tenant isolation and recovery behavior against real sockets.
