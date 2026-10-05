# Gateway Specification

Status: **draft v1** · Applies to: `gateway/proxy` (Go) and `gateway/openresty` (OpenResty/Lua)

go-micro ships two edge gateways with the same behaviour. One is built in Go on top of go-micro itself; the other runs on OpenResty with Lua adapter libraries. This document is the contract both must implement. A rules file written for one gateway works unchanged on the other, and the shared conformance suite (`gateway/conformance`) runs against both.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

## 1. Scope

A gateway accepts gRPC calls from outside the cluster, applies rules (auth, IP control, rate limits), resolves the target go-micro service through the registry and forwards the call to one of its nodes.

The two implementations differ in how they are built:

| | `gateway/proxy` (Go) | `gateway/openresty` |
|---|---|---|
| Transport | grpc-go transparent proxy (`UnknownServiceHandler`, raw frames) | nginx `grpc_pass` + `balancer_by_lua` |
| Registry / selector | go-micro `registry` + `registry/cache`, `selector` strategies and filters | `resty.micro.*` Lua ports that read the same registry data (§4, §5) |
| Rules source | go-micro `config/source` (etcd, consul, nacos); files polled | Lua readers of the same sources |
| Plugins | Go, in `gateway/proxy` | Lua |
| Extension | Go code (compiled in) | Lua (hot-loadable) |
| HTTP/JSON entry (§2.1, optional) | `net/http` + chi (HTTP/1.1 and h2c), or fasthttp (HTTP/1.1) | nginx server with Lua access/header/body filters around `grpc_pass` (HTTP/1.1 and h2c) |

Out of scope for v1 (§13): field-mapped REST transcoding, distributed rate limiting, the mdns registry.

**Terms**
- **Rules** — the hot-reloadable routing and policy document (§9).
- **Bootstrap** — static settings read at start-up (§11).
- **Node** — one registered instance of a service.

## 2. Wire protocol

- **Inbound:** gRPC over HTTP/2. Cleartext h2c MUST be supported. TLS SHOULD be supported.
- **Upstream:** gRPC over h2c. Upstream TLS is a bootstrap option (`MICRO_GATEWAY_UPSTREAM_TLS`), because go-micro does not advertise TLS in the registry.
- **RPC types:** all four MUST be supported — unary, server streaming, client streaming and bidirectional.
- **Payloads:** messages MUST be forwarded as opaque frames. The gateway never decodes payloads and needs no `.proto` files.

### 2.1 HTTP/JSON entry (optional)

A gateway MAY also accept plain HTTP calls with JSON bodies, for clients that do not speak gRPC. When it does, the entry MUST behave as follows. It is enabled by `MICRO_GATEWAY_HTTP_ADDRESS` (§11) and listens on its own address. The gRPC entry is unchanged.

**Protocol.** HTTP/1.1 MUST be accepted. Cleartext HTTP/2 (h2c) on the same address and TLS SHOULD be supported. An implementation MAY also offer an HTTP/1.1-only server, as the Go gateway does with its fasthttp option.

**Mapping.** `POST /api/<service>/<Handler>/<Method>` becomes the gRPC call `/<service>.<Handler>/<Method>`. This is the path go-micro's client uses (§3.1), and it matches the `/api/...` URLs that `micro server` shows. From there, the call is handled exactly like one arriving on the gRPC entry: route matching (§3.2), plugins (§9.4), discovery, load balancing and pre-send retries (§4–§6).

**Body.** The request body is the JSON request message. An empty body is `{}`. It is forwarded as one gRPC message with content-subtype `json` (`application/grpc+json`), so the gateway still needs no `.proto` files. go-micro's gRPC server decodes it into the handler's request type with `protojson`, which accepts both proto field names and their camelCase JSON names. Upstreams that do not support the `json` subtype cannot be reached through this entry.

