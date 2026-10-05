# Go Micro Examples

This directory contains runnable examples demonstrating various go-micro features and patterns.

## Quick Start

Most examples run with `go run .` from their directory. [auth](./auth/) and [grpc-interop](./grpc-interop/) have separate `server/` and `client/` programs (run each with `go run .` from its subdirectory), and [smoke](./smoke/) is a test suite run with `go test`.

## Examples

### [hello-world](./hello-world/)
Basic RPC service demonstrating core concepts:
- Service creation and registration
- Handler implementation (`Greeter.Hello`)
- Calling it with `micro call` or `curl`

**Run it:**
```bash
cd hello-world
go run .
```

### [web-service](./web-service/)
HTTP web service with service discovery:
- HTTP handlers
- Service registration
- Health checks
- JSON REST API

**Run it:**
```bash
cd web-service
go run .
```

### [multi-service](./multi-service/)
Multiple services in a single binary — the modular monolith pattern:
- Isolated server, client, store, and cache per service
- Shared registry and broker for inter-service communication
- Coordinated lifecycle with `service.Group`
- Start monolith, split later when you need to scale independently

**Run it:**
```bash
cd multi-service
go run .
```

### [auth](./auth/)
Protecting services with authentication and authorization:
- Pluggable auth provider (`noop.NewAuth()` in the example; swap in `auth/jwt` for real tokens)
- Auth wrappers on server and client
- Endpoint scopes

### [graceful-stop](./graceful-stop/)
Shutdown behaviour of the gRPC server: in-flight calls finish before the process exits.

### [grpc-interop](./grpc-interop/)
A plain `google.golang.org/grpc` client calling a go-micro service, with no go-micro imports on the client side.

### [smoke](./smoke/)
End-to-end smoke tests, runnable as a standalone module: the core in-process, plus Nacos integration.

For edge gateways (gRPC, HTTP/JSON and REST transcoding in front of services), see [gateway/](../gateway).

## Prerequisites

Some examples require external dependencies:

- **NATS**: `docker run -p 4222:4222 nats:latest`
- **Consul**: `docker run -p 8500:8500 consul:latest agent -dev -ui -client=0.0.0.0`
- **Redis**: `docker run -p 6379:6379 redis:latest`
- **Nacos**: `docker run -p 8848:8848 -p 9848:9848 -e MODE=standalone nacos/nacos-server:latest`

## Contributing

To add a new example:

1. Create a new directory
2. Add a descriptive README.md
3. Include working code with comments
4. Add to this index
5. Ensure it runs with `go run .`
