package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
	grpcserver "github.com/flylib/go-micro/server/grpc"
	pb "google.golang.org/grpc/interop/grpc_testing"
)

// echo is what every backend reply carries in Payload.Body, so a test can
// see which node answered and which metadata reached the service.
type echo struct {
	Service  string            `json:"service"`
	Version  string            `json:"version"`
	Node     string            `json:"node"`
	Metadata map[string]string `json:"metadata"`
}

// header looks a key up case-insensitively: gRPC lowercases keys, go-micro
// metadata may title-case them.
func (e echo) header(key string) (string, bool) {
	for k, v := range e.Metadata {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// backend is one registered node of a test service. The go-micro server
// routes by handler struct name and ignores the package part of the gRPC
// path, so a backend answers /<anything>.TestService/<Method> and
// /<anything>.Alt/<Method>: tests choose service names freely.
type backend struct {
	name, version, id string
	protocol          string
	srv               server.Server
	calls             atomic.Int64
}

type backendOptions struct {
	version  string
	protocol string // "grpc" (default) or "mucp"
	metadata map[string]string
}

type backendOption func(*backendOptions)

func withVersion(v string) backendOption { return func(o *backendOptions) { o.version = v } }
func withMUCP() backendOption            { return func(o *backendOptions) { o.protocol = "mucp" } }

// startBackend starts a node of service name, registered in reg and
// advertised at host. Stop it with stop(); the test cleanup also stops it.
func startBackend(reg registry.Registry, host, name string, opts ...backendOption) (*backend, error) {
	o := backendOptions{version: "v1", protocol: "grpc"}
	for _, opt := range opts {
		opt(&o)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	b := &backend{
		name:     name,
		version:  o.version,
		id:       fmt.Sprintf("%s-%d", name, port),
		protocol: o.protocol,
	}

	sopts := []server.Option{
		server.Name(name),
		server.Version(o.version),
		server.Id(fmt.Sprint(port)), // registered as <name>-<id>, i.e. b.id
		server.Address(fmt.Sprintf(":%d", port)),
		server.Advertise(net.JoinHostPort(host, fmt.Sprint(port))),
		server.Registry(reg),
		server.Metadata(o.metadata),
		server.RegisterTTL(30 * time.Second),
		server.RegisterInterval(10 * time.Second),
	}
	// grpc and mucp servers each stamp their own protocol into metadata
	if o.protocol == "mucp" {
		b.srv = server.NewRPCServer(sopts...)
	} else {
		b.srv = grpcserver.NewServer(sopts...)
	}

	svc := &TestService{b: b}
	if err := b.srv.Handle(b.srv.NewHandler(svc)); err != nil {
		return nil, err
	}
	if err := b.srv.Handle(b.srv.NewHandler(&Alt{TestService: svc})); err != nil {
		return nil, err
	}
	if err := b.srv.Start(); err != nil {
		return nil, err
	}
	return b, nil
}

// stop deregisters the node and closes its listener.
func (b *backend) stop() error {
	if b.srv == nil {
		return nil
	}
	err := b.srv.Stop()
	b.srv = nil
	return err
}

func (b *backend) address() string { return b.srv.Options().Advertise }

func freePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// TestService implements the grpc.testing.TestService methods the suite
// uses, in go-micro handler form.
type TestService struct{ b *backend }

// Alt exposes the same methods under a second handler name, for routes
// that must tell two paths of one service apart.
type Alt struct{ *TestService }

func (s *TestService) reply(ctx context.Context) *pb.Payload {
	s.b.calls.Add(1)
	md, _ := metadata.FromContext(ctx)
	body, _ := json.Marshal(echo{Service: s.b.name, Version: s.b.version, Node: s.b.id, Metadata: md})
	return &pb.Payload{Body: body}
}

// UnaryCall echoes the call, or fails with a go-micro error when
// ResponseStatus asks for one (code is the HTTP-style go-micro code).
func (s *TestService) UnaryCall(ctx context.Context, req *pb.SimpleRequest, rsp *pb.SimpleResponse) error {
	if st := req.GetResponseStatus(); st != nil && st.Code != 0 {
		return merr.New(s.b.name, st.Message, st.Code)
	}
	rsp.Payload = s.reply(ctx)
	return nil
}

// Echo is UnaryCall under another method name, for method-level routes.
func (s *TestService) Echo(ctx context.Context, req *pb.SimpleRequest, rsp *pb.SimpleResponse) error {
	return s.UnaryCall(ctx, req, rsp)
}

// StreamingOutputCall reads one request and sends one reply per
// ResponseParameters entry.
func (s *TestService) StreamingOutputCall(ctx context.Context, stream server.Stream) error {
	var req pb.StreamingOutputCallRequest
	if err := stream.Recv(&req); err != nil {
		return err
	}
	for range req.GetResponseParameters() {
		if err := stream.Send(&pb.StreamingOutputCallResponse{Payload: s.reply(ctx)}); err != nil {
			return err
		}
	}
	return nil
}

// FullDuplexCall replies to every request until the client closes.
func (s *TestService) FullDuplexCall(ctx context.Context, stream server.Stream) error {
	for {
		var req pb.StreamingOutputCallRequest
		if err := stream.Recv(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.Send(&pb.StreamingOutputCallResponse{Payload: s.reply(ctx)}); err != nil {
			return err
		}
	}
}