**Headers.** Request headers become gRPC metadata with lowercase keys. These are dropped:
- hop-by-hop headers: `Connection`, `Keep-Alive`, `Proxy-*`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`
- `Host`, `Content-Length`, `Content-Type`, `Accept-Encoding`

The rules of §7 then apply unchanged, including reserved `micro-gateway-*` headers, `x-forwarded-for` and `traceparent`.

**Success.** A successful reply is `200 OK` with `Content-Type: application/json`. The body is the JSON response message as the service encoded it. Response metadata from the service becomes response headers, except `content-type` and `grpc-*`.

**Errors.** Errors are JSON go-micro errors (`{"id","code","detail","status"}`), with the HTTP status taken from the error:
- **go-micro error from a service** (`grpc-message` parses as one with a `code` between 100 and 599): that error is the body, and its `code` is the HTTP status.
- **Gateway error** (§8): the same, with `id` `micro.gateway`.
- **Any other upstream error:** the status maps from the gRPC code, the same mapping go-micro's gRPC client uses (`InvalidArgument` 400, `Unauthenticated` 401, `PermissionDenied` 403, `NotFound` 404, `DeadlineExceeded` 408, `AlreadyExists` 409, `FailedPrecondition` 412, `ResourceExhausted` 429, `Unimplemented` 501, `Unavailable` 503, everything else 500). The body is a go-micro error with `id` = service and `detail` = the gRPC message.

**Requests the entry itself refuses**, each with a JSON go-micro error and `id` `micro.gateway`:

| Situation | Status |
|---|---|
| Path not of the form `/api/<service>/<Handler>/<Method>` | 404 |
| Method other than `POST` | 405 |
| `Content-Type` set and not `application/json` | 415 |
| Body larger than 4 MiB | 413 |

**Unary only.** The entry carries unary calls. A reply with more than one message is answered with 500.

### 2.2 HTTP rules (`google.api.http` transcoding)

On the HTTP/JSON entry, a gateway that offers it MUST also serve the `http_rules` of the rules document (§9.6). An HTTP rule maps a REST-style call such as `GET /v1/users/42?verbose=true` onto a gRPC method. It follows the [`google.api.http`](https://github.com/googleapis/googleapis/blob/master/google/api/http.proto) annotation: `protoc-gen-micro-gateway` generates the rules from those annotations, or they can be written by hand.

**Matching.** A request is matched against the HTTP rules before the `/api/...` mapping of §2.1. A rule matches when its `method` equals the request method and its path template matches the request path. When several rules match, the most specific one wins:
1. more literal segments;
2. then fewer `**` wildcards;
3. then document order.

When no rule matches, §2.1 applies: `/api/...` paths are mapped as before, and anything else is answered with 404.

**Path templates** use the `google.api.http` syntax:

| Element | Matches | Binds |
|---|---|---|
| `literal` | that segment | — |
| `*` | one segment | — |
| `**` | zero or more segments; last element only | — |
| `{field}` | one segment | `field` |
| `{field=pattern}` | `pattern`, made of literals, `*` and a final `**` | `field`, to the matched segments joined by `/` |
| `:verb` | a final `:verb` on the last segment | — |

`field` may be a dotted path (`user.id`), which sets a nested field. Templates match the raw (still percent-encoded) path, so `{field}` takes exactly one raw segment. Bound values are then percent-decoded, so a value may contain `/` when the client sent `%2F`.

**The request message** is a JSON object built from three sources, then sent like an HTTP/JSON entry call (§2.1):
- **The body**, depending on `body`:
  - `"*"`: the request body is the message.
  - `"<field>"`: the request body becomes that field.
  - empty or absent: there is no body, and the request body MUST be empty or `{}`.
- **Path variables**, set into the object. They override what the body set.
- **Query parameters**, except when `body` is `"*"`. `a.b=x` sets a nested field. A repeated key gives a list. A parameter naming a field already set by the path is ignored, as are parameters the rule's `params` does not list (when it lists any).

**Values.** Path and query values are strings. That is enough for proto3 JSON, which accepts strings for every numeric type and for enums. Two kinds of field need to know their type, and get it from the rule's `params` map (field path → type):
- `bool` turns `true`/`false` into JSON booleans; any other value is rejected with 400.
- `repeated` always makes a list, even for a single value.

Fields not listed in `params` stay strings. `protoc-gen-micro-gateway` fills `params` from the request message.

**The reply.** With `response_body: "<field>"`, the HTTP body is that field of the JSON response message. Otherwise it is the whole message. Errors are as in §2.1.

**After matching**, the call is the gRPC call to the rule's `target` (`/<service>.<Handler>/<Method>`), and §3 to §8 apply as for any call.

### 2.3 WebSocket entry (optional)

A gateway with the HTTP/JSON entry MAY also serve WebSocket connections (RFC 6455) at `GET /ws` on the same address. Over one connection a client can make RPC calls, open streams, subscribe to topics and receive messages pushed to its account. The entry is on when the rules document has a `websocket` section (§9.7). Without it, `/ws` is answered like any unknown path (404). `/ws` is matched before HTTP rules (§2.2).

The Go gateway implements this entry. Other gateways MAY forward `/ws` to a Go gateway instead. The OpenResty gateway does, with `MICRO_GATEWAY_WS_UPSTREAM` (§11). The forwarding gateway MUST pass the client address in `X-Forwarded-For`, and the Go gateway MUST trust it (§7).

**Handshake.**
- **Connection metadata.** The upgrade request's headers become the connection's metadata, filtered as in §2.1. `Sec-WebSocket-*` headers are dropped as well.
- **Browser tokens.** Browsers cannot set headers on a WebSocket. A query parameter `access_token=<token>` sets `authorization: Bearer <token>` when the request has no `Authorization` header.
- **Handshake plugins.** The `websocket.plugins` (§9.7) run once, on a call whose method is `/ws`. When one rejects it, the gateway answers the upgrade request with that error as in §2.1 (for example 401) and does not upgrade.
- **Account.** `jwt-auth` there sets the connection's *account* (the token's `sub`). It is the identity for user pushes and for `{account}` in topic patterns. A connection without `jwt-auth` has no account.
- **Subprotocol.** When the client offers the subprotocol `micro.v1`, the gateway selects it.

**Messages.** Every WebSocket message is one JSON object in a text frame.
- **Binary frame:** the gateway closes the connection with 1003.
- **Message over 4 MiB:** the gateway closes it with 1009.
- **Malformed message or unknown `type`:** the gateway answers with an `error` message (status 400, with the `id` if it could read one). The connection stays open.

Client to gateway:

| `type` | Fields | Meaning |
|---|---|---|
| `call` | `id`, `method`, `body`, `metadata` | Unary call |
| `stream` | `id`, `method`, `body`, `metadata`, `close_send` | Opens a stream. A `body`, if present, is its first message. `close_send: true` ends sending right away, which suits a server-streaming method. |
| `send` | `id`, `body` | Sends a message on an open stream |
| `close_send` | `id` | The client has finished sending on a stream |
| `cancel` | `id` | Cancels a call or stream. The gateway ends it with an `error`, status 499. |
| `subscribe` | `id`, `topic` | Subscribes the connection to a topic |
| `unsubscribe` | `id`, `topic` | Unsubscribes |
| `ping` | `id` | The gateway answers with `pong` |

Gateway to client:

| `type` | Fields | Meaning |
|---|---|---|
| `reply` | `id`, `body` | Result of a `call` |
| `message` | `id`, `body` | A message on a stream |
| `end` | `id` | The stream finished successfully |
| `error` | `id`, `error` | The call, stream or request failed. `error` is a go-micro error object. This is the last message for that `id`. |
| `subscribed` / `unsubscribed` | `id`, `topic` | Subscription change confirmed |
| `event` | `topic`, `body` | A message published to a subscribed topic |
| `push` | `body` | A message pushed to the connection's account |
| `pong` | `id` | Answer to `ping` |

**Fields.**
- **`id`.** The client chooses it. An `id` that names an open call or stream cannot be reused, or the request gets error 400. Messages for one `id` arrive in order. Messages for different ids may interleave.
- **`method`.** A gRPC path `/<service>.<Handler>/<Method>`. Each call or stream is handled like one on the gRPC entry: route matching (§3.2), route and global plugins (§9.4), discovery, load balancing and pre-send retries (§4–§6). §7 applies to its metadata. Plugins see the connection's metadata and the call's `metadata`, so a route with `jwt-auth` checks the handshake token.
- **`metadata`.** An object of string values, added to the connection's metadata for that call. Keys are lowercased.
- **`body`.** Any JSON value, forwarded as one `application/grpc+json` message (§2.1). An absent body is `{}`. Reply and message bodies are the JSON messages as the service encoded them.
- **Errors.** Error objects follow §2.1: go-micro errors from services pass through unchanged, gateway errors have `id` `micro.gateway`, and other upstream errors are mapped from the gRPC code. A `call` whose reply has more than one message fails with 500, as in §2.1.

**Limits and liveness.**
- **Open calls.** A connection has at most `websocket.max_calls` open calls and streams (default 100). Beyond that, a new one gets error 429.
- **Pings.** The gateway sends WebSocket pings every 30 s and closes connections that do not answer within 30 s.
- **Slow clients.** A client that stops reading until the gateway's outgoing queue for it is full is closed with 1008.

**Push and topics** need a broker (`MICRO_BROKER`, §11). Without one, `subscribe` fails with 503 and nothing is pushed.

| Broker topic | Delivered as | To |
|---|---|---|
| `micro.push.user.<account>` | `push` | Every connection with that account, on every gateway instance |
| `micro.push.topic.<topic>` | `event` | Every connection subscribed to `<topic>` |

- **Account encoding.** In broker topics, bytes of the account outside `[A-Za-z0-9_-]` are written as `%XX`. The broker message body is the JSON `body`.
- **Delivery.** Delivery is at most once: a connection that is not open when a message is published does not get it. Services publish with `gateway/push` (`push.ToUser`, `push.ToTopic`).
- **Topic names.** Dot-separated segments of `[A-Za-z0-9_-]`, at most 256 bytes. Anything else gets error 400.
- **Topic authorization.** A client may subscribe only to topics matched by a `websocket.topics` pattern, or it gets error 403.
  - In patterns, a `*` segment matches one segment, and a final `>` matches one or more segments.
  - `{account}` stands for the connection's account. It never matches for a connection without an account, or when the account is not a valid segment.

## 3. Routing

### 3.1 Deriving the service from the path

go-micro's gRPC client calls `/<service>.<Handler>/<Method>`, where `<service>` is the registry name (`client/grpc/request.go`, `methodToGRPC`). The gateway inverts this exactly as `util/grpc.ServiceFromMethod` does:

1. Split the path on `/`. The path MUST have three parts with a non-empty middle part, or the call is rejected with `Unimplemented`. This rejection may come from the gRPC library before any gateway code runs (grpc-go does this), so its message is not required to be a go-micro error.
2. Take the middle part (`greeter.Greeter`).
3. The service is everything before its **last** `.`.

| Path | Derived service |
|---|---|
| `/greeter.Greeter/Hello` | `greeter` |
| `/go.micro.srv.greeter.Greeter/Hello` | `go.micro.srv.greeter` |
| `/Greeter/Hello` | *(empty — needs an explicit route)* |

**Convention:** a service's proto `package` equals its registry name. With this convention, no route needs to be configured for the service.

### 3.2 Route matching

Each route in the rules has one `match` (§9.3). For a call, the gateway picks the first route that matches, in this order:

1. `method` — exact full path, e.g. `/greeter.Greeter/Hello`.
2. `prefix` — longest matching path prefix wins.
3. `service` — the derived service (§3.1) equals this value.

If no route matches:

- With `defaults.convention: true` (the default), the call goes to the derived service using `defaults` settings.
- Otherwise, or when the derived service is empty, the call is rejected with `Unimplemented` (§8).

A route's `upstream.service` overrides the derived service. This is how paths that do not follow the convention are mapped.

## 4. Service discovery

### 4.1 Common model

Every backend is normalised to go-micro's `registry.Service` shape:

```
{ name, version, metadata, endpoints[], nodes[{ id, address "host:port", metadata }] }
```

Nodes of the same service with different versions are separate `Service` entries, as in Go.

### 4.2 Backends

Implementations MUST read the data exactly as go-micro writes it. Neither gateway may require services to register differently.

**etcd** (`registry/etcd`)
- **Key:** `/micro/registry/<service>/<node-id>`. Any `/` inside the service name or node id is replaced with `-`.
- **Value:** JSON of a `registry.Service` holding exactly one node. The key is bound to a lease, so an expired node disappears with its key.
- **Read:** range over `/micro/registry/<service>/`, then watch that prefix.

**Consul** (`registry/consul`)
- **One Consul service per node:** `ID` = node id, `Name` = service, `Address`/`Port` = node address, `Meta` = node metadata in plain text.
- **Version:** read from `Meta["micro_version"]` (framework change F1, §12). Consul Meta keys allow only `[A-Za-z0-9_-]`, so this key cannot match Nacos's `micro.version`.
- **Tags:** version and endpoints are also stored in zlib+hex-encoded tags (`v-…`, `e-…`). Implementations MUST NOT depend on decoding tags.
- **Read:** `/v1/health/service/<service>?passing=true`. Blocking queries SHOULD be used.

**Nacos** (`registry/nacos`)
- **Instance:** `ServiceName` = service, `GroupName` = configured group (default `DEFAULT_GROUP`), `Ip`/`Port` = node address. Instances are ephemeral.
- **Metadata:** node metadata plus `micro.version`.
- **Read:** the instance list with `healthyOnly=true`, honouring the namespace and group set in bootstrap.
- **Endpoints:** not stored.

### 4.3 Node eligibility

A node is eligible only if all of these hold:
- its metadata has `protocol == "grpc"`. Nodes with `protocol == "mucp"` or no protocol MUST be skipped.
- the backend reports it healthy (Consul passing, Nacos healthy). An etcd key that exists counts as healthy.
- it passes the route's filters (§5.2).

### 4.4 Caching and failure

- Changes in the registry MUST take effect within **5 s** (by watch or poll).
- If the registry is unreachable, the gateway MUST keep serving from the last known node list. It MUST NOT drop all routes.
- A service with no eligible nodes is rejected with `Unavailable` (§8).

## 5. Load balancing

### 5.1 Strategies

Names and semantics match go-micro's `selector` packages. They are set per route in `upstream.selector.strategy`.

| Strategy | Semantics | Go equivalent |
|---|---|---|
| `roundrobin` (default) | Rotate over eligible nodes | `selector.RoundRobin` |
| `random` | Uniform random | `selector.Random` |
| `weighted` | Weight from node metadata `weight`, default 100, negative ignored | `selector/weighted.Strategy` |
| `p2c` | Power of two choices on EWMA latency × (inflight + 1) | `selector/p2c.Strategy` |

`upstream.selector.version_weights` (e.g. `{v1: 95, v2: 5}`) splits traffic between versions, as `weighted.VersionWeights` does:
- Versions that are not listed get no traffic.
- Nodes within a version share that version's weight equally.
- When set, it replaces `strategy`.

### 5.2 Filters

`upstream.filters` narrows the eligible nodes before the strategy runs. These mirror `selector.FilterVersion`, `FilterLabel` and `FilterEndpoint`.

- **`version`** — keep services with this version.
- **`labels`** — keep nodes whose metadata contains every given key/value pair.
- **`endpoint`** — keep services that list the called endpoint (`<Handler>.<Method>`). Endpoint lists exist only in etcd. With other registries this filter MUST be rejected when the rules are loaded (§10), rather than silently passing every node.

## 6. Timeouts and retries

- **Timeouts** come from `defaults.timeout`, overridable per route: `connect` (default `1s`), `send` (default `10s`), `read` (default `10s`). `read` is the idle time allowed between upstream frames.
- **Client deadline:** when the client sends `grpc-timeout`, the effective deadline is the smaller of `grpc-timeout` and the route's limits. `grpc-timeout` MUST be forwarded upstream.
- **Retries:** `retries` (default `2`) is the number of extra nodes tried. A retry MUST happen only if nothing has been written to the upstream yet — that is, on connect failure, refusal, or a reset before the request was sent. A call whose request reached an upstream MUST NOT be retried, whatever the response. This keeps retries safe for non-idempotent methods.
- **Streams** follow the same rule. A stream is retried only before its first frame has been sent.

The short connect timeout plus pre-send retry is what lets a node that has just stopped fail over cleanly. The node is still in the cache until the next registry update (§4.4).

## 7. Headers

gRPC metadata keys are case-insensitive. go-micro's `metadata.Get` also handles title-cased keys.

| Header | Gateway behaviour |
|---|---|
| `traceparent` | Forward if present and valid. Otherwise MUST generate one, so service logs (`wrapper/logging`) carry a `trace_id`. |
| `grpc-timeout` | Forward (§6). |
| `authorization` | Forward unchanged, so services can still verify the token themselves with `wrapper/auth`. |
| `x-forwarded-for` | Append the client IP. |
| `micro-gateway-*` | **Reserved.** Inbound values MUST be stripped, then set by the gateway. |
| `micro-gateway-account` | Set to the JWT `sub` after a successful `jwt-auth` (§9.5). |
| `micro-gateway-route` | Set to the name of the matched route. |

**Client IP.** The client IP is the TCP peer address. When the peer is in bootstrap `MICRO_GATEWAY_TRUSTED_PROXIES`, the gateway takes the right-most `x-forwarded-for` entry that is not itself a trusted proxy.

## 8. Errors

**Upstream errors** — status, message and trailers — MUST pass through unchanged.

**Gateway-originated errors** MUST look like a go-micro service error:
- `grpc-message` is the JSON of `errors.Error`: `{"id":"micro.gateway","code":<http>,"detail":"…","status":"<http status text>"}`
- `grpc-status` is the code from this table.

The table extends `server/grpc/error.go`. Codes 429, 502 and 504 are missing there today and are added by framework change F2 (§12).

| Situation | `code` | gRPC status |
|---|---|---|
| No route, empty derived service | 501 | `Unimplemented` |
| Missing or invalid credentials | 401 | `Unauthenticated` |
| Denied by IP rule or missing scope | 403 | `PermissionDenied` |
| Rate limited | 429 | `ResourceExhausted` |
| No eligible nodes | 503 | `Unavailable` |
| All tries failed to connect | 502 | `Unavailable` |
| Deadline or timeout exceeded at the gateway | 504 | `DeadlineExceeded` |
| Internal gateway fault | 500 | `Internal` |

Gateway errors MUST be gRPC responses (HTTP 200 with grpc-status trailers). A bare HTTP 4xx/5xx makes clients report a transport error instead.

## 9. Rules

### 9.1 Format

YAML or JSON, validated against [`rules.schema.json`](rules.schema.json). [`rules.example.yaml`](rules.example.yaml) shows every field.

Durations are strings such as `"500ms"`, `"1s"` or `"2m"`.

### 9.2 Structure

```yaml
version: 1
defaults:      # applied to every route and to convention routing
global:        # plugins run on every call, before route plugins
routes:        # ordered list
http_rules:    # REST mappings for the HTTP/JSON entry (§2.2, §9.6)
```

### 9.3 Route

```yaml
- name: greeter-canary              # unique, used in logs and micro-gateway-route
  match: { service: greeter }       # exactly one of: method | prefix | service
  upstream:
    service: greeter                # optional, defaults to the derived service
    selector: { strategy: roundrobin, version_weights: { v1: 90, v2: 10 } }
    filters:  { version: v2, labels: { zone: a } }
    timeout:  { connect: 1s, send: 10s, read: 10s }
    retries:  2
  plugins:
    - name: jwt-auth
      config: { … }
