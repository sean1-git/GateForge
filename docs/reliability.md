# Gateway safeguards and validation

GateForge's relevant safeguards cover API traffic, administration, storage and operations. It has no subscription billing, payments or file-storage feature, so payment deduplication and upload storage were not added. Request-body limits still protect the proxy and admin API.

## Traffic and spending controls

- Redis atomically enforces the configured per-key request quota across gateways. The demo permits 60 requests/minute for `/users`, 30 for `/orders`, and 300 for public `/catalog`. Excess requests receive 429 and `Retry-After`; unavailable Redis causes 503 rather than bypassing the limiter. Public requests use the connection IP for their quota, so clients behind a platform proxy can share that quota.
- Route `max_body_bytes` defaults to 1 MiB and can be set up to 16 MiB. Bodies, including chunked bodies, are checked before contacting a backend; oversized bodies receive 413. This bounds buffering and intentionally excludes streaming uploads. Admin JSON has a separate 1 MiB limit. Headers are limited to 32 KiB and client request reads to 15 seconds.
- Route `timeout_ms` defaults to 10 seconds when omitted/zero; the demo explicitly uses 5 seconds. Safe GET/HEAD failover remains available; mutation requests are never automatically replayed by the gateway or dashboard.
- Public response caching remains explicit, bounded and Redis-backed. Credentials, cookies, private/no-store responses and unsupported variations bypass shared caching. Admin responses are not compressed or cached. Only public JS/CSS assets are precompressed with gzip.
- Cloud Run remains at zero minimum / one maximum instance, concurrency 40. The project has $50/$90/$100 budget alerts against the approved $100 planning budget. These settings are **not a hard spending cap**: even rejected requests can incur charges, and PostgreSQL/Redis continue billing while the gateway is idle. A guaranteed dollar ceiling requires stopping billable resources, not an application flag.

## Administrator API and dashboard

Dashboard calls time out after 10 seconds, cancel on session exit, show failures from both JSON and non-JSON responses, and never retry writes automatically. Polling waits for completion before scheduling its next run. Forms lock synchronously to reject double clicks; loading, empty and failure states are distinct. A timed-out write might have committed, so the UI tells the administrator to check the result.

`POST /admin/api/keys` accepts `Idempotency-Key` (16–128 ASCII letters, digits, `_` or `-`). Clients should reuse the same value **and identical request payload** after an uncertain result. A unique PostgreSQL index allows only one key creation for that ID, including simultaneous requests across instances. Replays receive 409 and the existing `key_id`; payload conflicts receive 409. Raw secrets remain one-time values and are never stored for replay. If the original response was lost, revoke the listed key and create a new one with a new request ID. Clients that omit the header do not get this deduplication guarantee. Existing configuration updates use revision checks and return 409 for stale saves.

`GET /admin/api/keys?limit=50&cursor=...` returns `{ "keys": [...], "next_cursor": "..." }`. `next_cursor` is omitted at the end. The default is 50, maximum 200. The dashboard uses Previous/Next. This replaces the old bare-array response; API consumers must read `keys`. Pagination uses the `(created_at DESC, id DESC)` index and a keyset cursor, so equal timestamps do not skip or repeat keys. It does not run an expensive total-count query. Concurrent inserts newer than the current cursor appear when starting again at the first page.

API-key lookups already use the unique `key_hash` index; revocation uses the primary-key index. Refresh queries now fetch and decode route JSON only when its revision changed. The connection pool stays bounded at ten connections per gateway.

## Monitoring and errors

The `GateForge readiness` Cloud Monitoring check calls HTTPS `/readyz` every five minutes from Europe, Iowa and Oregon, validates the certificate, and requires HTTP 200 within 20 seconds. Readiness covers backend availability, PostgreSQL and Redis. The alert policy opens incidents after failures from at least two checker locations persist for five minutes and is connected to the owner's Google Cloud account email. Alert delivery itself has not been failure-tested. The monitor generates a small amount of request traffic and can wake a scaled-down instance.

Gateway logs are structured JSON with Cloud Logging severity. Server errors and recovered panics are logged without request bodies, credentials, URL queries or panic payloads. A panic before response headers produces a generic 500; a panic after headers aborts the response. Existing upstream error responses distinguish timeout (504), unhealthy backends (503), and transport failure (502). Cloud Run also records its own request logs. Per-instance dashboard metrics remain in memory; uptime monitoring is independent of an open dashboard.

## Tests and restore rehearsal

Cloud Build starts disposable PostgreSQL 17 and Redis 7.2 containers, then runs Go race tests/vet, frontend tests/build and the image build. Test credentials are generated per build and excluded from the image/source archive. The build environment and test containers are disposable; the live databases are never used for these tests.

Tests cover 100 simultaneous HTTPS requests across two gateways/two API keys (exactly 20 accepted and 30 rate-limited per key), 20 simultaneous duplicate creation attempts (one key created), pagination with identical timestamps, oversized fixed/chunked requests, safe panic recovery, gzip negotiation, API timeouts and duplicate form submissions.

The opt-in `TestBackupRestore` test uses `pg_dump`/`pg_restore` to restore a real dump into a separate, temporary PostgreSQL database. It checks saved routes, active hashed keys, revoked keys, deduplication and indexes, then removes both temporary databases and the dump. Run only with a disposable base database whose name ends in `_test`, PostgreSQL client tools installed, and `GATEFORGE_TEST_BACKUP_RESTORE=1`. This is a logical backup restore rehearsal; it does **not** validate Cloud SQL's managed backup/PITR recovery procedure or establish a production recovery-time guarantee.

Cloud SQL daily backups, three retained backups and one day of PITR remain enabled. The deployment is zonal with Basic Redis and a single Cloud Run instance, so it is a portfolio demo rather than a highly available service.
