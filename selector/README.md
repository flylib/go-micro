# Selector

Node selection — given the registry's answer "these nodes host service X", pick which node this request goes to. This is the client-side load balancer.

## Concept

- `Select(service)` returns a `Next` func the client calls per attempt — retries naturally rotate to other nodes.

```
Registry.GetService("orders")            [v1: n1 n2 n3] [v2: n4]
        │
        ├─ Filters      prune candidates (by version/metadata/endpoint)
        ├─ Strategy     round-robin | random | custom
        ▼
      Next() ──▶ n2      (client calls Next once per attempt;
      Next() ──▶ n3       a retry naturally lands on another node)
```
- Strategies: round-robin (default) and random ship in-package; a `Strategy` is just `func([]*registry.Service) Next`, so custom balancers are one function.
- `Filter`s prune candidates before strategy runs (by version, metadata, endpoint).
- Marks nodes success/failure (`Mark`) so implementations can blacklist flapping nodes.

```go
client.NewClient(client.Selector(selector.NewSelector(
    selector.SetStrategy(selector.Random),
)))
```
