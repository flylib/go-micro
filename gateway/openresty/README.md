# OpenResty Gateway

The OpenResty implementation of the go-micro edge gateway. It is nginx with `resty.micro.*` Lua adapter libraries that read go-micro's own registry data and follow its selector semantics. Behaviour is defined by [SPEC.md](../SPEC.md). It passes the [conformance suite](../conformance) against etcd, Consul and Nacos, and with a file rules source, the same as the [Go gateway](../proxy).

## Run

```bash
docker build -t micro-openresty-gateway .

docker run -p 8080:8080 \
  -e MICRO_REGISTRY=nacos \
  -e MICRO_REGISTRY_ADDRESS=nacos:8848 \
  -e MICRO_GATEWAY_RULES='nacos://nacos:8848/micro-gateway-rules?group=DEFAULT_GROUP' \
  micro-openresty-gateway
```

The image builds offline: every Lua dependency is vendored ([vendor/VENDOR.md](vendor/VENDOR.md)). It runs as `nobody`.

Bootstrap settings ([SPEC.md §11](../SPEC.md#11-bootstrap)) are the Go gateway's, except that the broker settings are replaced by `MICRO_GATEWAY_WS_UPSTREAM`:

| Variable | Default | Meaning |
|---|---|---|
| `MICRO_GATEWAY_ADDRESS` | `:8080` | Listen address (h2c) |
| `MICRO_REGISTRY` | — | `etcd`, `consul` or `nacos` |
| `MICRO_REGISTRY_ADDRESS` | — | Comma-separated `host:port` list |
| `MICRO_REGISTRY_NAMESPACE`, `MICRO_REGISTRY_GROUP` | public, `DEFAULT_GROUP` | Nacos namespace and group |
| `MICRO_REGISTRY_USERNAME`, `MICRO_REGISTRY_PASSWORD` | — | Nacos credentials (auth is on by default since Nacos 3.0). A `nacos://` rules source without `user:pass@` uses them too. |
| `MICRO_GATEWAY_RULES` | — | `file://`, `etcd://`, `consul://` or `nacos://`, or `none` |
| `MICRO_GATEWAY_TRUSTED_PROXIES` | — | CIDRs whose `x-forwarded-for` is trusted |
| `MICRO_GATEWAY_UPSTREAM_TLS` | `false` | Dial nodes with TLS (`grpcs://`) |
| `MICRO_GATEWAY_HTTP_ADDRESS` | — | HTTP/JSON entry address (SPEC §2.1); empty removes that server |
| `MICRO_GATEWAY_WS_UPSTREAM` | — | `host:port` of a Go gateway's HTTP entry; `/ws` is forwarded there (SPEC §2.3) |

**Behaviour on bad rules.** A missing or invalid rules document stops the gateway, and the container exits with code 1. A later invalid update is logged and the previous rules stay.

## HTTP/JSON entry

With `MICRO_GATEWAY_HTTP_ADDRESS` set, a second nginx server accepts plain HTTP calls. It serves HTTP/1.1 and h2c ([SPEC.md §2.1](../SPEC.md#21-httpjson-entry-optional)):

```bash
curl -X POST localhost:8090/api/greeter/Greeter/Hello -d '{"name":"gopher"}'
```

The call is proxied by `grpc_pass` in the HTTP request itself, so it gets the same balancer, retries and timeouts as a gRPC call. `resty.micro.http` handles three phases:

- **access** checks the request and routes it with the gRPC entry's code (rules, plugins, discovery). It then wraps the JSON body in a gRPC frame, points the URI at `/<service>.<Handler>/<Method>` and sets `application/grpc+json`.
- **header filter** sets the HTTP status. go-micro errors arrive trailers-only, so their gRPC status is already in the headers. It becomes the HTTP status, and the go-micro error becomes the body.
- **body filter** unwraps the reply frame into the JSON body.

**Why not a subrequest.** `ngx.location.capture` to a `grpc_pass` location crashes nginx workers (signal 11), so the reply cannot be buffered through a subrequest.

**One consequence.** The HTTP status is fixed when the headers pass. A reply that sends a message and then a non-OK status keeps 200, with an error in the body. go-micro unary handlers either reply or fail trailers-only, never both.

### REST transcoding

`http_rules` in the rules document map REST endpoints onto gRPC methods ([SPEC.md §2.2](../SPEC.md#22-http-rules-googleapihttp-transcoding)). They use the `google.api.http` model:

```yaml
http_rules:
  - {method: GET, path: "/v1/users/{id}", target: /users.Users/Get, params: {id: string, verbose: bool}}
  - {method: PATCH, path: "/v1/users/{id}", target: /users.Users/Update, body: user}
```

```bash
curl 'localhost:8090/v1/users/42?verbose=true'   # -> /users.Users/Get {"id":"42","verbose":true}
```

- **Matching.** Rules are matched before `/api/...`. The most specific template wins: more literal segments first, then fewer `**`. Path variables, the body (`*` or one field) and query parameters build the request message.
- **Calls.** The call then follows the target method's routes and plugins.
- **Responses.** `response_body` returns one field of the reply.
- **Errors.** A request the rule cannot build, such as a bad `bool` or an unexpected body, gets 400. Other errors come back as go-micro JSON errors with their HTTP status.
- **Generating rules.** [protoc-gen-micro-gateway](../../cmd/protoc-gen-micro-gateway) generates the rules from `google.api.http` annotations.
- **Caller identity.** Pair the rules with `jwt-auth` `forward_claims: {user-id: sub}` to give services the caller's id in metadata (§9.5). Clients cannot set those keys themselves.
- **Implementation.** [httprules.lua](lib/resty/micro/httprules.lua) mirrors the Go gateway. Numbers with more than 15 significant digits are quoted before cjson decodes the body, so int64 ids stay exact. `response_body` is cut out of the reply text without decoding it.

## Nacos

The Lua libraries read Nacos over its HTTP OpenAPI (`/nacos/v1/ns/instance/list`, `/nacos/v1/cs/configs`). Nacos 2.x and 3.x both serve it. With `MICRO_REGISTRY_USERNAME` set, the gateway logs in at `/nacos/v1/auth/login` and sends the access token with every request. It keeps the token per worker until 90% of its TTL, and logs in again when Nacos answers 403. This is tested on Nacos 2.4.3 and 3.1.1, with and without authentication.

## WebSocket entry

OpenResty does not implement the WebSocket entry itself ([SPEC.md §2.3](../SPEC.md#23-websocket-entry-optional)). With `MICRO_GATEWAY_WS_UPSTREAM` set, `location = /ws` forwards the upgrade, query included, to a [Go gateway](../proxy#websocket-entry). The Go gateway authenticates, routes calls, and delivers pushes and topic messages. Clients keep one entry point:

```
client ──wss──▶ OpenResty :8090 /ws ──ws──▶ Go gateway :8090 /ws ──gRPC──▶ services
                                                 ▲
                                    NATS ────────┘  push.ToUser / push.ToTopic from services
```

Setting it up:
- **Same rules.** Point both gateways at the same rules source. OpenResty validates the `websocket` section too, and strips its `forward_claims` keys from its own calls.
- **Trust.** Set `MICRO_GATEWAY_TRUSTED_PROXIES` on the Go gateway to OpenResty's addresses, so it sees the client IP from `X-Forwarded-For`.
- **Timeouts.** Proxy read and send timeouts are 1 h. The Go gateway's 30 s pings keep idle connections open.
- **Without it,** `/ws` is an unknown path (404).

## Layout

```
lib/resty/micro/
  gateway.lua    phase entry points: init, init_worker, access, balance, log, errors
  rules.lua      parse YAML/JSON, validate against schema.json, compile, match
  registry.lua   read etcd / Consul / Nacos in go-micro's native format
  discovery.lua  shared-dict node cache, refreshed by one worker
  selector.lua   eligibility, filters, roundrobin/random/weighted/version_weights/p2c
  plugins.lua    ip-restriction, jwt-auth (RS256, auth/jwt tokens), rate-limit
  headers.lua    reserved headers, x-forwarded-for, traceparent, client IP
  http.lua       HTTP/JSON entry: access, header and body filters
  httprules.lua  REST transcoding: templates, matching, request and reply
  errors.lua     go-micro shaped gateway errors as gRPC statuses
  source.lua     rules sources
  ip.lua         CIDR matching
  schema.json    copy of ../rules.schema.json (t/run.sh checks they match)
conf/nginx.conf.tmpl   rendered by docker-entrypoint.sh
vendor/                lua-resty-http, jsonschema, net-url, tinyyaml (patched)
t/                     unit tests
```

## How it maps onto nginx

- **Forwarding.** `grpc_pass` relays frames, statuses and trailers untouched (§8). It handles all four RPC types.
- **Retries.** `balancer_by_lua` hands nginx the next candidate on each try. The routing decisions, plugins and candidate order are made in `access_by_lua`. `grpc_next_upstream error timeout` retries connect failures. nginx never retries a POST that already reached a node, so a retry only happens before the request was sent (§6). Timeouts are set per route with `ngx.balancer.set_timeouts`.
- **Gateway errors.** These become gRPC statuses with go-micro JSON messages (§8). nginx cannot end an HTTP/2 stream with headers alone, so the status is sent as real trailers (`add_trailer`) from dedicated `@micro_*` locations. Those directives must never be on the proxying location: there they make nginx expect trailers, which drops the status of the trailers-only replies upstreams send for errors.
- **Discovery.** Node lists live in a shared dict. Worker 0 refreshes every recently requested service each second, and a service seen for the first time is fetched inline. When the registry is unreachable, the last known nodes are kept (§4.4).
- **Rules.** Worker 0 polls the rules source each second, validates each change, and publishes it. Workers recompile it on their next call.
- **Access log.** One JSON line per call, with `wrapper/logging`'s field names, including `trace_id`.

## Tests

```bash
./t/run.sh   # unit tests, in an OpenResty container
```

The unit tests mirror the Go gateway's: rules, plugins, selector, registry parsers.

**End-to-end.** For end-to-end checks, run the [conformance suite](../conformance) against a running container.

**Docker on macOS (colima, Docker Desktop).** Run the suite in a container on the gateway's network, so that the gateway reaches the suite's backends container to container. Container-to-host connections through the VM drop intermittently and show up as false failures.

## Known limitations

- **Node addresses must be IPs.** `ngx.balancer` needs an IP; registry entries with a host name are skipped.
- **A read timeout after the response started resets the stream.** nginx cannot send a status at that point, so the client sees a stream reset rather than `DeadlineExceeded`. A timeout before the response headers gives `DeadlineExceeded` (504).
- **Round-robin positions are per worker.** Rotation is even per worker, not exactly global.
- **Inbound TLS is not configured.** Add a `listen ... ssl` server or terminate TLS in front of the gateway.
- **The gateway continues or starts the W3C trace but does not open its own span.**
