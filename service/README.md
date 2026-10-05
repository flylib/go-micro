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
- **Order does not matter.** A server, client or broker passed with `service.Server`, `service.Client` or `service.Broker` gets the service-level settings given before it. These are name, version, address, metadata, registry, broker, transport, selector, wrappers and handlers, including ones added with `svc.Handle`. Service-level settings win over the component's own.
- Its `Store` is automatically scoped to `service/<name>` so co-hosted services never share keys.

## Usage

```go
svc := micro.New("orders",
    service.Registry(nacos.NewRegistry()),
    service.Broker(kafka.NewBroker()),
    service.Server(grpcserver.NewServer()), // keeps the name, registry and broker above
    service.Address(":9001"),
)
svc.Handle(new(OrdersHandler))
svc.Run()
```

Related: [server](../server) (inbound), [client](../client) (outbound), [registry](../registry) (discovery).
