# Server

The inbound half of RPC — listens on a transport, routes requests to registered handlers, dispatches broker events to subscribers.

## Concept

- `Handle` registers a struct; each exported `(ctx, *Req, *Rsp) error` method becomes an endpoint (`Struct.Method`). Endpoint metadata is published to the registry.
- Owns registration lifecycle: on `Start` it registers the node (TTL-refreshed), on `Stop` it deregisters and drains — graceful shutdown is built in.
- `Subscribe` binds queue/topic consumers through the same server so they share codecs and wrappers.
- Note the distinction: **service** is the whole assembled unit; **server** is only its request-receiving component (see the main README's Core Concepts).

Implementations: RPC (default) · gRPC (`server/grpc`, own module) · mock (tests).

## Health checks (gRPC server)

The gRPC server also serves the standard `grpc.health.v1.Health` service, so Kubernetes gRPC probes, load balancers and probing tools (Uptime Kuma, Gatus, grpc_health_probe) can check a service without any code.

| `service` in the request | Answers |
|---|---|
| `""` or the service name | **Readiness**: `SERVING` while every critical check registered with the [health](../health) package passes, for example `health.Register("db", health.PingCheck(db.Ping))`. `NOT_SERVING` as soon as the server starts stopping, so traffic drains while it deregisters. |
| `liveness` | **Liveness**: `SERVING` while the process runs, so a failing dependency does not restart the pod. |
| anything else | `NOT_FOUND` (`Watch`: `SERVICE_UNKNOWN`) |

- **Caching.** Results are cached for one second, so frequent probes do not hammer dependencies.
- **Watch.** `Watch` re-evaluates every 5 s.
- **Turning it off.** Disable it with `grpc.DisableHealth()`. An application that registers its own `grpc.health.v1.Health` keeps it.

```yaml
# Kubernetes
readinessProbe:
  grpc: {port: 9001}
livenessProbe:
  grpc: {port: 9001, service: liveness}
```
