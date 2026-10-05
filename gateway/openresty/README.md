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

Bootstrap settings ([SPEC.md §11](../SPEC.md#11-bootstrap)) are the same as the Go gateway's:

| Variable | Default | Meaning |
|---|---|---|
| `MICRO_GATEWAY_ADDRESS` | `:8080` | Listen address (h2c) |
| `MICRO_REGISTRY` | — | `etcd`, `consul` or `nacos` |
| `MICRO_REGISTRY_ADDRESS` | — | Comma-separated `host:port` list |
| `MICRO_REGISTRY_NAMESPACE`, `MICRO_REGISTRY_GROUP` | public, `DEFAULT_GROUP` | Nacos namespace and group |
| `MICRO_GATEWAY_RULES` | — | `file://`, `etcd://`, `consul://` or `nacos://`, or `none` |
| `MICRO_GATEWAY_TRUSTED_PROXIES` | — | CIDRs whose `x-forwarded-for` is trusted |
| `MICRO_GATEWAY_UPSTREAM_TLS` | `false` | Dial nodes with TLS (`grpcs://`) |

**Behaviour on bad rules.** A missing or invalid rules document stops the gateway, and the container exits with code 1. A later invalid update is logged and the previous rules stay.

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