```

Fields left out of a route fall back to `defaults`.

### 9.4 Plugins

- A plugin entry is `{name, config}`. Execution order is `global.plugins`, then the route's `plugins`, each in list order. All plugins run before node selection.
- A plugin may end the call with a gateway error (§8). Otherwise the next plugin runs.
- **Unknown plugin names MUST cause the rules to be rejected (§10).** This prevents a typo from silently disabling a security rule.
- Implementation-specific plugins MUST use the `x-` prefix (e.g. `x-lua-script`). A gateway that does not know an `x-` plugin MUST also reject the rules.

### 9.5 Standard plugins (v1)

Both implementations MUST provide these plugins with these exact names and fields.

**`ip-restriction`**
- **Fields:** `allow: [CIDR]`, `deny: [CIDR]`. IPv4 and IPv6 are both accepted.
- **Order:** `deny` is checked first. If `allow` is non-empty and the client IP (§7) is not in it, the call is rejected with 403.

**`jwt-auth`** — compatible with tokens issued by `auth/jwt`
- **Fields:** `public_key` (base64-encoded PEM, the same encoding as `auth/jwt/token.WithPublicKey`) and an optional `scopes: [string]`.
- **Algorithm:** RS256 only. Tokens signed with any other `alg` MUST be rejected.
- **Token:** read from `authorization: Bearer <token>`. The gateway verifies the signature and `exp` (with up to 30 s of clock skew).
- **Scopes:** with `scopes` set, the token's `scopes` claim must contain at least one of them, or the call is rejected with 403.
- **Errors:** a missing, malformed or expired token is rejected with 401.
- **On success:** sets `micro-gateway-account` = `sub`.
- **Forwarding claims:** `forward_claims` (optional, metadata key → claim name) copies string or number claims of the verified token into upstream metadata, for example `{user-id: sub}`. Keys are lowercase metadata names. Inbound values of every key named in any `forward_claims` of the loaded rules are removed on every call, on every route, as for reserved headers (§7). So clients cannot set them, not even on routes without `jwt-auth`. This is how services receive the caller's user id.

**`rate-limit`** — token bucket, local to each gateway instance
- **Fields:** `rate` (requests per second, > 0), `burst` (≥ 0), `key`.
- **Bucket:** refills at `rate` tokens per second and holds `1 + burst` tokens, starting full. Each call takes one token. This matches nginx `limit_req` with `nodelay`, and Go's `rate.NewLimiter(rate, 1+burst)`.
- **Key:** one of `client_ip`, `account` (falls back to `client_ip` when unauthenticated) or `header:<name>`.
- **Errors:** rejected with 429.

### 9.6 HTTP rules

```yaml
http_rules:
  - method: GET                     # GET | PUT | POST | DELETE | PATCH
    path: /v1/users/{id}            # template (§2.2)
    target: /user.User/Get          # gRPC method
    body: ""                        # "", "*" or a field name
    response_body: ""               # "" or a field name
    params: {id: string, verbose: bool, tags: repeated}
