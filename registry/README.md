# Registry

Service discovery — where services announce themselves and find each other.

## Concept

- A `Service` record carries `Name`, `Version`, `Metadata` and its `Nodes` (id + address). Register/Deregister happen automatically inside `server.Start/Stop`, refreshed on a TTL interval.
- `GetService(name)` returns one `*Service` **per version**, each with its live nodes; the [selector](../selector) load-balances across them.
- `Watch` streams change events so caches and selectors stay warm without polling.

```
service "orders"                       registry                     consumers
server.Start ── Register(node) ────▶  orders/v1: [n1, n2]  ◀──── GetService("orders")
   │ every RegisterInterval             │                            ▲
   └─ re-Register (TTL refresh)         └── change events ──▶ Watch (cache/selector)
server.Stop ─── Deregister(node) ───▶  node removed
```

## Implementations

| Backend | Module | Notes |
|---|---|---|
| mDNS | built-in (default) | zeroconf, LAN/dev only |
| memory | built-in | tests, single process |
| Consul / etcd / NATS / Nacos | `registry/<name>` (own go.mod) | production discovery; Nacos: see [registry/nacos](nacos) for ports and auth |
| cache | `registry/cache` | read-through cache wrapping any registry |

```go
reg := nacos.NewRegistry(registry.Addrs("10.0.0.1:8848"))
micro.New("orders", service.Registry(reg))
```
