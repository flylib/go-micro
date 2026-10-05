# Nacos registry

`registry.Registry` on Nacos 2.x and 3.x, built on nacos-sdk-go v2. It is tested on Nacos 2.4.3 and 3.1.1, with and without authentication.

```go
import (
	"os"

	"github.com/flylib/go-micro"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/registry/nacos"
)

reg := nacos.NewRegistry(
	registry.Addrs("10.0.0.1:8848"),           // the HTTP port; gRPC is derived from it
	nacos.WithAuth("nacos", os.Getenv("NACOS_PASSWORD")), // auth is on by default since Nacos 3.0
	nacos.WithNamespaceId("dev"),              // default: public
	nacos.WithGroupName("DEFAULT_GROUP"),
)
service := micro.New("orders", micro.Registry(reg))
```

## Connection

- **Protocol.** The SDK registers, subscribes and receives pushes over **gRPC**. It connects to the HTTP port + 1000: 9848 for `:8848`. Some calls, login among them, still use HTTP on the port you give. **Open both ports.**
- **Port mappings.** Keep the 1000 offset (for example `-p 18848:8848 -p 19848:9848`), or set the gRPC port with `nacos.WithGRPCPort(19848)`.
- **Authentication.** On Nacos 3.0 and later it is on by default. Pass `nacos.WithAuth(username, password)`; the SDK logs in and refreshes its token. Without credentials, calls fail with `401 User not found`.
- **Full control.** `nacos.WithClientConfig` and `nacos.WithServerConfigs` (TLS, timeouts, several servers with their own ports) or `nacos.WithNamingClient` (a client you built) override the options above.

## Behaviour

- **One instance per node.** Each node is one Nacos instance, with go-micro's metadata stored as plain instance metadata. The [OpenResty gateway](../../gateway/openresty) reads this format.
- **Watching.** The watcher turns each Nacos push into per-node create and delete events, so caches drop removed nodes.
- **One instance per client.** A Nacos 2.x+ client holds one instance per service. Each process (each service node) needs its own registry client, which go-micro services have anyway.

The config source [`config/source/nacos`](../../config/source/nacos) takes the same `WithAuth` and `WithGRPCPort` options. The edge gateways read `MICRO_REGISTRY_USERNAME` and `MICRO_REGISTRY_PASSWORD` ([gateway/SPEC.md §11](../../gateway/SPEC.md#11-bootstrap)).
