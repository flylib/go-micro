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

### Exposing a service over REST

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

**Service-side requirements.** Services must use the gRPC server (`server/grpc`) and register in etcd, Consul or Nacos. **Routing convention:** a service's proto `package` equals its registry name, so `/<service>.<Handler>/<Method>` needs no route.
