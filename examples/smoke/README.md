# Smoke Tests

End-to-end smoke tests for the framework, runnable as a standalone example module.

## Core (no external dependencies)

In-process tests on memory registry/broker covering the full chain —
RPC round-trip, register/deregister, pub/sub, KV store, and two services
calling each other in one process:

```bash
go test -run TestSmoke -v .
```

## Nacos integration (podman)

`nacos_test.go` runs the nacos registry plugin against a real server:
register → GetService → ListServices → Watch → Deregister.
It skips automatically when no nacos is reachable.

Start a local nacos with podman (both ports required — 9848 is the gRPC
port used by nacos-sdk-go v2):

```bash
podman run -d --name nacos-smoke -e MODE=standalone -e NACOS_AUTH_ENABLE=false \
  -p 8848:8848 -p 9848:9848 docker.io/nacos/nacos-server:v2.5.1
```

Then:

```bash
go test -run TestSmokeNacos -v .
```

Point at another server with `NACOS_ADDR=host:8848`.
