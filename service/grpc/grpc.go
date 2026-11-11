// Package grpc assembles a go-micro service the way the edge gateways and
// operations tools expect it: on the gRPC server and client, configured
// from the environment.
//
//	import (
//		grpcservice "github.com/flylib/go-micro/service/grpc"
//		_ "github.com/flylib/go-micro/registry/nacos" // selectable with MICRO_REGISTRY=nacos
//	)
//
//	svc := grpcservice.New("users", micro.Address(":9001"))
//	svc.Handle(new(Users))
//	svc.Run()
//
// New is micro.New with three things set before the caller's options:
//
//   - the gRPC server (server/grpc): reachable by the gateways and any gRPC
//     client, with the standard health service and graceful stop;
//   - the gRPC client (client/grpc);
//   - micro.FromEnv: registry, broker, store and server address from
//     MICRO_* variables.
//
// The caller's options come last and win. A service that registers
// somewhere other than mDNS without a fixed listen address logs a warning
// at start: Kubernetes Services and eBPF tools need a fixed port.
package grpc

import (
	"net"

	"github.com/flylib/go-micro"
	grpcclient "github.com/flylib/go-micro/client/grpc"
	"github.com/flylib/go-micro/logger"
	grpcserver "github.com/flylib/go-micro/server/grpc"
)

// New returns a service on gRPC, configured from the environment and then
// by opts.
func New(name string, opts ...micro.Option) micro.Service {
	var svc micro.Service
	base := []micro.Option{
		micro.Server(grpcserver.NewServer()),
		micro.Client(grpcclient.NewClient()),
		micro.FromEnv(),
		micro.BeforeStart(func() error {
			// local development (mDNS, memory) is fine on a random port
			local := false
			if reg := svc.Options().Registry; reg != nil {
				local = reg.String() == "mdns" || reg.String() == "memory"
			}
			if addr := svc.Server().Options().Address; !local && randomPort(addr) {
				svc.Options().Logger.Logf(logger.WarnLevel,
					"service %s listens on a random port (%q): set micro.Address(\":9001\") or MICRO_SERVER_ADDRESS, "+
						"Kubernetes Services and eBPF tools need a fixed port", name, addr)
			}
			return nil
		}),
	}
	svc = micro.New(name, append(base, opts...)...)
	return svc
}

// randomPort reports whether a listen address leaves the port to the
// system.
func randomPort(addr string) bool {
	if addr == "" {
		return true
	}
	_, port, err := net.SplitHostPort(addr)
	return err != nil || port == "" || port == "0"
}
