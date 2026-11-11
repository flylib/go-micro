# Client

The outbound half of RPC — makes requests to other services by **name**, with discovery, load balancing, retries and timeouts built in.

## Concept

```
Call("orders", "Orders.Get", req)
  │
  ├─ Registry.GetService("orders")      resolve name → nodes
  ├─ Selector.Select(...)               pick one node (round-robin)
  ├─ Codec.Marshal(req)                 encode body per Content-Type
  ├─ Transport.Dial(node).Send(msg)     wire
  │     └─ node unreachable → retry on ANOTHER node (≤ Retries)
  └─ Codec.Unmarshal(rsp)
```

- `Call` flow: resolve via [registry](../registry) → pick a node via [selector](../selector) → encode via [codec](../codec) → send via [transport](../transport).
- **Resilience built in**: `Retries` (default 5) with a pluggable `RetryFunc`, and `RequestTimeout` default 30s.
- **What is retried by default** (`RetryOnConnect`): only calls that never reached a node, because no connection could be made. Such an error has the id `go.micro.client.connect` and code 503 (`client.IsConnectError`).
  - The retry goes straight to a node not yet tried, whatever the selector strategy, without backoff.
  - Once every node has failed, the call fails at once.
  - A request that was sent is **not** retried, since the service may have acted on it.
- **Opting in to more**: `client.Retry(client.RetryOnError)` also retries timeouts (408), with exponential backoff. Use it only for idempotent endpoints, per client or per call (`client.WithRetry`).
- Also does pub/sub publishing (`Publish`) and streaming (`Stream`).
- Wrap cross-cutting behaviour with `client.Wrapper` / `CallWrapper` — see [wrapper](../wrapper).

```go
req := svc.Client().NewRequest("orders", "Orders.Get", &GetReq{Id: "1"})
var rsp GetRsp
err := svc.Client().Call(ctx, req, &rsp)
```

Implementations: RPC (default) · gRPC (`client/grpc`, own module).
