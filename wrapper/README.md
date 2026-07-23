# Wrapper

Middleware/interceptors — cross-cutting logic wrapped around the RPC path without touching business code (the framework's AOP layer).

## Concept

Every wrapper is a decorator: take the next layer, return a wrapped one.

### Where each wrapper sits

```
client side                                server side
Client.Call                                request arrives
  │                                          │
  │ ① Wrapper — wraps the whole Client       │ ④ HandlerWrapper — wraps each RPC handler
  │   (outermost, once per logical call)     │ ⑤ SubscriberWrapper — wraps each event
  │                                          │    subscriber (broker deliveries)
  │ ② CallWrapper — wraps one node attempt   ▼
  │   (after the Selector picked a node,   your Handler
  ▼    before the wire; runs per retry)
network ─────────────────────────────────▶
```

Retries re-enter at ② with a different node, so node-level logic (per-node
breakers, attempt counting) belongs in `CallWrapper`; call-level logic
(metadata injection, tracing spans, client-side breakers keyed by
service.endpoint) belongs in `Wrapper`.

| Type | Mount | Runs |
|---|---|---|
| `client.Wrapper` | `micro.WrapClient` | around the whole client (outermost, per logical call) |
| `client.CallWrapper` | `micro.WrapCall` | per attempt, **after** node selection — node-level logic |
| `server.HandlerWrapper` | `micro.WrapHandler` | around every RPC handler |
| `server.SubscriberWrapper` | `micro.WrapSubscriber` | around every event subscriber |

Wrappers nest onion-style in registration order: `W1(W2(handler))`. Code before `next(...)` is the pre-hook, after it the post-hook; refusing to call `next` implements breakers/limiters.

## In-tree wrappers

[auth](auth) (credentials both sides) · [monitoring/prometheus](monitoring/prometheus) (request metrics) · [trace/opentelemetry](trace/opentelemetry) (spans + propagation) · [breaker/hystrix](breaker/hystrix) (circuit breaking) · [ratelimiter](ratelimiter) (token-bucket & leaky-bucket limits) · [governance/sentinel](governance/sentinel) (Sentinel flow control + circuit breaking + system protection) · [shedding](shedding) (adaptive CPU-based load shedding, go-zero style) · [deadline](deadline) (cross-hop deadline propagation, in core) — heavy-dep wrappers are standalone modules.
