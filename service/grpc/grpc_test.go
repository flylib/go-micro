package grpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flylib/go-micro"
	"github.com/flylib/go-micro/client"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/registry"
	mgrpc "github.com/flylib/go-micro/util/grpc"
)

type Request struct {
	Name string `json:"name"`
}

type Response struct {
	Msg string `json:"msg"`
}

type Greeter struct{}

func (g *Greeter) Hello(ctx context.Context, req *Request, rsp *Response) error {
	rsp.Msg = "Hello " + req.Name
	return nil
}

func TestNewIsOnGRPC(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	svc := New("users", micro.Registry(reg), micro.Address("127.0.0.1:0"))
	if s := svc.Server().String(); s != "grpc" {
		t.Fatalf("server %q", s)
	}
	if c := svc.Client().String(); c != "grpc" {
		t.Fatalf("client %q", c)
	}
	so := svc.Server().Options()
	if so.Name != "users" || so.Registry != reg {
		t.Fatalf("server name %q registry %v", so.Name, so.Registry)
	}
	if svc.Client().Options().Registry != reg {
		t.Fatal("client did not get the registry")
	}
}

// A service made by New is called by a client made by New.
func TestCallBetweenServices(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	users := New("users", micro.Registry(reg), micro.Address("127.0.0.1:0"), micro.HandleSignal(false))
	if err := users.Handle(new(Greeter)); err != nil {
		t.Fatal(err)
	}
	if err := users.Start(); err != nil {
		t.Fatal(err)
	}
	defer users.Stop()

	caller := New("orders", micro.Registry(reg))
	c := caller.Client()
	req := c.NewRequest("users", "Greeter.Hello", &Request{Name: "Alice"}, client.WithContentType("application/grpc+json"))
	var rsp Response
	if err := c.Call(context.Background(), req, &rsp); err != nil {
		t.Fatal(err)
	}
	if rsp.Msg != "Hello Alice" {
		t.Fatalf("reply %q", rsp.Msg)
	}
}

func TestEnvironmentThenCode(t *testing.T) {
	t.Setenv("MICRO_SERVER_ADDRESS", ":9001")
	t.Setenv("MICRO_SERVER_VERSION", "v3")
	t.Setenv("MICRO_REGISTRY", "memory")

	svc := New("users")
	so := svc.Server().Options()
	if so.Address != ":9001" || so.Version != "v3" || so.Registry.String() != "memory" {
		t.Fatalf("from the environment: %q %q %s", so.Address, so.Version, so.Registry)
	}

	// code wins over the environment
	svc = New("users", micro.Address(":7000"))
	if a := svc.Server().Options().Address; a != ":7000" {
		t.Fatalf("address %q, want the one set in code", a)
	}
}

func TestRandomPort(t *testing.T) {
	for addr, want := range map[string]bool{
		"": true, ":0": true, "127.0.0.1:0": true, "[::]:0": true, "nonsense": true,
		":9001": false, "0.0.0.0:9001": false, "[::]:9001": false,
	} {
		if got := randomPort(addr); got != want {
			t.Errorf("randomPort(%q) = %v", addr, got)
		}
	}
}

type Echo struct{}

type EchoResponse struct {
	Msg    string `json:"msg"`
	Header string `json:"header"`
}

func (e *Echo) Say(ctx context.Context, req *Request, rsp *EchoResponse) error {
	if req.Name == "" {
		return merr.BadRequest("users", "name is required")
	}
	rsp.Msg = "Hello " + req.Name
	rsp.Header, _ = metadata.Get(ctx, "x-test") // gRPC metadata keys are lowercase
	return nil
}

// The CLI and dashboard call a real go-micro gRPC server with
// util/grpc.CallJSON, without the gRPC library.
func TestCallJSONAgainstTheGRPCServer(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	svc := New("users", micro.Registry(reg), micro.Address("127.0.0.1:0"), micro.HandleSignal(false))
	if err := svc.Handle(new(Echo)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	addr := svc.Server().Options().Address
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, err := mgrpc.CallJSON(ctx, addr, mgrpc.Path("users", "Echo.Say"), []byte(`{"name":"Alice"}`), map[string]string{"X-Test": "v1"})
	if err != nil {
		t.Fatal(err)
	}
	var rsp EchoResponse
	if err := json.Unmarshal(reply, &rsp); err != nil || rsp.Msg != "Hello Alice" || rsp.Header != "v1" {
		t.Fatalf("reply %s: %v", reply, err)
	}

	// the service's own error comes back unchanged
	_, err = mgrpc.CallJSON(ctx, addr, mgrpc.Path("users", "Echo.Say"), nil, nil)
	if e := merr.Parse(err.Error()); e.Id != "users" || e.Code != 400 || e.Detail != "name is required" {
		t.Fatalf("service error: %v", err)
	}
	if _, err = mgrpc.CallJSON(ctx, addr, mgrpc.Path("users", "Echo.Nope"), nil, nil); err == nil {
		t.Fatal("unknown endpoint succeeded")
	}
	_, err = mgrpc.CallJSON(ctx, "127.0.0.1:1", mgrpc.Path("users", "Echo.Say"), nil, nil)
	if !client.IsConnectError(err) {
		t.Fatalf("node down: want a connect error, got %v", err)
	}
}
