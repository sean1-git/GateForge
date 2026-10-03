# Failure and tenant-fairness experiments — 2026-10-02

Measured with release `02a1b5121036053bdf37c56da952c0c13fb1a827` in the administrator-only Cloud Run failure lab. Runtime: Go 1.27.1, Linux amd64, GOMAXPROCS 2. These are short synthetic experiments inside the gateway process, using disposable loopback backends and generated fixture credentials. They do not measure public internet latency, managed database calls, or production customer traffic.

## Tenant fairness

Twelve workers represented a busy tenant and one worker represented a quiet tenant. Each waited 25 ms after completing a request. Both backends performed 80 ms of simulated work, with a two-second gateway deadline. The global limit stayed at four in-flight requests. The first phase allowed either tenant to occupy all four slots; the second limited each tenant to one. Each phase generated traffic for approximately two seconds and drained outstanding requests.

| Phase | Tenant | Completed | Successful | HTTP 429 | Errors | p50 ms | p99 ms |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Shared pool only | Busy | 655 | 89 | 378 | 188 | 0.218 | 81.866 |
| Shared pool only | Quiet | 55 | 7 | 0 | 48 | 0.228 | 93.096 |
| Per-tenant limit | Busy | 842 | 24 | 818 | 0 | 0.199 | 81.121 |
| Per-tenant limit | Quiet | 19 | 19 | 0 | 0 | 80.752 | 92.506 |

Quiet-tenant success increased from 7/55 (12.7%, rounded to 13%) to 19/19 (100%) in this run. The overloaded tenant received immediate rejections rather than occupying the quiet tenant's available capacity. The lower completion count in the protected phase reflects successful requests taking longer than immediate rejections in this closed-loop workload. Latency percentiles include all outcomes, so fast errors can produce misleadingly low medians.

The demo limits are deliberately small; application defaults are 128 in-flight requests per process and 16 per tenant. This is evidence of isolation in the stated two-tenant workload, not a guarantee for arbitrary numbers of tenants or across multiple instances.

## Failure and recovery

In separate runs on the same release, stopping one backend produced one recorded retry with no failed requests. Injecting 350 ms of backend delay against a 150 ms deadline produced six HTTP 504 responses; the subsequent recovery phase completed 152 successful requests. These were actual socket requests and gateway decisions, not animated or fabricated outcomes. Counts vary with scheduling and health-check timing.

## Reproduce

Open **Failure lab** in the authenticated dashboard and run **Tenant fairness**, **Stop a backend**, or **Inject latency**. Download the evidence JSON after each run. The report includes runtime metadata, counts, latency percentiles, memory measurements and representative decision traces.

For repeated local runs with revision metadata:

```sh
go run -buildvcs=true ./cmd/evidence -scenario all -repeat 3 > evidence.json
```

Compare individual runs using the same commit, runtime, resources and workload. Local results will differ from Cloud Run results. See [experiment methodology and limits](../experiments.md) and the separate [allocation/metrics microbenchmarks](../performance.md).
