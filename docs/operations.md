# Shared metrics, audit and managed recovery

## Metrics persistence

When PostgreSQL is configured, each gateway process has a random source ID. It
periodically submits cumulative route counters. A transaction locks that source
and the shared total, applies only the delta, and commits both together. Retries
and concurrent processes cannot double count the same snapshot. The dashboard
reads shared totals; Prometheus `/metrics` remains per-process so a collector
does not double count global totals scraped from multiple replicas.

Flushes occur on request completion at most once every five seconds, with a
300 ms database deadline, on dashboard reads, and during graceful shutdown.
Failed background flushes preserve local counters and do not fail proxy traffic.
Dashboard reads fail explicitly when persistence is unavailable. A crash can
lose the unflushed tail; an idle process or database outage can extend this gap.
These are operational counters, not billing-grade accounting. Route cardinality
is bounded; source checkpoints grow with process restarts and currently require
operator-managed retention. Do not delete checkpoints for active processes, as
their next snapshot would be counted twice.

## Audit records

Successful route changes, key creation/revocation and administrator session
creation/deletion insert an audit event in the same PostgreSQL transaction. An
audit failure rolls back the operation. Retried revocations do not create extra
events. Rows contain a stable administrator ID, fixed action, resource ID and
timestamp; they omit credentials, display names, request bodies and IP addresses.

The dashboard's Audit log uses descending-ID cursor pagination, 50 records per
page. The primary key supplies the pagination index. The database rejects UPDATE,
DELETE and TRUNCATE through an append-only trigger. A privileged database owner
can change the trigger/schema, so this is not a tamper-proof external ledger.
Failed login attempts remain in structured application logs. An independent
restricted audit exporter and retention policy remain future work.

## Deferred regional redundancy — not provisioned

The owner chose to retain the current low-cost deployment on 2026-10-01. Keep
Cloud Run at minimum 0 / maximum 1, zonal shared-core PostgreSQL and Basic Redis.
The existing $100 monthly planning alerts remain unchanged. The alternative below
is a reference plan only; its higher cost is not approved.

This plan retains the existing region and approved administrator allowlist:

| Component | Proposed configuration | Approximate baseline/month |
| --- | --- | ---: |
| Cloud SQL | PostgreSQL 17 Enterprise, regional HA, 1 vCPU / 3.75 GiB (`db-custom-1-3840`), 10 GiB SSD | $100.75 |
| Redis | New Standard 1 GiB instance, AUTH, verified TLS, private VPC, automatic failover | $62.78 |
| Cloud Run | Minimum 2 / maximum 2 instances; existing five containers per instance, total 10 vCPU and 2 GiB, request-based idle billing | $78.84 idle |
| Total | Before active traffic, backups, networking, builds, logs, secrets and tax | **$242.37** |

Estimate retrieved 2026-10-01 from Google's Cloud Billing Catalog, USD on-demand
SKUs applicable to `europe-west1`, using 730 hours/month and no credits/discounts.
SQL regional CPU is $0.0826/hour, RAM $0.014/GiB-hour, SSD $0.34/GiB-month;
Redis Standard M1 Belgium is $0.086/GiB-hour; Run idle minimum CPU and memory
are each $0.0000025 per allocated unit-second. Active Run CPU is billed at a
higher rate. A **$300 monthly planning budget** offers modest headroom for light
usage, but is not a cap or guarantee. Existing $100 approval does not cover this
plan. No HA resources should be provisioned before the increased cost is approved.

Migration sequence after approval:

1. Capture the current immutable image, secret versions, route revision and
   managed backup. Keep deletion protection and existing PITR retention.
2. Upgrade SQL to the reviewed dedicated-core regional configuration. Expect
   interrupted connections during the change; use a maintenance window and
   verify readiness, key authorization, audit writes and route persistence.
3. Create a separate Standard Redis instance on the same VPC with AUTH/TLS.
   Basic cannot be upgraded in place. Store the new URL and CA as new secret
   versions, preserving the previous versions for rollback.
4. Coordinate a single Redis cutover. Do not split traffic across old/new quota
   stores: their counters are independent. Allow the longest configured quota
   window to elapse while protected traffic is paused, then start new traffic.
   Cache entries may start cold. Login throttles also reset with the new store.
5. Render a new Run revision with those pinned secret versions, minimum 2 and
   maximum 2 at both service and revision levels. Verify shared quotas, metrics,
   session revocation and readiness across replicas. Minimum instances reduce
   cold starts; they do not guarantee a specific cross-zone placement or protect
   against region-wide outages.
6. Retire old Redis only after successful observation and explicit cleanup review.
   The overlap temporarily adds its existing cost. Keep automated GitHub
   deployment disabled. Monitor actual billing against the approved plan.

This adds regional resilience. It is not a multi-region deployment; regional
disaster recovery still needs a separately rehearsed restore and routing plan.

## Managed backup recovery rehearsal

Use a fresh managed backup after the intended application migrations. Record its
completion time, source configuration and database revision. Restore only into a
new, explicitly named disposable SQL instance; never restore over production.
Keep its public authorized-networks list empty, use the authenticated SQL
connector, and do not attach any public application traffic.

Verify schema, route revision and route checksum, key counts/revocation/expiry,
administrator bindings, shared metric totals and audit counts/ordering. Compare
with a consistent pre-backup checkpoint or account for writes after that point.
Delete restored session and pending-login rows before allowing any traffic, so
old sessions cannot become usable again. Do not print key hashes, URLs with
passwords, email bindings or session data in the test report.

Record RPO from the verified recovery point and RTO from restore initiation to
successful application checks. A successful backup API status alone does not
prove recovery. Dispose of only the named rehearsal instance after verification;
preserve the source and backup. Managed restore and managed failover rehearsal
are **pending**, and no RPO/RTO guarantee is claimed. The existing disposable
PostgreSQL logical dump/restore tests exercise a different failure boundary.

References: [Billing Catalog API](https://cloud.google.com/billing/v1/how-tos/catalog-api),
[Cloud SQL pricing](https://cloud.google.com/sql/pricing),
[Redis tiers](https://docs.cloud.google.com/memorystore/docs/redis/redis-tiers),
[Cloud Run pricing](https://cloud.google.com/run/pricing),
[Cloud SQL HA](https://docs.cloud.google.com/sql/docs/postgres/high-availability),
[managed restore](https://docs.cloud.google.com/sql/docs/postgres/backup-recovery/restore).
