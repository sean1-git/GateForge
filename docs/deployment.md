# Run locally and deploy to GCP

## Local full stack

Install Docker Desktop with Linux containers and Compose v2.24.4 or newer. From the repository directory:

```powershell
./scripts/setup-local.ps1
docker compose --profile scale up --build -d --wait
```

The setup script creates a gitignored `.env` with random PostgreSQL, Redis, and admin credentials; it refuses to overwrite an existing file. Keep it private. The Compose command starts HTTPS gateways on localhost ports 8443 and 8444, two users replicas, orders, catalog, PostgreSQL, and Redis. Certificates live in a Docker volume and are generated only when absent.

Copy the development certificate for command-line verification:

```powershell
New-Item -ItemType Directory -Force .local/certs
docker compose cp gateway:/certs/cert.pem .local/certs/cert.pem
curl.exe --cacert .local/certs/cert.pem https://localhost:8443/healthz
curl.exe --cacert .local/certs/cert.pem https://localhost:8443/catalog/items
```

The certificate is self-signed. For browser use, review and trust it in a development browser/profile, or supply a locally trusted development certificate. Never use the development certificate as a public production certificate. Open `https://localhost:8443/admin/` and enter `GATEFORGE_ADMIN_TOKEN` from your private `.env`. Add a key for `/users` and `/orders`; it is displayed once. Route configuration is seeded from `configs/stack.json` only when PostgreSQL has no saved configuration. Subsequent edits belong in the dashboard/admin API; changing the seed file does not overwrite the database.

JWT routes use `configs/jwt.example.json` as a starting point. Supply the issuer's RSA public PEM key, expected issuer/audience, and any required scope. For containers, mount that public key read-only and point `public_key_file` to its container path. The default stack demonstrates API keys; enabling JWTs requires your identity provider's values.

To stop the stack while retaining data:

```powershell
docker compose --profile scale down
```

Do not add `--volumes` unless you intentionally want to delete database, Redis, and certificate data. Renew the development certificate when it expires; existing cert files are preserved rather than silently replaced.

## Running Go directly

Go 1.25+ is required by the dependencies; the implementation was tested with Go 1.27.1. The dashboard build uses Node 24 and `npm ci` in `web`.

```powershell
go run ./cmd/devcert
cd web
npm ci
npm run build
cd ..
go run ./cmd/gateway -listen 127.0.0.1:8443 -config ./configs/routes.example.json -tls-cert .local/certs/cert.pem -tls-key .local/certs/key.pem
```

Set `DATABASE_URL`, `REDIS_URL`, and a random `GATEFORGE_ADMIN_TOKEN` in the process environment to enable persistence, Redis policies, and administration. This program does not automatically read `.env`; Compose does. PostgreSQL and Redis must already be running. For hosted services, use certificate-verified PostgreSQL transport and `rediss://` as appropriate. The Compose connection strings disable database TLS only on the isolated local container network.

## Validation

```powershell
go test -timeout 60s ./...
go vet ./...
cd web
npm test
npm run build
cd ..
```

Without `GATEFORGE_TEST_DATABASE_URL`, database integration tests are explicitly skipped. Point it to a disposable PostgreSQL database for persistence and two-gateway HTTPS tests. Tests create and remove uniquely named schemas. Set `GATEFORGE_TEST_REDIS_URL` to a dedicated Redis instance to run the two-gateway test against Redis; otherwise it uses the in-process Redis test server. The GitHub Actions workflow supplies real PostgreSQL and Redis services and runs race detection. It also builds/runs the Compose stack and checks failures; it becomes active after the changes are pushed.

With the default Compose stack, set the admin token in the environment without placing it in command history, then run:

```powershell
go run ./cmd/stackcheck
go run ./cmd/loadtest -url https://localhost:8443/catalog/items -ca .local/certs/cert.pem -seconds 10 -rate 10
```

`stackcheck` verifies TLS, required credentials, one quota shared across 8443/8444, shared public caching, and immediate revocation. It creates a short-lived test key and revokes it on exit. It assumes the unmodified sample routes and quota of 60 requests per minute. It sends the same public Host header to both gateway addresses when testing cache sharing.

For backend failure, stop `users1`, wait at least one health-check interval, rerun `stackcheck`, then start `users1`. For Redis outage, stop `redis`, run `stackcheck -phase redis-down`, then start Redis and wait for readiness. For database outage, stop `postgres`, run `stackcheck -phase database-down`, then start PostgreSQL. Restore every stopped service even when a test fails. These commands are for the disposable development stack, not a live deployment.

The load generator is GET-only and caps duration, rate and concurrency. It reports achieved request count, status counts, transport errors, and p95 latency. A 429 is an intentional rate-limit result, not a transport failure. It does not establish production capacity.

## Cloud Run deployment requirements

Cloud Run and the Compute Engine Compose deployment below are different deployment options. Cloud Run does not launch a Compose file or create PostgreSQL/Redis automatically.

