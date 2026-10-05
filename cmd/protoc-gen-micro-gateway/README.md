# protoc-gen-micro-gateway

A protoc plugin that turns `google.api.http` annotations into gateway HTTP rules ([gateway/SPEC.md §2.2](../../gateway/SPEC.md)). Both gateways (`gateway/proxy`, `gateway/openresty`) read the same rules.

```bash
go install github.com/flylib/go-micro/cmd/protoc-gen-micro-gateway@main
```

## Use

Annotate methods as for grpc-gateway:

```proto
syntax = "proto3";
package users;   // = the service's registry name

import "google/api/annotations.proto";

service Users {
  rpc Get(GetRequest) returns (User) {
    option (google.api.http) = { get: "/v1/users/{id}" };
  }
  rpc Update(UpdateRequest) returns (User) {
    option (google.api.http) = {
      patch: "/v1/users/{id}"
      body: "user"
      additional_bindings { put: "/v1/users/{id}" body: "user" }
    };
  }
}
```

Generate. `google/api/annotations.proto` comes from [googleapis](https://github.com/googleapis/googleapis), or from buf's `buf.build/googleapis/googleapis`:

```bash
protoc -I. -I<googleapis> --micro-gateway_out=. users.proto
```

This writes `users.gateway.yaml` next to `users.proto`:

```yaml
http_rules:
  - method: GET
    path: "/v1/users/{id}"
    target: "/users.Users/Get"
    params: {"id": string, "verbose": bool}
  - method: PATCH
    path: "/v1/users/{id}"
    target: "/users.Users/Update"
    body: "user"
    params: {"id": string, "user.email": string, "user.name": string}
  ...
```

Copy or concatenate the `http_rules` lists of your services into the gateway rules document. It lives wherever `MICRO_GATEWAY_RULES` points: a file, or an etcd, Consul or Nacos key.

## What it emits

| Annotation | Rule |
|---|---|
| `get` / `put` / `post` / `delete` / `patch` | `method` and `path` |
| `body`, `response_body` | the same fields |
| `additional_bindings` | one more rule each, same target |
| the method | `target: /<package>.<Service>/<Method>` |
| the request message | `params`: its fields by path, typed `bool`, `number`, `repeated` or `string` |

Notes on `params`:

- **Nested messages.** Fields of nested messages are listed by dotted path, up to 4 levels. A message that contains itself is not followed again.
- **Excluded fields.** Map fields are left out, since paths and query strings cannot set them.
- **Wrapper types.** `google.protobuf` wrapper types, `Timestamp`, `Duration` and `FieldMask` count as their JSON scalar.
- **Allow-list.** A non-empty `params` is an allow-list. The gateway ignores query parameters that are not fields of the request, as grpc-gateway does.

## Refused annotations

These annotations make the plugin fail, because the gateway cannot serve them:

- `custom` patterns;
- annotations on streaming methods (the HTTP entry is unary only).

## Service side

Nothing changes on the service side:

- the call reaches the service as `application/grpc+json`, which go-micro's gRPC server decodes with `protojson`;
- the routing convention (proto package = registry name) makes the target resolve;
- with `jwt-auth` `forward_claims: {user-id: sub}`, the handler reads the caller from metadata:

```go
uid, _ := metadata.Get(ctx, "user-id")
```
