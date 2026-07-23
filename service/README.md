# Service

The top-level handle of the framework — a Service assembles the pluggable pieces (registry, broker, transport, client, server, store, …) and manages their shared lifecycle.

## Concept

- One Service = one microservice: it owns its **own** client, server, store and cache, so several services can run in a single binary (`service.NewGroup`).

```
                         Service ("orders")
        ┌───────────────────┼──────────────────────┐
   outbound             inbound                 shared parts
   Client ──▶ RPC out   Server ◀── RPC in       Registry (discovery)
   (selector,           (handlers,              Broker   (pub/sub)
    retries)             subscribers)           Transport, Store, Cache,
                                                Config, Auth, Logger
```
- Lifecycle: `New → Handle → Run`. `Run` starts the server, registers with the registry, blocks on signal/context, then deregisters and stops.
- Top-level options **cascade**: `service.Registry(r)` also re-initialises the client, server and broker so every component agrees on discovery.
- Its `Store` is automatically scoped to `service/<name>` so co-hosted services never share keys.

## Usage

```go
svc := micro.New("orders",
    service.Registry(nacos.NewRegistry()),
    service.Broker(kafka.NewBroker()),
)
svc.Handle(new(OrdersHandler))
svc.Run()
```

Related: [server](../server) (inbound), [client](../client) (outbound), [registry](../registry) (discovery).