The gateway listens on `0.0.0.0:$PORT` when the platform supplies `PORT`; an explicit `-listen` overrides it. Local runs without `PORT` retain the loopback default. Cloud Run terminates HTTPS before forwarding HTTP to the container, so set `GATEFORGE_TLS_OFFLOADED=true` for this deployment and omit TLS certificate/key variables. See Google's [container contract](https://docs.cloud.google.com/run/docs/container-contract).

Before deploying the complete gateway:

1. Publish the source including the root Dockerfile, frontend, dependencies and gateway packages to the branch used by Cloud Build. Reconcile remote commits first. A push to the existing `main` trigger starts a build/deployment automatically.
2. Prepare durable PostgreSQL and Redis endpoints with suitable network access and transport encryption. Store their URLs and a randomly generated admin token as secret references. Do not run PostgreSQL or Redis with ephemeral container storage as a substitute for persistence.
3. Supply `-config` with a route file whose upstreams actually exist. Compose names such as `users1` will not resolve on Cloud Run. Demo backends can be sidecars bound to distinct loopback ports, with only the gateway exposed. Separate IAM-protected Cloud Run backends require outbound Google identity-token support, which this gateway has not implemented yet; simply entering their URLs is insufficient.
4. Provide the JWT issuer, audience and mounted public key if enabling JWT routes. Keep administration behind its separate secret token.
5. Account for background probes/config refresh when choosing request-based or instance-based CPU allocation. Request-based CPU can pause these tasks between requests; always-allocated CPU has different billing implications.
6. Deploy with Google IAM access still required, test through authenticated invocation, and verify readiness, public sample data, credential rejection/acceptance and admin isolation. Grant public invocation only after these checks pass and public exposure is approved.

The repository currently prepares a Cloud Run-compatible listener, not a provisioned full Cloud Run stack. Database/cache provisioning, secrets, actual upstreams, cloud build and security validation still require deployment configuration and an approved spending budget.

## GCP Compute Engine deployment

Before execution, choose a billed GCP project, zone, public domain, and budget. Install/authenticate the Google Cloud CLI. The deploying identity needs permission to create the listed Compute Engine/network resources, use OS Login and IAP, and enable the required APIs. The VM has no attached service account. Pricing depends on region, disks, IP and network egress; billing alerts do not impose a hard spending cap.

Review the resource commands without creating anything:

```powershell
./infra/gcp/provision.ps1 -ProjectId YOUR-PROJECT-ID -Zone us-central1-a
```

After cost and resource approval, run the same command with `-Apply -AcceptCloudCosts`. This creates named resources once; it stops on conflicts rather than overwriting existing infrastructure. If a step fails, inspect which resources were created before retrying. The startup script installs Docker from its signed Debian repository. Wait for it to finish before deployment.

Prepare the VM over IAP SSH:

1. Point your domain at the reserved `gateforge-ip` address.
2. Obtain a trusted certificate for that domain, for example using an ACME DNS challenge. Place its certificate chain at `/opt/gateforge/certs/cert.pem` and its private key at `/opt/gateforge/certs/key.pem`. GateForge terminates TLS itself. Certificate issuance is not automated by these scripts.
3. Set the certificate directory ownership/group so UID/GID 10001 can traverse it and read the key: directory mode 0750, private key mode 0640, group 10001. Do not make the private key world-readable.
4. Create `/opt/gateforge/.env` with new random `POSTGRES_PASSWORD`, `REDIS_PASSWORD` and `GATEFORGE_ADMIN_TOKEN`. Use root ownership and mode 0600. Keep production secrets separate from local test credentials and out of Git and startup metadata.

Then upload/build/start the application:

```powershell
./infra/gcp/deploy.ps1 -ProjectId YOUR-PROJECT-ID -Zone us-central1-a
```

The deployment archive excludes environment files, keys, dependencies and generated assets. It contains the source needed for the Docker build. The GCP override publishes only port 443 and uses the supplied certificate, with a loopback health listener for container checks. Open `https://YOUR-DOMAIN/admin/` and run authenticated smoke checks before using real traffic. Repeat load/failure tests in a separate staging deployment.

## Operations and practical limits

- Back up PostgreSQL with `pg_dump` and store backups outside the VM; rehearse a restore. Container persistence is not a backup. Disk retention/deletion protection prevent some accidental deletion but do not create high availability.
- Schedule certificate renewal and replace cert/key files atomically. The gateway reloads valid replacements within a minute; monitor expiry and failed renewals.
- Export `/metrics` to a protected Prometheus-compatible collector if durable analytics are needed.
- Keep app/database/Redis credentials scoped to this deployment. Rotating a PostgreSQL password requires changing the database role as well as the app environment; changing `.env` alone does not update an existing database volume.
- The sample microservices are echo demos. Replace them with real services and review route permissions before production use.
- To remove the cloud deployment, first export needed data, disable VM deletion protection intentionally, and review the VM, retained disk, static IP, firewall and network resources. The scripts do not delete them automatically.
