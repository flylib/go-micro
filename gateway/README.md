# Gateway

Edge gateways for traffic from outside the cluster to go-micro services. There are two implementations, a Go gateway and an OpenResty gateway. They follow one contract ([SPEC.md](SPEC.md)) and are tested by one black-box suite.

| Directory | What |
|---|---|
| [SPEC.md](SPEC.md) | The contract: entries, routing, discovery, load balancing, retries, headers, errors, rules, bootstrap |
| [rules.schema.json](rules.schema.json), [rules.example.yaml](rules.example.yaml) | The rules format shared by both gateways |
| [proxy/](proxy) | Go gateway, built on go-micro's registry, selector, broker and config sources |
| [openresty/](openresty) | OpenResty gateway with go-micro adapter Lua libraries |
| [push/](push) | `push.ToUser` / `push.ToTopic`: messages from services to WebSocket clients |
| [conformance/](conformance) | Black-box suite that every gateway must pass |
| [api/](api) | HTTP server shell used by `micro run` and `micro server` for the dashboard. It is not an edge gateway. |

## What the gateways do

| | Go gateway | OpenResty gateway |
|---|---|---|
| gRPC entry: all four RPC types, opaque frames | ✓ | ✓ |
| Discovery in etcd, Consul, Nacos | ✓ | ✓ |
| Rules from a file, etcd, Consul, Nacos, hot reloaded | ✓ | ✓ |
| Routes, load balancing (roundrobin, random, weighted, version weights, p2c), pre-send retries | ✓ | ✓ |
| Plugins: `ip-restriction`, `jwt-auth` (with `forward_claims`), `rate-limit` | ✓ | ✓ |
| HTTP/JSON entry and REST transcoding (`http_rules`) | ✓ (net/http + chi, or fasthttp) | ✓ |
| WebSocket entry `/ws`: calls, streams, topics, push | ✓ | forwards to a Go gateway |
| Custom logic | Go code around `proxy.Gateway` | Lua in nginx phases |

**Which one.** Both pass the same suite, so choose by how you run nginx.
- **OpenResty** suits an OpenResty or nginx edge that also carries other traffic, or a team that wants Lua at the edge.
- **The Go gateway** is a single binary, embeddable as a library.
- **Both together.** With OpenResty as the entry, add a Go gateway behind it for `/ws`.

**Service side.** Services must use the gRPC server (`server/grpc`) and register in etcd, Consul or Nacos.
- **Routing convention.** A service's proto `package` equals its registry name, so `/<service>.<Handler>/<Method>` needs no route.
- **Codecs.** HTTP and WebSocket calls reach the service as `application/grpc+json`, which go-micro's gRPC server decodes with `protojson`.

## Exposing a service over REST

1. Install the generator:

   ```bash
   go install github.com/flylib/go-micro/cmd/protoc-gen-micro-gateway@main
   ```

2. Annotate methods with `google.api.http` and generate the rules (`<googleapis>` is a checkout of [googleapis](https://github.com/googleapis/googleapis) for `google/api/annotations.proto`):

   ```bash
   protoc -I. -I<googleapis> --micro-gateway_out=. users.proto
   ```

3. Merge the `http_rules` list from `users.gateway.yaml` into the gateway rules document: a file, or an etcd, Consul or Nacos key (`MICRO_GATEWAY_RULES`). Both gateways reload it within 5 s.

4. Service code does not change. To pass the caller's identity, add `forward_claims: {user-id: sub}` to the route's `jwt-auth` plugin. The handler then reads it with `metadata.Get(ctx, "user-id")`. The gateway strips inbound headers with that name on every call, so clients cannot forge it.

Details: [protoc-gen-micro-gateway](../cmd/protoc-gen-micro-gateway), SPEC §2.2 (transcoding) and §9.5 (`forward_claims`).

## Long-lived connections

The WebSocket entry `/ws` carries calls, streams, topic subscriptions and pushes to a user over one connection (SPEC §2.3). Services push over NATS:

```go
p := push.New(svc.Options().Broker) // the NATS broker the gateway uses (MICRO_BROKER=nats)
p.ToUser(ctx, "user-42", msg)       // every connection of that account, on every gateway instance
p.ToTopic(ctx, "room.42", msg)      // every connection subscribed to the topic
```

The Go gateway serves the entry, and OpenResty forwards `/ws` to it. See [proxy/](proxy#websocket-entry) for the protocol and [openresty/](openresty#websocket-entry) for forwarding.

## Behaviour on bad rules

If the rules document is missing or invalid at start-up, the gateway refuses to start. If a later update is invalid, the gateway logs the error and keeps the previous rules.
