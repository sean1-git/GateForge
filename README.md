# GateForge | API Gateway & Developer Platform

A small API gateway written in Go. Start with one working request path, then add one feature at a time as this grows into a distributed gateway and developer platform.

GateForge aims to provide an infrastructure layer that controls how APIs are exposed, secured, and monitored. Authentication, HTTPS, rate limiting, multi-backend routing, caching, and richer logging are future capabilities; the current version implements the single-backend gateway described below.

As a web application grows into multiple backend services, each service should not have to independently handle authentication, rate limits, routing, logging, security, and failures. GateForge provides one controlled entry point that manages those concerns for the entire system.

```text
client -> GateForge (:8080) -> your backend (:9000)
```

The first version uses only the Go standard library. It forwards requests to one configured backend, exposes a local health endpoint, logs startup and proxy errors, and drains active HTTP requests on shutdown.

## Run locally

Install Go 1.22 or newer. Start the included example backend in one terminal:

```sh
go run ./examples/echo
```

Start the gateway in a second terminal:

```sh
go run ./cmd/gateway
```

Try it in a third terminal. On Windows PowerShell, use `curl.exe` in place of `curl`:

```sh
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}

curl "http://127.0.0.1:8080/hello?name=world"
# {"method":"GET","path":"/hello","query":"name=world"}

curl -X POST -d "hello" http://127.0.0.1:8080/messages
# {"method":"POST","path":"/messages","query":""}
```

The example reports request metadata; your real backend receives the original request body. Stop the gateway with Ctrl+C; active HTTP requests have up to ten seconds to finish.

If Go is installed but missing from PowerShell's `PATH`, add it for that terminal:

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
```

## Use your own backend

```sh
go run ./cmd/gateway -listen 127.0.0.1:8080 -upstream http://127.0.0.1:3000
```

| Flag | Default | Purpose |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8080` | Gateway HTTP address |
| `-upstream` | `http://127.0.0.1:9000` | Fixed backend URL, optionally with a path prefix |

The gateway preserves request methods, bodies, paths, valid query parameter values, and upstream response statuses and bodies. Standard proxy handling can normalize query encoding and key order, removes malformed query parameters, and removes hop-by-hop headers. The outbound `Host` is the backend's host. Incoming forwarding headers are replaced using the direct client's address, original Host, and connection scheme.

An upstream such as `http://127.0.0.1:3000/api` maps `/users` to `/api/users`. Upstream URLs cannot contain credentials, query strings, or fragments.

`GET /healthz` and `HEAD /healthz` are reserved for gateway liveness and never contact the backend. Other methods on that path return 405. A healthy gateway can still have an unavailable backend; proxy connection failures and response-header timeouts return 502 with `{"error":"bad gateway"}`. Errors after a response has started terminate the response.

The gateway allows five seconds to read incoming request headers, ten seconds to receive upstream response headers after sending a request, and sixty seconds for idle client connections. There is no total request deadline, so long-running response streams can continue. Shutdown does not drain upgraded connections such as WebSockets.

This is a local development foundation. The listener defaults to loopback. Public deployment, authentication, rate limits, and operational controls belong to later milestones.

## Build and verify

```sh
go test ./...
go vet ./...
go build -o bin/gateway ./cmd/gateway
```

On Windows, use `go build -o bin/gateway.exe ./cmd/gateway`.

Tests run their own local backends; no external services are needed.

If Go reports `error obtaining VCS status` because an unrelated parent Git repository is inaccessible, add `-buildvcs=false` immediately after `go run` or `go build`. This workspace was verified with that flag for executable builds.

## Grow one feature at a time

Complete and test each milestone before starting the next. Only milestone 1 is implemented.

1. **Working gateway:** one upstream, health endpoint, failure handling, shutdown, and integration tests.
2. **Static routing:** map path prefixes to backends with a small file configuration and explicit matching rules.
3. **API keys:** protect selected routes; cover missing, invalid, and rotated keys. Add transport security before carrying credentials over a network.
4. **Local rate limiting:** bound requests per key in memory; make the single-instance limitation explicit.
5. **Observability:** request IDs, access logs with sensitive fields excluded, and request/latency/error metrics.
6. **Multiple upstreams:** load balancing, health checks, and explicit retry rules for safe requests.
7. **Deployment:** introduce the packaging and edge TLS needed by the chosen host, then exercise graceful shutdown under load.
8. **Distributed operation:** coordinate configuration and limits across gateway instances; choose shared storage when its requirements are known.
9. **Developer platform:** add durable API/key management, usage reporting, and a developer portal once the gateway behavior is stable.

Keep routing and middleware in `internal/gateway`, process setup in `cmd/gateway`, and the demo backend in `examples/echo`. Split components into separate services only when a concrete scaling or operational need appears.
