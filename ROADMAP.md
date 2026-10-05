# Go Micro Roadmap

Priorities come from the teams that build on this fork: services on gRPC, registered in Nacos, etcd or Consul, behind the edge gateways in [gateway/](gateway). Changes are listed in [CHANGELOG.md](CHANGELOG.md).

## Done (2026)

- **Edge gateways.** A Go gateway and an OpenResty gateway, with one contract ([gateway/SPEC.md](gateway/SPEC.md)) and one conformance suite. They cover discovery in etcd, Consul and Nacos, hot-reloaded rules, load balancing, retries, IP restriction, JWT auth and rate limiting.
- **HTTP clients.**
  - An HTTP/JSON entry and REST transcoding from `google.api.http` annotations.
  - JWT claims forwarded to services as metadata.
  - [protoc-gen-micro-gateway](cmd/protoc-gen-micro-gateway) generates the gateway rules.
- **Backends.**
  - A Nacos registry and Nacos config source.
  - `store/redis` and `sync/redis` for Redis, Dragonfly and Valkey.
  - Per-plugin modules, pinned by pseudo-version.
- **Operations.**
  - `wrapper/logging` writes trace-aware access logs.
  - Every module declares the same Go version (1.26).

## Next

- [ ] **MongoDB backend** for `store` and `model`.
- [ ] **WebSocket bridge.** Long-lived client connections at the gateway that carry RPC calls and server pushes to and from services.
- [ ] **Gateway deployment.** Kubernetes manifests or Helm charts for both gateways, and Prometheus metrics for the gateway.

## Under discussion

- **Object storage and upload signing.** For now we leave it to application code (for example `minio-go` against MinIO or S3), rather than adding a store plugin.
- **mTLS between gateway and services**, and secret-manager integration for keys.

## Versioning

There are no release tags. Consumers depend on pseudo-versions:

```bash
go get github.com/flylib/go-micro/<module>@main
```

In-repo modules pin each other to the same pushed commit. Breaking changes are called out in the changelog.

## Contributing

Pick an item, open an issue to agree on the approach, then send a PR with tests. See [CONTRIBUTING.md](CONTRIBUTING.md).
