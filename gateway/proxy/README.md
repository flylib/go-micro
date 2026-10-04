# Go Gateway

The Go implementation of the go-micro edge gateway. It accepts gRPC calls from outside, applies the [rules](../rules.example.yaml), and forwards each call to a node of the target go-micro service. Its behaviour is defined by [SPEC.md](../SPEC.md), and it passes the full [conformance suite](../conformance) against etcd, Consul and Nacos.

## Run

```bash
go build -o micro-gateway ./cmd/micro-gateway

MICRO_REGISTRY=nacos \
MICRO_REGISTRY_ADDRESS=127.0.0.1:8848 \
MICRO_GATEWAY_RULES='nacos://127.0.0.1:8848/micro-gateway-rules?group=DEFAULT_GROUP' \
./micro-gateway
```

It is configured by the bootstrap settings of [SPEC.md §11](../SPEC.md#11-bootstrap):

| Variable | Default | Meaning |
|---|---|---|
| `MICRO_GATEWAY_ADDRESS` | `:8080` | Listen address (h2c) |
| `MICRO_REGISTRY` | — | `etcd`, `consul` or `nacos` |
| `MICRO_REGISTRY_ADDRESS` | — | Comma-separated `host:port` list |
| `MICRO_REGISTRY_NAMESPACE`, `MICRO_REGISTRY_GROUP` | public, `DEFAULT_GROUP` | Nacos namespace and group |
| `MICRO_GATEWAY_RULES` | — | Rules source: `file://`, `etcd://`, `consul://` or `nacos://`. `none` means convention routing with no plugins. |
| `MICRO_GATEWAY_TRUSTED_PROXIES` | — | CIDRs whose `x-forwarded-for` is trusted |
| `MICRO_GATEWAY_UPSTREAM_TLS` | `false` | Dial nodes with TLS |

**Behaviour on bad rules.** If the rules document is missing or invalid at start-up, the gateway refuses to start. If a later update is invalid, the gateway logs the error and keeps the previous rules.

**Streams.** Services are reached at `/<service>.<Handler>/<Method>`, the path go-micro's own gRPC client uses. All four RPC types are forwarded.

## As a library

```go
src, _ := proxy.SourceFromURI("file:///etc/micro/gateway/rules.yaml")
gw, err := proxy.New(
	proxy.Registry(nacos.NewRegistry(registry.Addrs("127.0.0.1:8848"))),
	proxy.RulesFrom(src), // any go-micro config/source.Source works
)
if err != nil {
	log.Fatal(err)
}
l, _ := net.Listen("tcp", ":8080")
gw.Serve(l)
```

## Design

**Transport: a transparent grpc-go proxy.** Calls arrive through `grpc.UnknownServiceHandler` and are relayed as opaque frames. The gateway never decodes a payload and needs no `.proto` files.

It does not use go-micro's server `Router` and client. Those would decode upstream errors into go-micro errors and back, which loses gRPC statuses that go-micro does not map. They also drop response headers, and they retry after a request has already been sent. The spec forbids all three (§6, §8). Raw grpc-go keeps statuses, headers and trailers byte-for-byte, and it shows exactly whether a request reached a node.

**Everything else is go-micro:**
- `registry` and `registry/cache` for discovery. The cache watches changes and serves cached nodes while the registry is down (§4.4).
- `selector` strategies and filters: `RoundRobin` (kept per route across calls), `Random`, `weighted.Strategy`, `weighted.VersionWeights`, `p2c.Strategy` with `p2c.Track`, and `FilterVersion` / `FilterLabel` / `FilterEndpoint`.
- `config/source` (etcd, consul, nacos) to load and watch rules.
- `logger` for access and reload logs. Access lines use `wrapper/logging`'s field names, so gateway and service lines join on `trace_id`.

**Retries happen only before a request is sent (§6).** A node is skipped when its connection is not ready within `timeout.connect`, or when the stream fails to open with `Unavailable`. Once a stream is open, the call is never retried.

**File rules are polled once per second, not watched.** Polling survives atomic rename and the ConfigMap symlink swaps that silently drop an inotify or kqueue watch.

## Known limitations

- **Inbound TLS** (SPEC §2 SHOULD) is not implemented yet. Terminate TLS in front of the gateway.
- **The gateway does not open its own span.** It continues or starts the W3C trace, so service spans are children of the client's span, not of a gateway span.
- **`timeout.read` is idle time between upstream frames.** It therefore also limits how long a client-streaming upload may run before the service replies.
- **Rate-limit buckets restart from full on every rules reload**, and they are local to each gateway instance (SPEC §13).
- **A data race inside nacos-sdk-go v2.3.5** (`RpcClient.reconnect` vs `notifyConnectionEvent`) shows up under `-race` when Nacos goes away. It is in the SDK, not the gateway.
