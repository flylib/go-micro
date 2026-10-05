# Gateway

Edge gateways that take gRPC calls from outside the cluster and route them to go-micro services. Both implementations follow one contract and are tested by one suite.

| Directory | What |
|---|---|
| [SPEC.md](SPEC.md) | The contract: routing, discovery, load balancing, retries, headers, errors, rules, bootstrap |
| [rules.schema.json](rules.schema.json), [rules.example.yaml](rules.example.yaml) | The rules format shared by both gateways |
| [proxy/](proxy) | Go gateway, built on go-micro's registry, selector and config sources |
| [openresty/](openresty) | OpenResty gateway with go-micro adapter Lua libraries |
| [conformance/](conformance) | Black-box suite that every gateway must pass |
| [api/](api) | HTTP server shell used by `micro run` and `micro server` for the dashboard. It is not an edge gateway. |

**HTTP clients.** Both gateways can also serve an HTTP/JSON entry. It accepts `POST /api/<service>/<Handler>/<Method>` and REST endpoints transcoded from `google.api.http` annotations (SPEC §2.1, §2.2). [protoc-gen-micro-gateway](../cmd/protoc-gen-micro-gateway) generates the rules from your protos.

**Service-side requirements.** Services must use the gRPC server (`server/grpc`) and register in etcd, Consul or Nacos. **Routing convention:** a service's proto `package` equals its registry name, so `/<service>.<Handler>/<Method>` needs no route.
