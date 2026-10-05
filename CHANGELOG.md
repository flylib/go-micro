# Changelog

All notable changes to Go Micro are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/). Go Micro uses
calendar-based versions (YYYY.MM) for the AI-native era.

---

## [Unreleased]

### Added
- **Nacos registry and config source** — `registry/nacos` and `config/source/nacos`.
- **Scoped store state** — `store.Scope(s, database, table)` returns a store handle that confines every operation to a database/table without mutating the shared store (unlike `Init(Table(...))`, which is process-global and races between co-located components). Services, agents, and flows now each keep their state in their own table (`service/{name}`, `agent/{name}`, `flow/{name}`); the service path replaces the old `Init(store.Table(name))` global mutation with a scoped handle.
- **Edge gateways** — two gRPC edge gateways behind one contract (`gateway/SPEC.md`, `gateway/rules.schema.json`) and one black-box suite (`gateway/conformance/`): a Go gateway (`gateway/proxy/`) and an OpenResty gateway with go-micro adapter Lua libraries (`gateway/openresty/`). Both discover services in etcd, Consul or Nacos, read hot-reloaded rules from a file, etcd, Consul or Nacos, and offer routes, load balancing, retries, `ip-restriction`, `jwt-auth` and `rate-limit`.
- **Gateway HTTP/JSON entry and REST transcoding** — `POST /api/<service>/<Handler>/<Method>`, plus `google.api.http`-style `http_rules` that map path, query and body onto the request message. `jwt-auth` `forward_claims` passes token claims (such as the user id) to services as metadata. `cmd/protoc-gen-micro-gateway` generates the rules from proto annotations.
- **Gateway WebSocket entry** — `/ws` on the HTTP entry carries unary calls, streams, topic subscriptions and pushes to a user's connections over one connection. Calls get the same routes, plugins and discovery as gRPC calls. Browsers authenticate with `?access_token=`. Services publish with `gateway/push` (`ToUser`, `ToTopic`) over NATS, and every gateway instance delivers to its own connections, at most once. The Go gateway implements it, and OpenResty forwards `/ws` to it (`MICRO_GATEWAY_WS_UPSTREAM`). SPEC §2.3, conformance W1–W8.
- **Redis store and sync** — `store/redis` and `sync/redis` work on Redis (standalone, Sentinel, Cluster), Dragonfly and Valkey, so services get durable state, locks and leader election without etcd. The store keeps keys in byte order for scans and expires records with server-side TTLs. Locks are leased keys renewed while held. Both are tested on Redis 7 and Dragonfly 2.0.
- **MongoDB store and model** — `store/mongo` (expiry on the server clock, a TTL index, prefix scans on `_id`) and `model/mongo` (typed BSON fields, indexes from `model:"index"`, filters as MongoDB operators). Tested on MongoDB 7. Requires MongoDB 4.4 or later.
- **Nacos 3 and Nacos authentication** — `registry/nacos` and `config/source/nacos` gain `WithAuth(username, password)` and `WithGRPCPort`. Both gateways read `MICRO_REGISTRY_USERNAME` / `MICRO_REGISTRY_PASSWORD` and accept `nacos://user:pass@host/...` rules sources, and OpenResty logs in and refreshes its token. Conformance passes on Nacos 2.4.3 and on 3.1.1, with authentication on.
- **Dashboard HTTP API** — `micro run` and `micro server` now really call services at `POST /api/{service}/{Handler}/{Method}` and from the endpoint forms, which only echoed their input before, and serve `/health`, `/health/live` and `/health/ready`.
- **Request logging wrapper** — `wrapper/logging` logs each call with its trace id, on both the RPC and gRPC servers.

### Changed
- **Module path and layout (v5.30.0)** — the module is `github.com/flylib/go-micro` (was `go-micro.dev/v5`), and pluggable backends are separate modules (`registry/etcd`, `store/postgres`, …), so importing one backend does not pull in every SDK. In-repo modules pin each other to a pushed commit's pseudo-version; there are no release tags.
- **Go 1.26 everywhere** — every module and `go.work` now declare `go 1.26.0` (was a mix of 1.24, 1.25 and 1.26), and CI reads the version from `go.mod`. The floor comes from dependencies: etcd client v3.7 requires Go 1.26. `registry/etcd` and `gateway/conformance` move to etcd client v3.7.0 (and grpc v1.81), like the other etcd modules.
- **`micro new` and `micro build`** — generated `go.mod` and Dockerfiles target Go 1.26.

### Fixed
- **Kafka publishes took a second each** — the Kafka broker kept kafka-go's default `BatchTimeout` of 1 s, so every synchronous `Publish` of one message waited out a batch (measured 1.00–1.03 s). The default is now 10 ms, about 15 ms per publish measured. New options `kafka.Async()`, `BatchTimeout`, `BatchSize` and `RequiredAcks` are available, and `Publish` now honours `broker.PublishContext`.
- **Publish-only gRPC services had no broker** — `server/grpc` connected the broker only when the service had subscribers, so a service that only published (for example with `gateway/push`) failed every publish with `not connected`. It now connects on start like the RPC server. Without subscribers, a broker that cannot be reached is logged and does not stop the service. `push.Pusher` also connects its broker before the first push.

