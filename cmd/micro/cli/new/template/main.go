package template

var (
	MainSRV = `package main

import (
	"{{.Dir}}/handler"
	pb "{{.Dir}}/proto"

	grpcservice "github.com/flylib/go-micro/service/grpc"
)

func main() {
	// Create a service on gRPC. It is configured from the environment:
	// MICRO_SERVER_ADDRESS (e.g. :9001), MICRO_REGISTRY, MICRO_BROKER, ...
	// To select a plugin by name, link it in with a blank import, e.g.
	//   _ "github.com/flylib/go-micro/registry/nacos"
	service := grpcservice.New("{{lower .Alias}}")

	// Parse command line flags
	service.Init()

	// Register handler
	pb.Register{{title .Alias}}Handler(service.Server(), handler.New())

	// Run service
	service.Run()
}
`
)
