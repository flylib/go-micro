# gRPC service preset

`micro.New` assembled the way the edge gateways and operations tools expect it: on the gRPC server and client, configured from the environment. `micro new` generates services on it.

```go
import (
	"github.com/flylib/go-micro"
	grpcservice "github.com/flylib/go-micro/service/grpc"

	_ "github.com/flylib/go-micro/registry/nacos" // selectable with MICRO_REGISTRY=nacos
)

svc := grpcservice.New("users")
svc.Handle(new(Users))
svc.Run()
```

```bash
MICRO_SERVER_ADDRESS=:9001 MICRO_REGISTRY=nacos MICRO_REGISTRY_ADDRESS=nacos:8848 ./users
```

`New(name, opts...)` is `micro.New` with three options before yours:

- **`micro.Server(grpcserver.NewServer())`.** The service is reachable by both gateways and any gRPC client, serves `grpc.health.v1` for readiness and liveness probes, and stops gracefully ([server](../../server)).
- **`micro.Client(grpcclient.NewClient())`.** Calls to other services go over gRPC, and unreachable nodes are retried on another node ([client](../../client)).
- **`micro.FromEnv()`.** Registry, broker, store and server address come from `MICRO_*` variables ([service](../README.md#from-the-environment)).

Your options come last, so they win over the environment: `grpcservice.New("users", micro.Address(":9001"))` listens on `:9001` whatever `MICRO_SERVER_ADDRESS` says.

A service that registers anywhere but mDNS or the memory registry without a fixed listen address logs a warning at start. Kubernetes Services and eBPF tools such as Coroot need a fixed port.

It is its own module because the core module cannot import `server/grpc` and `client/grpc`:

```bash
go get github.com/flylib/go-micro/service/grpc@main
```
