# Request-path performance refactor

Measured on 2026-09-30, Windows amd64, Go 1.27.1, AMD Ryzen 7 5800X. Results are medians of three 500 ms samples with the same benchmark code before and after the refactor. These are local microbenchmarks, not production capacity or full-stack throughput measurements.

## Changes

- Reuse 32 KiB response-copy buffers through `httputil.ReverseProxy.BufferPool` and `sync.Pool`. A buffer belongs to one active response until copying completes. The pool is shared across routes and reloads, and the garbage collector can discard unused buffers.
- Resolve metrics counters once per handler, on its first completed request. Each route has its own lock; requests to unrelated routes no longer share a global update lock. Handler generations reuse counters for the same route.
- Copy metrics rows under their individual locks, then calculate means and sort outside the locks. Each row is internally consistent, but rows in a scrape can represent slightly different moments. This is not a transaction across all routes.
- Build the unmatched-route metrics wrapper once when routes are compiled, instead of rebuilding it for every 404.

Authentication, API-key revocation, distributed Redis policy, routing selection, and retry eligibility are unchanged. Metrics retain the existing 512 named-route limit plus an overflow row and only show routes with completed requests.

## Measurements

| Benchmark | Before | After | Interpretation |
| --- | ---: | ---: | --- |
| Proxy, 1 KiB response, 1 CPU: allocated bytes/request | 41,245 B | 8,468 B | 79.5% less allocation |
| Proxy, 64 KiB response, 1 CPU: allocated bytes/request | 41,247 B | 8,468 B | 79.5% less allocation |
| Proxy, 1 KiB response, 1 CPU: time/request | 508.7 us | 496.5 us | Small observed difference; no meaningful end-to-end speedup claimed |
| Proxy, 64 KiB response, 1 CPU: time/request | 517.9 us | 511.6 us | Small observed difference; no meaningful end-to-end speedup claimed |
| Metrics, 64 configured routes, 8 workers on separate routes | 168.9 ns/op | 47.9 ns/op | 71.6% less time, approximately 3.5x benchmark throughput |
| Metrics, 1 route, 8 workers | 189.1 ns/op | 148.3 ns/op | 21.6% less time |
| Metrics, 1 route, 1 worker | 126.9 ns/op | 142.5 ns/op | 12.3% more time in this small sample; benefit targets concurrent traffic |

The proxy benchmark includes a real HTTP loopback upstream and discards the downstream response body. It excludes TLS, PostgreSQL, Redis, and metrics. It measures allocations across the test process, including the upstream. Reusing the copy buffer saves approximately 32 KiB per response; it does not mean the gateway's total resident memory falls by 79.5%. Active concurrent responses still need their own buffers.

The metrics benchmark exercises the actual middleware around a no-op handler and reuses worker-local request/writer objects. Three samples are useful directional evidence, not a statistical confidence claim. Raw output: [before](benchmarks/2026-09-30-before.txt), [after](benchmarks/2026-09-30-after.txt).

Reproduce in PowerShell from the repository root, with Go on PATH:

```powershell
go test ./internal/analytics ./internal/gateway -run '^$' -bench 'Benchmark(MetricsParallel|ProxyResponse)' -benchmem -benchtime=500ms -count=3 '-cpu=1,8'
```

## Validation

- `go test -count=1 ./...` and `go vet ./...` passed on Windows.
- `go test -race -count=1 -timeout 120s ./...` passed in Linux Docker with real PostgreSQL and Redis integration enabled. Database tests used disposable isolated schemas.
- Added concurrent response integrity coverage with distinct response contents and sizes, concurrent metrics scrape/update coverage, counter sharing across handler generations, snapshot independence, lazy route registration, and cardinality limits.
- Existing streaming, TLS, authentication, cache, routing, health, timeout, and retry tests passed.
- Both local gateway containers were rebuilt and became healthy. `cmd/stackcheck` passed trusted TLS, authentication, shared quotas/cache, and revocation against them. The dashboard returned 200 and the anonymous admin configuration API returned 401 with normal Windows certificate verification.

Cloud deployment and cloud performance measurements remain pending under the existing spending restriction.
