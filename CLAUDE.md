# CLAUDE.md - Go Micro Project Guide

## Project Overview

Go Micro (`github.com/flylib/go-micro`) is a framework for distributed systems development in Go. It provides pluggable abstractions for service discovery, RPC, pub/sub, config, auth, storage, locks and more.

This repository is a fork of `micro/go-micro`. In v5.30.0 the fork removed upstream's AI layer (agents, LLM providers, flows, MCP/A2A gateways). It renamed the module and split pluggable backends into per-plugin modules. Do not reintroduce AI/MCP code or docs, and do not use the upstream `go-micro.dev/v5` import path.

The main users are application teams that run all services on gRPC (`server/grpc` + `client/grpc`). They register in Nacos, etcd or Consul, and enter through one of the two edge gateways in `gateway/`.

## Build & Test

The repo is a multi-module workspace (`go.work`): the core module plus one module per plugin, the gateways and the examples. All modules declare `go 1.26.0`.

```bash
make test                       # core module tests
go test ./gateway/proxy/...     # a plugin module, in workspace mode
cd store/redis && GOWORK=off go test ./...   # a module standalone, as consumers build it
make lint
make fmt
go build -o micro ./cmd/micro   # CLI
```

- **Proxy variables.** `HTTP(S)_PROXY` in the environment breaks the gRPC and web tests (`client/grpc`, `transport/grpc`, `web`). Run them with the variables unset.
- **Gateway suites.** The gateway conformance suite and the OpenResty Lua tests need Docker. See `gateway/conformance/README.md` and `gateway/openresty/README.md`.

## Project Structure

```
go-micro/
├── auth/           # Authentication (JWT in auth/jwt, no-op)
├── broker/         # Message broker (HTTP, memory; NATS, RabbitMQ, Kafka modules)
├── cache/          # Caching (memory; Redis module)
├── client/         # RPC client (mucp; gRPC module)
├── cmd/
│   ├── micro/                     # CLI: new, run, server, call, describe, build, deploy
│   ├── protoc-gen-micro/          # protobuf code generation for handlers and clients
│   └── protoc-gen-micro-gateway/  # gateway http_rules from google.api.http annotations
├── codec/          # Message codecs (JSON, Proto)
├── config/         # Dynamic config (env, file, flag; etcd, Consul, Nacos, NATS modules)
├── errors/         # go-micro JSON errors
├── events/         # Event streams (memory; NATS JetStream module)
├── gateway/
│   ├── SPEC.md, rules.schema.json   # edge gateway contract and rules format
│   ├── proxy/      # Go edge gateway (gRPC + HTTP/JSON + REST transcoding + WebSocket)
│   ├── openresty/  # OpenResty edge gateway with go-micro adapter Lua libraries (forwards /ws to the Go gateway)
│   ├── push/       # push.ToUser / push.ToTopic for WebSocket clients (core module)
│   ├── conformance/# black-box suite both gateways must pass
│   └── api/        # HTTP shell used by `micro run` / `micro server` dashboards
├── health/         # Health checking
├── logger/         # Logging (slog, zap, zerolog modules)
├── metadata/       # Context metadata
├── model/          # Typed data models (memory; SQLite, Postgres, MongoDB modules)
├── registry/       # Service discovery (mDNS, memory; etcd, Consul, Nacos, NATS modules)
├── selector/       # Client-side load balancing
├── server/         # RPC server (mucp; gRPC module)
├── service/        # Service interface
├── store/          # KV persistence (file, memory; Postgres, MySQL, NATS KV, Redis, MongoDB modules)
├── sync/           # Locks and leader election (memory; etcd, Redis modules)
├── transport/      # Network transport (HTTP; gRPC, NATS modules)
├── web/            # Web service helpers (SSE)
├── wrapper/        # Middleware (auth, logging, trace, metrics, rate limit, breaker, ...)
├── examples/       # Working examples
└── internal/       # Non-public: release script, test helpers, utils
```

## Key Architectural Decisions

- **Plugin architecture**: All abstractions use Go interfaces. Defaults work out of the box, everything is swappable.
- **Per-plugin modules**: Each backend is its own module, so importing one does not pull in every SDK. In-repo requires are pinned to a pushed commit's pseudo-version (`internal/scripts/release-modules.sh --pseudo <commit>`). Each module also keeps `replace github.com/flylib/go-micro => ../..` so it builds standalone in the repo. There are no release tags.
- **Gateways follow one contract**: `gateway/SPEC.md` and `rules.schema.json` define behaviour, and `gateway/conformance` enforces it. Any gateway change goes into the SPEC, both implementations and the suite together. Keep the schema copies in `proxy/`, `conformance/` and `openresty/lib/resty/micro/` identical to `gateway/rules.schema.json`.
- **gRPC routing convention**: a service's proto package equals its registry name, so `/<service>.<Handler>/<Method>` needs no route.
- **Reflection-based registration**: Handlers are registered via reflection for minimal boilerplate. Doc comments on handler methods become endpoint metadata (`server/comments.go`).

## Code Conventions

- Standard Go conventions (gofmt, golint)
- Functional options pattern for configuration (`WithX()` functions)
- Interface-first design: define the interface, then implement
- Tests alongside code (not in separate test directories)
- Commit messages: `type(scope): description` in imperative mood, e.g. `feat(store): add redis store`

## Key Files

| Purpose | File |
|---------|------|
| Gateway contract | `gateway/SPEC.md` |
| Gateway rules schema / example | `gateway/rules.schema.json`, `gateway/rules.example.yaml` |
| Go gateway | `gateway/proxy/proxy.go`, `gateway/proxy/http.go`, `gateway/proxy/httprules.go` |
| OpenResty gateway | `gateway/openresty/lib/resty/micro/` |
| Model layer | `model/model.go` |
| CLI entry | `cmd/micro/main.go` |
| Server (run/server) | `cmd/micro/server/server.go` |
| Module pinning | `internal/scripts/release-modules.sh` |
| Changelog | `CHANGELOG.md` |
| Roadmap | `ROADMAP.md` |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Key points:
- Open an issue before large changes
- Include tests for new features
- Run `make test` and `make lint` before submitting