### Removed
- **AI layer (v5.30.0)** — agents, LLM providers (`ai/`), flows, the MCP and A2A gateways, x402 payments, the `micro chat` / `micro flow` / `micro mcp` commands and the Python SDKs in `contrib/`. The upstream docs site and blog (`internal/website/`), the AI design documents (`internal/docs/`) and the MCP deployment example are gone too. `micro new` no longer generates MCP sections, and the dashboard no longer links to the agent playground.

---

> Entries below are from upstream `micro/go-micro` releases. They describe features, AI and MCP included, that this fork has since removed.

## [2026.03] - March 2026

### Added

#### Developer Experience
- **`micro new` MCP templates** — `micro new myservice` generates MCP-enabled services with doc comments, `@example` tags, and `WithMCP()` wired in. Use `--no-mcp` to opt out.
- **`micro.New("name")` unified API** — single way to create services: `micro.New("greeter")` or `micro.New("greeter", micro.Address(":8080"))`. Replaces `micro.NewService()` + `service.New()` dual API.
- **`service.Handle()` simplified registration** — register handlers with `service.Handle(new(Greeter))` instead of manual `server.NewHandler` + `server.Handle`.
- **`micro.NewGroup()` modular monoliths** — run multiple services in one binary with shared lifecycle: `micro.NewGroup(users, orders).Run()`.
- **`mcp.WithMCP()` one-liner** — add MCP to any service with a single option: `micro.New("name", mcp.WithMCP(":3001"))`.
- **CRUD example** — contact book service with 6 operations, rich agent docs, and validation patterns (`examples/mcp/crud/`).

#### MCP Gateway
- **WebSocket transport** — bidirectional JSON-RPC 2.0 streaming over WebSocket for real-time agent communication (`gateway/mcp/websocket.go`).
- **OpenTelemetry integration** — full span instrumentation across HTTP, stdio, and WebSocket transports with W3C trace context propagation (`gateway/mcp/otel.go`).
- **Standalone gateway binary** — `micro-mcp-gateway` with Docker support for running the MCP gateway independently of services.
- **Per-tool auth scopes** — service-level (`server.WithEndpointScopes()`) and gateway-level (`Options.Scopes`) scope enforcement with bearer token auth.
- **Rate limiting** — per-tool token bucket rate limiting (`Options.RateLimit`).
- **Audit logging** — immutable audit records per tool call with trace ID, account, scopes, duration, and errors (`Options.AuditFunc`).

#### AI Model Package
- **`model.Model` interface** — unified AI provider abstraction with `Generate()` and `Stream()` methods.
- **Anthropic Claude provider** — `model/anthropic` with tool execution and auto-calling.
- **OpenAI GPT provider** — `model/openai` with provider auto-detection from base URL.

#### Agent SDKs
- **LangChain SDK** — `contrib/langchain-go-micro/` Python package with auto-discovery, tool generation, and multi-agent workflow examples.
- **LlamaIndex SDK** — `contrib/go-micro-llamaindex/` Python package with RAG integration examples.

#### Documentation
- **AI-native services guide** — building services for AI agents from scratch
- **MCP security guide** — auth, scopes, and audit logging
- **Tool descriptions guide** — writing doc comments that improve agent performance
- **Agent patterns guide** — architecture patterns for agent integration
- **Error handling guide** — writing agent-friendly error responses with typed errors
- **Troubleshooting guide** — common MCP issues and solutions
- **Migration guide** — add MCP to existing services in 5 minutes

#### CLI
- **`micro mcp serve`** — start MCP server (stdio for Claude Code, HTTP for web agents)
- **`micro mcp list`** — list available tools (human-readable or JSON)
- **`micro mcp test`** — test tools with JSON input
- **`micro mcp docs`** — generate tool documentation
- **`micro mcp export`** — export to LangChain, OpenAPI, or JSON formats

#### Agent Playground
- **Chat-focused UI** — redesigned playground with collapsible tool calls, real-time status, and thinking indicators
- **Provider settings** — configurable OpenAI/Anthropic provider, model, and API key

### Changed
- Service interface moved to `service.Service` with `micro.Service` as a type alias for backward compatibility.
- `service.New()` returns `service.Service` interface (was `*ServiceImpl`).
- `service.NewGroup()` accepts `service.Service` interface (was `*ServiceImpl`).
- `go.mod` template in `micro new` updated to Go 1.22.

### Fixed
- Handler `Handle()` method accepts variadic `server.HandlerOption` for scopes and metadata.
- Store initialization uses service name as table automatically.
- Service `Stop()` properly aggregates errors from lifecycle hooks.

---

## [2026.02] - February 2026

### Added
- **MCP gateway library** — `gateway/mcp/` with HTTP/SSE and stdio transports, service discovery, tool generation, and JSON schema generation from Go types (2,500+ lines).
- **CLI integration** — `micro run --mcp-address` flag to start MCP alongside services.
- **Documentation extraction** — auto-extract tool descriptions from Go doc comments with `@example` tag and struct tag parsing.
- **Blog post** — "Making Microservices AI-Native with MCP"
- **MCP examples** — `examples/mcp/hello/` and `examples/mcp/documented/`

---

## [2026.01] - January 2026

### Added
- **`micro deploy`** — deploy services to any Linux server via SSH + systemd with `micro deploy user@server`.
- **`micro build`** — build Go binaries and Docker images with `micro build --docker`.
- **Blog post** — "Introducing micro deploy"

---

_For earlier changes, see the [git log](https://github.com/flylib/go-micro/commits/main)._
