// Package caller calls a service endpoint with a JSON request for the
// micro CLI and dashboard, whatever server the service runs on: nodes on
// the gRPC server are called with gRPC+JSON over HTTP/2, others through
// the default (mucp) client.
package caller

import (
	"context"
	"math/rand"
	"strings"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/codec/bytes"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
	mgrpc "github.com/flylib/go-micro/util/grpc"
)

const (
	errorID   = "micro.caller"
	grpcProto = "grpc"
)

// Call sends the JSON body to service's endpoint ("Handler.Method") and
// returns the JSON reply. Metadata in ctx is sent along. Errors are
// go-micro errors.
func Call(ctx context.Context, reg registry.Registry, cl client.Client, service, endpoint string, body []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	svcs, err := reg.GetService(service)
	if err != nil || len(svcs) == 0 {
		return nil, merr.NotFound(errorID, "service %s not found", service)
	}

	var grpcNodes []*registry.Node
	other := false
	for _, s := range svcs {
		for _, n := range s.Nodes {
			if n.Metadata["protocol"] == grpcProto {
				grpcNodes = append(grpcNodes, n)
			} else {
				other = true
			}
		}
	}

	if len(grpcNodes) > 0 {
		md, _ := metadata.FromContext(ctx)
		// try the nodes in random order until one can be connected to
		rand.Shuffle(len(grpcNodes), func(i, j int) { grpcNodes[i], grpcNodes[j] = grpcNodes[j], grpcNodes[i] })
		var last error
		for _, n := range grpcNodes {
			reply, err := mgrpc.CallJSON(ctx, n.Address, mgrpc.Path(service, endpoint), body, md)
			if err == nil {
				return reply, nil
			}
			if last = err; !client.IsConnectError(err) {
				return nil, err
			}
		}
		if !other {
			return nil, last
		}
	}

	req := cl.NewRequest(service, endpoint, &bytes.Frame{Data: body}, client.WithContentType("application/json"))
	var rsp bytes.Frame
	// the default client cannot talk to nodes on the gRPC server
	if err := cl.Call(ctx, req, &rsp, client.WithSelectOption(selector.WithFilter(withoutGRPC))); err != nil {
		if e := merr.Parse(err.Error()); e.Code > 0 {
			return nil, e
		}
		return nil, merr.InternalServerError(errorID, "%s", err.Error())
	}
	if len(rsp.Data) == 0 {
		return []byte("{}"), nil
	}
	return rsp.Data, nil
}

// withoutGRPC drops nodes that run the gRPC server.
func withoutGRPC(svcs []*registry.Service) []*registry.Service {
	out := make([]*registry.Service, 0, len(svcs))
	for _, s := range svcs {
		cp := *s
		cp.Nodes = nil
		for _, n := range s.Nodes {
			if n.Metadata["protocol"] != grpcProto {
				cp.Nodes = append(cp.Nodes, n)
			}
		}
		if len(cp.Nodes) > 0 {
			out = append(out, &cp)
		}
	}
	return out
}
