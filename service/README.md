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

## From the environment

`service.FromEnv()` (`micro.FromEnv()`) sets the registry, broker, store and server address from `MICRO_*` variables, so one binary runs in every environment:

```go
import _ "github.com/flylib/go-micro/registry/nacos" // registers the plugin "nacos"

svc := micro.New("orders",
    micro.FromEnv(),          // MICRO_REGISTRY=nacos MICRO_REGISTRY_ADDRESS=nacos:8848 MICRO_SERVER_ADDRESS=:9001
    micro.Version("1.4.2"),   // options after FromEnv win over the environment
)
```

| Variable | Sets |
|---|---|
| `MICRO_REGISTRY`, `MICRO_REGISTRY_ADDRESS` | Registry plugin by name, and its comma-separated addresses |
| `MICRO_BROKER`, `MICRO_BROKER_ADDRESS` | Broker plugin and addresses |
| `MICRO_STORE`, `MICRO_STORE_ADDRESS`, `MICRO_STORE_DATABASE`, `MICRO_STORE_TABLE` | Store plugin, addresses, database and table |
| `MICRO_SERVER_ADDRESS`, `MICRO_SERVER_ADVERTISE` | Listen address, and the address registered for callers |
| `MICRO_SERVER_VERSION`, `MICRO_SERVER_METADATA` | Version, and `key=value,key=value` node metadata |

- **Plugins by name.** Built-in names (`mdns`, `memory`, `http`, `file`, ...) always work. A plugin module registers its name in `init` (`registry.RegisterPlugin`, `broker.RegisterPlugin`, `store.RegisterPlugin`, `transport.RegisterPlugin`), so it works once the package is linked in. An unknown name stops the service at start-up, and the error names the import to add. `<KIND>_ADDRESS` without the kind is an error too.
- **Precedence.** `FromEnv` is an option like any other: options after it win, options before it lose. It parses no command line flags, unlike `Service.Init`.
- **The gRPC preset.** [`service/grpc`](grpc) is `micro.New` on the gRPC server and client with `FromEnv` first: `grpcservice.New("orders")`.

Related: [server](../server) (inbound), [client](../client) (outbound), [registry](../registry) (discovery).
