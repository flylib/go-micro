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
  │     └─ on error → backoff → retry with the NEXT node (≤ Retries)
  └─ Codec.Unmarshal(rsp)
```

- `Call` flow: resolve via [registry](../registry) → pick a node via [selector](../selector) → encode via [codec](../codec) → send via [transport](../transport).
- **Resilience built in**: `Retries` (default 5) with a pluggable `RetryFunc` (default retry-on-error), exponential backoff, and each attempt selects a **different node**; `RequestTimeout` default 30s.
- Also does pub/sub publishing (`Publish`) and streaming (`Stream`).
- Wrap cross-cutting behaviour with `client.Wrapper` / `CallWrapper` — see [wrapper](../wrapper).

```go
req := svc.Client().NewRequest("orders", "Orders.Get", &GetReq{Id: "1"})
var rsp GetRsp
err := svc.Client().Call(ctx, req, &rsp)
```

Implementations: RPC (default) · gRPC (`client/grpc`, own module).
