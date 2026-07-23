# Metadata

Request-scoped key/value context — how per-request metadata (auth tokens, trace ids, tenant ids) travels with an RPC.

## Concept

- `metadata.Metadata` is a case-insensitive `map[string]string` carried in `context.Context` (`NewContext/FromContext`, `Get/Set`).
- The client serializes it into transport headers (`Micro-*`), the server restores it into the handler's context — so metadata **propagates across hops** transparently.

```
service A                     wire                      service B
ctx{Tenant-Id: t1}  ──▶  Message.Header ──▶  ctx{Tenant-Id: t1}  ──▶  next hop...
     (client injects)     Micro-*/custom       (server restores)
```
- This is the substrate wrappers use: tracing injects span ids here, auth reads tokens from here.

```go
ctx = metadata.Set(ctx, "Tenant-Id", "t1")     // client side
tenant, _ := metadata.Get(ctx, "Tenant-Id")     // server handler
```
