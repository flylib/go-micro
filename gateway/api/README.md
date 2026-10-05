# API Gateway shell

`gateway/api` is the HTTP server shell behind the `micro run` and `micro server` dashboards. It owns the listener, the `http.ServeMux` and shutdown. All routes come from a `HandlerRegistrar` you pass in.

It is not an edge gateway. For traffic from outside the cluster, use the [Go or OpenResty gateway](..): gRPC, HTTP/JSON, REST transcoding and WebSocket.

## Usage

```go
package main

import (
    "context"
    "net/http"

    "github.com/flylib/go-micro/gateway/api"
)

func main() {
    gw, err := api.New(api.Options{
        Address: ":8080",
        Context: context.Background(),
        HandlerRegistrar: func(mux *http.ServeMux) error {
            mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
                w.Write([]byte("Hello from gateway"))
            })
            return nil
        },
    })
    if err != nil {
        panic(err)
    }
    gw.Wait() // blocks until the context ends, then shuts down
}
```

`api.Run(opts)` is `New` plus `Wait`. `gw.Stop()` shuts down at once, `gw.Addr()` returns the configured address, and `gw.Mux()` returns the mux, so more routes can be added later.

## Options

```go
type Options struct {
    Address          string                          // listen address, e.g. ":8080"
    AuthEnabled      bool                            // tells the registrar to add auth; the shell itself checks nothing
    Context          context.Context                 // cancellation (default: context.Background())
    Logger           *log.Logger                     // default: log.Default()
    HandlerRegistrar func(mux *http.ServeMux) error // registers every route
    Registry         registry.Registry               // default: registry.DefaultRegistry
}
```

## In the micro CLI

Both `micro run` and `micro server` start the shell through `server.StartGateway` (`cmd/micro/server/gateway.go`), with auth enabled. That registrar (`registerHandlers` in `cmd/micro/server/server.go`) serves:

- **The dashboard**: services, endpoints, logs and status, with forms to call endpoints.
- **`POST /api/{service}/{Handler}/{Method}`**: calls the service with the JSON body through the default client, as `micro call` does (`cmd/micro/server/api.go`). Services on the gRPC server are answered with 501; use an edge gateway for those.
- **`/health`, `/health/live`, `/health/ready`**, without auth.
- **JWT login, tokens, users and endpoint scopes** under `/auth/`.

```go
gw, err := server.StartGateway(server.GatewayOptions{ // GatewayOptions = api.Options
    Address:     ":8080",
    AuthEnabled: true,
    Context:     ctx,
})
```
