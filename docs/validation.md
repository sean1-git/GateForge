# Validation record

Verified locally on 2026-09-30 with Windows, Go 1.27.1, Node 24.21.0, and Docker Engine 29.7.2 / Compose v5.4.0 using Linux containers. These are development results, not a production certification.

| Check | Result |
| --- | --- |
| Go tests and vet | Passed across all packages |
| PostgreSQL integration | Passed against a temporary real PostgreSQL 17.11 database, using isolated test schemas |
| Two HTTPS gateway instances | Passed shared key authorization/revocation, Redis quota and cache tests with both miniredis and the real Compose Redis server |
| Authentication | Covered invalid/expired/revoked/scoped keys, JWT algorithm/issuer/audience/expiry/subject/scope, TLS enforcement and admin isolation |
| Routing and reliability | Covered prefix boundaries, ambiguous path rejection, health failure/recovery, balancing, safe retries and deadlines |
| Cache rules | Covered credential/cookie/private/oversize bypass, query/Accept separation, shared entries and preservation of upstream Age |
| Executable TLS smoke test | Passed certificate-verified HTTPS proxy request, separate loopback readiness and admin isolation on the health port |
| Windows and Linux builds | Passed Go command builds and actual Linux container execution |
| Linux race detection | `go test -race -count=1 -timeout 120s ./...` passed inside a temporary Linux container with real PostgreSQL and Redis integration enabled |
| React tests and production build | Passed |
| Browser | Verified sign-in, metrics, backend status, route save/revision update and sign-out; no browser errors observed |
| Dependency checks | npm install reported zero vulnerabilities; govulncheck v1.8.0 reported no vulnerabilities after dependency updates |
| Compose manifests | Local and GCP merged configurations passed Compose v5.5.1 config validation |
| Compose execution | Image build and both HTTPS gateway readiness checks passed; PostgreSQL, Redis and four demo backends are running locally |
| Container failure tests | Remaining users replica served traffic after stopping users1; Redis and PostgreSQL outages returned 503 on protected limited routes; all services were restored and readiness passed |
| Deployed local admin isolation | Dashboard HTML and public catalog returned 200; unauthenticated users route, admin config, admin keys and metrics returned 401 over certificate-verified HTTPS |
| GCP provision script | Plan-only execution passed without creating resources |

The dependency scan found and prompted updates of `golang.org/x/text` to v0.39.0 and `golang.org/x/sys` to v0.44.0. See the Go advisories [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970) and [GO-2026-5024](https://pkg.go.dev/vuln/GO-2026-5024). A clean scan is a point-in-time result, not a guarantee against unknown defects.

## Load smoke test

The local Go gateway forwarded requests to the loopback echo backend for 10 seconds at a target 25 requests/second:

- 249 completed requests, all HTTP 200.
- Zero transport errors.
- Approximately 2.3 ms p95 client latency.

This exercised public HTTP forwarding without Redis policy or TLS, on one local machine. It is not a measurement of the complete secured stack, distributed throughput or cloud capacity. The executable TLS check and two-gateway integration tests cover separate functionality.

After Docker setup, a second 10-second smoke test exercised the public catalog over certificate-verified HTTPS through the running Compose stack with real Redis rate limiting and response caching. At a target 10 requests/second it completed 99 requests, all HTTP 200, with zero transport errors and approximately 2.26 ms p95 client latency. This remains a small development check, not a capacity benchmark or an authenticated-route load test.

The subsequent [performance refactor](performance.md) records before/after proxy-allocation and concurrent-metrics benchmarks, along with another passing Windows test/vet run and Linux race/integration run using real PostgreSQL and Redis.

## Still to execute

- GCP application deployment, trusted domain/certificate setup where needed, cloud load/failure tests and backup/restore verification. Cloud provisioning requires a reviewed configuration and approved costs.
- GitHub Actions execution for the published branch. Local checks are recorded above; remote workflow results should be checked on GitHub.

The full-stack start and validation commands are in [deployment.md](deployment.md). Metrics are per-instance memory and the cloud design is one VM; durable analytics and highly available managed infrastructure remain separate production work.

## Cloud deployment status

The complete application has been verified locally. A full GCP application deployment and its security, load, and recovery checks remain pending. Cloud project identifiers, build identifiers, and account-specific operational notes are intentionally kept out of the public source.

Publishing source to a non-deployment branch does not provision cloud infrastructure. Review branch triggers and obtain cost approval before merging into an automatically deployed branch.
