# Gateway Conformance Suite

Black-box tests that every go-micro gateway implementation must pass. Both `gateway/proxy` (Go) and `gateway/openresty` are tested with it. Each test is one case of [SPEC.md §14](../SPEC.md#14-conformance), named by its ID (`TestR1_…`, `TestP3_…`).

The suite drives a running gateway from the outside:

1. It starts its own backend services in-process and registers them in the registry the gateway watches.
2. It writes rules to the source the gateway loads.
3. It calls through the gateway with a plain gRPC client.

It needs no `.proto` files. Backends serve `grpc.testing.TestService` from `google.golang.org/grpc/interop/grpc_testing`, and go-micro ignores the package part of the path. So `/<any-service>.TestService/UnaryCall` reaches a backend registered as `<any-service>`.

## Run

Start the gateway first, then point the suite at the same registry and rules source:

```bash
export GATEWAY_ADDR=localhost:8080
export MICRO_REGISTRY=nacos                 # etcd | consul | nacos
export MICRO_REGISTRY_ADDRESS=127.0.0.1:8848
export MICRO_GATEWAY_RULES='nacos://127.0.0.1:8848/micro-gateway-rules?group=DEFAULT_GROUP'
go test -count=1 -v .
```

| Variable | Meaning |
|---|---|
| `GATEWAY_ADDR` | Gateway address. When unset, the gateway cases are skipped. |
| `GATEWAY_HTTP_ADDR` | Gateway HTTP/JSON entry address (SPEC §2.1). When unset, the J cases are skipped. |
| `GATEWAY_HTTP_H2C` | `false` for an HTTP/1.1-only entry: the J cases then skip their h2c runs. |
| `GATEWAY_WS_URL` | WebSocket entry, e.g. `ws://localhost:8090/ws` (SPEC §2.3). When unset, the W cases are skipped. |
| `MICRO_BROKER`, `MICRO_BROKER_ADDRESS` | The broker the gateway uses for push (`nats`). When unset, W5 and W6 are skipped. |
| `MICRO_REGISTRY`, `MICRO_REGISTRY_ADDRESS` | Registry the gateway reads. Backends register here. |
| `MICRO_REGISTRY_NAMESPACE`, `MICRO_REGISTRY_GROUP` | Nacos namespace and group, if not the defaults. |
| `MICRO_REGISTRY_USERNAME`, `MICRO_REGISTRY_PASSWORD` | Nacos credentials, for servers with authentication on. |
| `MICRO_GATEWAY_RULES` | Rules source URI the gateway was started with: `file://`, `etcd://`, `consul://` or `nacos://`. The suite overwrites it. |
| `CONFORMANCE_ADVERTISE_HOST` | Host the gateway can reach backends at. Defaults to the first private IP. When the gateway runs in a container, use an address the container can reach. |
| `CONFORMANCE_REGISTRY_STOP` / `CONFORMANCE_REGISTRY_START` | Shell commands that stop and start the registry, for case D4. When unset, D4 is skipped. |

The variable names match the gateway bootstrap settings ([SPEC.md §11](../SPEC.md#11-bootstrap)), so a single env file can configure both the gateway and the suite.

**Gateway in a container on a macOS Docker VM (colima, Docker Desktop).** Run the suite in a container too, on the gateway's network:

```bash
CGO_ENABLED=0 GOOS=linux go test -c -o conf-linux .
docker run --rm --network <gateway-net> -v "$PWD:/e2e" -e GATEWAY_ADDR=<gateway>:8080 ... alpine /e2e/conf-linux -test.v
```

Container-to-host connections through the VM drop intermittently. With the suite on the host, backends are reached through the VM, and those drops show up as false failures. D4 needs `docker` and is skipped in this mode.

**Use a disposable rules source.** Each case replaces the whole rules document. Every service the suite starts is named with a random per-run prefix, so runs do not collide in the registry.

## How a case knows its rules are live

`apply` adds a fresh probe route to every rules document. The probe route points at a long-lived echo service. After writing the document, `apply` polls the probe until the gateway serves it.

This turns the spec's "rules take effect within 5 s" into a measurable barrier. Case C1 asserts on that measurement. Every document the suite writes is also validated against `rules.schema.json` first. The only exception is the deliberately invalid document in P5.

## Harness self-tests

The `TestHarness_*` tests call backends directly, with no gateway in between. They always run, with no environment needed. Their job is to check the suite itself, so that a failing conformance run points at the gateway rather than at the suite.

```bash
go test -count=1 -run Harness .
```
