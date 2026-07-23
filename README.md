# Go Micro [![Go.Dev reference](https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white&style=flat-square)](https://pkg.go.dev/github.com/flylib/go-micro?tab=doc)

Go Micro is a pluggable framework for building microservices in Go.

You write services; the framework gives them service discovery, RPC, and pub/sub out of the box. Every moving part — how services find each other, how they talk, how messages are encoded, where state is stored — is a Go interface with a default implementation you can swap without touching business code.

## Contents

- [Core Concepts](#core-concepts)
- [Quick Start](#quick-start)
- [Writing a Service](#writing-a-service)
- [Multi-Service Projects](#multi-service-projects)
- [Pluggable Backends](#pluggable-backends)
- [Data Model](#data-model)
- [CLI](#cli)
- [Examples](#examples)

## Core Concepts

### Service — the unit of work

A **Service** is the top-level handle you build everything around. It wires together the pluggable pieces below, registers itself for discovery, and manages its own lifecycle (`Init` → `Run` → graceful stop on signal/context).

```go
service := micro.New("greeter")   // create
service.Handle(new(Say))          // register RPC handlers
service.Run()                     // start, block until signalled, then stop
```

Each service owns its own client, server, store, and cache, so several services can run in one binary (see [Multi-Service Projects](#multi-service-projects)).

### The pluggable abstractions

Everything the framework does is expressed as an interface with a default implementation and a functional-options constructor. Pick a different backend by passing a different option — no business-code changes.

| Abstraction | Responsibility | Default | Other backends |
|---|---|---|---|
| **Registry** | Service discovery — register/lookup service nodes | mDNS | Consul, etcd, NATS, Nacos |
| **Broker** | Asynchronous pub/sub messaging | HTTP | NATS, RabbitMQ, Kafka, memory |
| **Transport** | Point-to-point synchronous communication (the RPC pipe) | HTTP | gRPC, NATS |
| **Client** | Makes RPC calls (retries, timeouts, streaming) | RPC | gRPC |
| **Server** | Serves RPC handlers and subscribers | RPC | gRPC |
| **Selector** | Picks one node from Registry results (load balancing) | round-robin | — |
| **Codec** | Encodes/decodes messages | protobuf/JSON | grpc, bytes, jsonrpc, text |
| **Store** | Key-value persistence | file (bbolt) | Postgres, MySQL, NATS JetStream KV |
| **Config** | Dynamic configuration from sources | — | env, file, flag, CLI, NATS, memory |
| **Model** | Typed data layer (CRUD + queries) | memory | SQLite, Postgres |
| **Logger** | Structured logging | built-in (slog-style) | slog, Zap, zerolog |

Supporting pieces: **Auth** (accounts/JWT), **Cache**, **Events** (JetStream streams), **Metadata** (request context), **Wrapper** (client/server middleware), **Logger**, and **Debug** (profile/trace/health).

### How a request flows

An RPC call travels through the abstractions in order:

```
Client.Call
  → Selector picks a node from the Registry
  → Transport opens a connection and sends the Message
  → Server receives it
  → Codec decodes the body
  → Wrapper middleware chain runs
  → your Handler executes
  → Codec encodes the reply back
```

Because each hop is an interface, you can change the wire protocol (HTTP → gRPC → NATS), the discovery backend, or add middleware without rewriting handlers.

### Message & headers

The unit that moves across any Transport is a `Message`:

```go
type Message struct {
    Header map[string]string // metadata
    Body   []byte            // payload
}
```

The header keys are standardized in the `transport/headers` package (`Micro-Service`, `Micro-Endpoint`, `Micro-Topic`, `Micro-Error`, `Micro-Trace-ID`, …). Every transport (HTTP, gRPC, NATS) and the broker/server use the **same** key names, so client, server, and middleware read request metadata the same way regardless of the underlying wire protocol. `transport/headers` is not a transport backend — it's the shared vocabulary the backends fill in.

### Functional options

Configuration everywhere uses the functional-options pattern: `Option func(*Options)`. Options hold pointers to the pluggable pieces so they can be swapped at runtime, and setting a top-level option (e.g. `Registry`) cascades down to the client, server, and broker so the components stay consistent.

```go
service := micro.New("greeter",
    micro.Address(":8080"),
    micro.Registry(consul.NewRegistry()),   // swaps discovery for the whole service
)
```

## Quick Start

Install the CLI:

```bash
go install github.com/flylib/go-micro/cmd/micro@latest
```

Scaffold a service, run it, call it:

```bash
micro new helloworld
cd helloworld
micro run
```

In another terminal, call it through the HTTP API gateway that `micro run` starts on `:8080`:

```bash
curl -X POST http://localhost:8080/api/helloworld/Helloworld.Call \
  -H 'Content-Type: application/json' -d '{"name":"World"}'
```

`micro run` gives you:

```
# Dashboard:  http://localhost:8080
# API:        http://localhost:8080/api/{service}/{method}
# Health:     http://localhost:8080/health
```

## Writing a Service

A service is a struct with methods. Each exported method with the `(ctx, *Request, *Response) error` shape becomes an RPC endpoint.

```go
package main

import (
    "context"

    "github.com/flylib/go-micro"
)

type Request struct {
    Name string `json:"name"`
}

type Response struct {
    Message string `json:"message"`
}

type Say struct{}

// Hello greets a person by name.
func (h *Say) Hello(ctx context.Context, req *Request, rsp *Response) error {
    rsp.Message = "Hello " + req.Name
    return nil
}

func main() {
    service := micro.New("greeter")
    service.Handle(new(Say))
    service.Run()
}
```

Scaffold from a template instead:

```bash
micro new helloworld               # default template
micro new contacts --template crud # crud / pubsub / api
```

## Multi-Service Projects

Run several services in one binary with a shared lifecycle:

```go
users  := micro.New("users",  micro.Address(":9001"))
orders := micro.New("orders", micro.Address(":9002"))

users.Handle(new(Users))
orders.Handle(new(Orders))

g := micro.NewGroup(users, orders)
g.Run() // all start together, all stop together on signal
```

Or describe a multi-service project with a `micro.mu` file (services start in dependency order):

```
service users
    path ./users

service orders
    path ./orders
    depends users
```

## Pluggable Backends

Swap any abstraction by importing a backend and passing it as an option — the rest of your code is unchanged.

```go
import (
    "github.com/flylib/go-micro"
    "github.com/flylib/go-micro/registry/consul"
    "github.com/flylib/go-micro/transport/grpc"
    "github.com/flylib/go-micro/broker/nats"
)

service := micro.New("orders",
    micro.Registry(consul.NewRegistry()),
    micro.Transport(grpc.NewTransport()),
    micro.Broker(nats.NewBroker()),
)
```

- **Registry:** mDNS (default), Consul, etcd, NATS, Nacos
- **Broker:** HTTP (default), NATS, RabbitMQ, Kafka, memory
- **Transport:** HTTP (default), gRPC, NATS
- **Store:** file/bbolt (default), Postgres, MySQL, NATS JetStream KV
- **Config sources:** env, file, flag, CLI, NATS, memory
- **Logger:** built-in (default), slog, Zap, zerolog

## Data Model

Typed persistence with CRUD and queries on top of the store:

```go
type User struct {
    ID    string `json:"id" model:"key"`
    Name  string `json:"name"`
    Email string `json:"email" model:"index"`
}

db := service.Model()
db.Register(&User{})
db.Create(ctx, &User{ID: "1", Name: "Alice", Email: "alice@example.com"})

var results []*User
db.List(ctx, &results, model.Where("email", "alice@example.com"))
```

Backends: memory (default), SQLite, Postgres.

## CLI

| Command | Purpose |
|---|---|
| `micro new myservice` | Scaffold a service (`--template crud/pubsub/api`) |
| `micro run` | Dev mode: hot reload + HTTP API gateway + dashboard |
| `micro server` | Production mode: gateway with auth + dashboard |
| `micro call service Endpoint '{}'` | Call a service from the CLI |
| `micro services` | List registered services |
| `micro describe service` | Show a service's endpoints |
| `micro gen proto` | Generate code from `.proto` (needs protoc + protoc-gen-micro) |
| `micro build` | Compile production binaries |
| `micro deploy user@server` | Deploy via SSH + systemd |

## Examples

- [hello-world](examples/hello-world/) — basic RPC service
- [multi-service](examples/multi-service/) — multiple services in one binary
- [web-service](examples/web-service/) — HTTP service via the web package
- [auth](examples/auth/) — authentication
- [graceful-stop](examples/graceful-stop/) — clean shutdown
- [grpc-interop](examples/grpc-interop/) — call go-micro from any gRPC client
- [smoke](examples/smoke/) — end-to-end smoke tests (core + nacos via podman)
- [deployment](examples/deployment/) — deploying services

See [all examples](examples/README.md).

Package reference: https://pkg.go.dev/github.com/flylib/go-micro

## Development

The repo is a multi-module workspace (core + one module per pluggable backend + examples), wired together by [go.work](go.work) — open the repo root in your IDE and every module resolves against local sources, no `replace` juggling needed while developing or debugging.

```bash
go build ./...                    # build the core (workspace mode)
cd examples/smoke && go test .    # end-to-end smoke tests
GOWORK=off go build ./...         # build a module standalone (as consumers see it)
```

Each plugin module also carries its own `replace github.com/flylib/go-micro => ../..` so it still builds standalone outside the workspace.