```

Semantics are in §2.2. Two rules with the same `method` and `path` are rejected (§10).

### 9.7 WebSocket

```yaml
websocket:                          # present: the /ws entry is on (§2.3)
  plugins:                          # run once per connection, at the handshake
    - name: jwt-auth
      config: {public_key: "...", forward_claims: {user-id: sub}}
  topics: ["room.*", "user.{account}.>"]   # topics clients may subscribe to; default none
  max_calls: 100                    # open calls and streams per connection
```

A new rules document applies to new connections and to new calls and subscriptions on open ones. An open connection keeps its handshake result (account, forwarded claims).

## 10. Rules loading and hot reload

1. A new rules document MUST be fully validated before use: schema (§9.1), plugin names (§9.4) and backend-dependent checks (§5.2).
2. **Valid:** swap it in atomically. Calls already in flight finish on the old rules; new calls use the new ones.
3. **Invalid:** keep the previous rules, log the reason, and expose the failure through a log line and a metric.
4. **At start-up:** a missing or invalid rules document MUST stop the gateway from starting. Running with no rules is only possible with an explicit `MICRO_GATEWAY_RULES=none`, which means convention routing with no plugins.
5. **Timing:** a change in the rules source MUST take effect within **5 s**.

## 11. Bootstrap

Static settings, read once. Names reuse go-micro's existing environment variables where one exists. Both implementations MUST accept these names, either as environment variables or as equivalent flags.

| Setting | Meaning |
|---|---|
| `MICRO_GATEWAY_ADDRESS` | Listen address, default `:8080` |
| `MICRO_GATEWAY_HTTP_ADDRESS` | HTTP/JSON entry listen address (§2.1); empty disables it |
| `MICRO_REGISTRY` | `etcd`, `consul` or `nacos` |
| `MICRO_REGISTRY_ADDRESS` | Comma-separated `host:port` list |
| `MICRO_REGISTRY_NAMESPACE` / `MICRO_REGISTRY_GROUP` | Nacos namespace and group |
| `MICRO_GATEWAY_RULES` | Rules source URI, see below |
| `MICRO_GATEWAY_TRUSTED_PROXIES` | Comma-separated CIDRs (§7) |
| `MICRO_GATEWAY_UPSTREAM_TLS` | `true` to dial nodes with TLS |
| `MICRO_BROKER` / `MICRO_BROKER_ADDRESS` | Broker for WebSocket push and topics (§2.3): `nats`, or empty for none. Go gateway only. |
| `MICRO_GATEWAY_WS_UPSTREAM` | `host:port` of a Go gateway's HTTP entry that `/ws` is forwarded to (§2.3). Gateways that forward WebSocket only. |

**Rules sources** map onto go-micro's `config/source` backends:

```
file:///etc/micro/gateway/rules.yaml
etcd://host:2379/micro/gateway/rules                   # value of one key
consul://host:8500/micro/gateway/rules                 # one KV key
nacos://host:8848/micro-gateway-rules?group=DEFAULT_GROUP&namespace=public
```

The rules backend is independent of `MICRO_REGISTRY`. For example, rules can live in Nacos while services register in etcd.

## 12. Framework changes this spec depends on

| ID | Change | Why | Status |
|---|---|---|---|
| F1 | `registry/consul`: also write the version as `Meta["micro_version"]` in plain text. Backward compatible. | Lua cannot decode the zlib+hex tags (§4.2). | Done |
| F2 | `server/grpc/error.go` and the gRPC client: map 429 ↔ `ResourceExhausted`, 502 → `Unavailable`, 504 → `DeadlineExceeded`. | Gateway and service errors must map the same way (§8). | Done |
| F3 | `server/grpc`: the global `proto` codec it installs also handles legacy v1 messages, as `client/grpc`'s already did (#2333). | Without it, any binary with the gRPC server and the etcd registry fails to register (`grpc: error while marshaling: invalid message`). Found by the conformance harness. | Done |
| F4 | `selector/p2c`: export `Track(addr)` to feed latency and in-flight counts from outside the go-micro client. | The Go gateway's `p2c` strategy (§5.1). | Done |
| F5 | `registry/nacos`: the watcher diffs each pushed snapshot and emits per-node `create`/`delete` results grouped by version. | It emitted the whole snapshot as one `update`, which `registry/cache` merges, so removed nodes stayed cached and all versions were merged into one (§4.4, §5.1). Found by the conformance suite. | Done |

## 13. Out of scope for v1

- **WebSocket in OpenResty itself.** OpenResty forwards `/ws` to a Go gateway (§2.3).
- **Guaranteed delivery of pushes.** Pushes are at most once (§2.3). Durable inboxes belong in services.
- **Distributed rate limiting** (shared counters across gateway instances).
- **Upstream mTLS** beyond the single TLS switch.
- **mdns registry:** not reachable from OpenResty, and multicast is unsuitable at the edge anyway.

## 14. Conformance

`gateway/conformance` runs the cases below against a gateway address. Each case states the expected gRPC status. Both implementations MUST pass all of them before release. How to run the suite is described in [conformance/README.md](conformance/README.md).

| ID | Case |
|---|---|
| R1 | Convention route: `/greeter.Greeter/Hello` reaches `greeter` |
| R2 | Explicit `method` route beats `prefix`, which beats `service` |
| R3 | `/Greeter/Hello` with no route → `Unimplemented` |
| R4 | Malformed path → `Unimplemented` (message not checked, §3.1) |
| D1 | Scale 2 → 3 nodes: the new node receives traffic within 5 s |
| D2 | Scale 3 → 1 under constant load: no failed calls (§6) |
| D3 | Nodes with `protocol=mucp` never receive traffic |
| D4 | Registry stops: calls keep succeeding on cached nodes |
| L1 | `version_weights {v1: 100, v2: 0}` sends nothing to v2 |
| E1 | Service returns `errors.BadRequest` → client sees `InvalidArgument` with the go-micro JSON unchanged |
| E2 | No eligible nodes → `Unavailable`, `grpc-message` is go-micro JSON with `id: micro.gateway` |
| H1 | Call without `traceparent`: the service receives a valid generated one |
| H2 | Inbound `micro-gateway-account` from the client is not seen by the service |
| P1 | `ip-restriction` deny → `PermissionDenied` |
| P2 | `jwt-auth`: no token → `Unauthenticated`; token from `auth/jwt` → OK and `micro-gateway-account` = `sub` |
| P3 | `jwt-auth`: HS256 token signed with the public key → `Unauthenticated` (algorithm confusion) |
| P4 | `rate-limit` `{rate: 1, burst: 0}`: second immediate call → `ResourceExhausted` |
| P5 | Rules with an unknown plugin are rejected; previous rules stay active |
| C1 | Rules changed in the source take effect within 5 s, with no failed calls during the swap |
| S1 | Server-streaming and bidirectional calls are forwarded |

**HTTP/JSON entry cases.** These run only when the gateway offers the entry (`GATEWAY_HTTP_ADDR`). A gateway that offers it MUST pass them. Each case runs over HTTP/1.1, and over h2c unless the entry is HTTP/1.1-only (`GATEWAY_HTTP_H2C=false`).

| ID | Case |
|---|---|
| J1 | `POST /api/<svc>/TestService/UnaryCall` with `{}` → 200 JSON reply from `<svc>` |
| J2 | Routes apply: an explicit `method` route for `/<y>.TestService/Echo` sends `POST /api/<y>/TestService/Echo` to its upstream |
| J3 | Service returns `errors.BadRequest` → HTTP 400, body is that go-micro error |
| J4 | No eligible nodes → HTTP 503, body is a go-micro error with `id: micro.gateway` |
| J5 | `jwt-auth`: no token → 401; valid token → 200 with `micro-gateway-account` = `sub`; client headers reach the service, reserved ones do not, `traceparent` is generated |
| J6 | `GET` → 405; path with too few segments → 404; `Content-Type: text/plain` → 415 |
| T1 | HTTP rule `GET /v1/<svc>/size/{response_size}`: the path variable reaches the service as that numeric field; `?fill_username=true` with `params: {fill_username: bool}` arrives as a boolean; `?response_status.message=hi` sets the nested field |
| T2 | `body: "*"`: the JSON body is the message, and a path variable overrides the same field in it; query parameters are ignored |
| T3 | `body: "payload"`: the request body becomes that field, and query parameters fill the others |
| T4 | `response_body: "payload"`: the HTTP body is that field only |
| T5 | Specificity: `GET /v1/<svc>/items/special` beats `GET /v1/<svc>/items/{id}`, which beats `GET /v1/<svc>/**` |
| T6 | No HTTP rule matches and the path is not `/api/...` → 404; a `bool` param that is not `true`/`false` → 400 |
| T7 | `jwt-auth` with `forward_claims: {user-id: sub}`: the service receives `user-id` = `sub`, and a client-sent `user-id` header never reaches it |

**WebSocket entry cases.** These run only when the gateway offers the WebSocket entry (`GATEWAY_WS_URL`). Push and topic cases (W5, W6) also need the broker the gateway uses (`MICRO_BROKER`). A gateway that offers the entry, itself or by forwarding, MUST pass them.

| ID | Case |
|---|---|
| W1 | Two concurrent `call`s on one connection get `reply` messages matched by `id`, each from its service |
| W2 | Service returns `errors.BadRequest` → `error` with that go-micro error; no eligible nodes → `error` with status 503 and `id: micro.gateway`; the connection stays usable |
| W3 | `stream` with `close_send: true` to a server-streaming method → its messages, then `end`; a bidirectional stream answers each `send` |
| W4 | Without a `websocket` section, `/ws` → 404. With `jwt-auth` in `websocket.plugins`: no token → 401 and no upgrade; `?access_token=` with a valid token → upgraded |
| W5 | `push.ToUser(sub)` reaches both connections of that account and not a connection of another account |
| W6 | `subscribe` to a topic allowed by `websocket.topics` → `subscribed`, then `push.ToTopic` arrives as `event`; a topic not allowed → `error` 403; `{account}` patterns match only the connection's own account |
| W7 | Handshake headers and a call's `metadata` reach the service; with `forward_claims: {user-id: sub}` the service receives `user-id` = `sub`, and a client-set `user-id` never reaches it |
| W8 | A malformed message → `error` 400 and the connection stays usable; a binary frame → close 1003 |
