# Health

Readiness and liveness checks for a service and its dependencies.

```go
health.Register("db", health.PingContextCheck(db.PingContext))         // critical, 5s timeout
health.Register("redis", health.TCPCheck("redis:6379", time.Second))
health.RegisterCheck(health.Check{Name: "search", Check: health.HTTPCheck("http://search/ping", time.Second), Critical: false})
health.SetInfo("version", "1.4.2")
```

- **Built-in checks.** `PingCheck` / `PingContextCheck` (databases), `TCPCheck`, `HTTPCheck`, `DNSCheck`, `RegistryCheck` (the service's registry answers) and `CustomCheck`.
- **Readiness.** Every critical check must pass. Non-critical checks are reported but do not fail readiness. Checks run concurrently, each with its own timeout.
- **Liveness.** The process answers. A failing dependency should not get a process restarted.

## Where the checks are served

| How | Readiness | Liveness |
|---|---|---|
| **gRPC server** (`server/grpc`), automatically | `grpc.health.v1` Check with service `""` or the service name | the same, with service `liveness` |
| **HTTP**, with `health.RegisterHandlers(mux)` | `GET /health/ready` (and `/health` with the details) | `GET /health/live` |

On the gRPC server, results are cached for one second. Readiness turns `NOT_SERVING` as soon as the server starts stopping. See [server](../server#health-checks-grpc-server).

```yaml
# Kubernetes, gRPC service on :9001
readinessProbe:
  grpc: {port: 9001}
livenessProbe:
  grpc: {port: 9001, service: liveness}
```

The same endpoint answers load balancers, [`micro-inspect`](../registry/inspect) and probing tools such as Uptime Kuma or `grpc_health_probe`.
