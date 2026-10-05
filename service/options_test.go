package service

import (
	"context"
	"testing"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

type Greeter struct{}

type HelloRequest struct{}
type HelloResponse struct{}

func (g *Greeter) Hello(ctx context.Context, req *HelloRequest, rsp *HelloResponse) error { return nil }

func passHandler(h server.HandlerFunc) server.HandlerFunc { return h }

func passCall(c client.CallFunc) client.CallFunc { return c }

type wrappedClient struct{ client.Client }

func wrapClient(c client.Client) client.Client { return &wrappedClient{c} }

// endpoints starts the server and returns the endpoints it registered.
func endpoints(t *testing.T, s server.Server, reg registry.Registry, name string) []string {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	svcs, err := reg.GetService(name)
	if err != nil || len(svcs) == 0 {
		t.Fatalf("%s not registered in the service's registry: %v", name, err)
	}
	var out []string
	for _, ep := range svcs[0].Endpoints {
		out = append(out, ep.Name)
	}
	return out
}

// A server set with Server gets every service-level setting given before
// it: these used to stay on the replaced default server.
func TestServerKeepsEarlierOptions(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	b := broker.NewMemoryBroker()
	srv := server.NewRPCServer()
	o := newOptions(
		Name("orders"),
		Version("v2"),
		Address("127.0.0.1:0"),
		Metadata(map[string]string{"zone": "a"}),
		Registry(reg),
		Broker(b),
		WrapHandler(passHandler),
		Handle(new(Greeter)),
		Server(srv),
	)
	if o.Server != srv {
		t.Fatal("Server not set")
	}
	so := srv.Options()
	if so.Name != "orders" || so.Version != "v2" || so.Address != "127.0.0.1:0" || so.Metadata["zone"] != "a" {
		t.Fatalf("server identity: name %q version %q address %q metadata %v", so.Name, so.Version, so.Address, so.Metadata)
	}
	if so.Registry != reg || so.Broker != b {
		t.Fatalf("server registry %v broker %v: not the service's", so.Registry, so.Broker)
	}
	if len(so.HdlrWrappers) != 1 {
		t.Fatalf("handler wrappers: %d, want 1", len(so.HdlrWrappers))
	}
	if eps := endpoints(t, srv, reg, "orders"); len(eps) != 1 || eps[0] != "Greeter.Hello" {
		t.Fatalf("endpoints %v: the handler did not reach the new server", eps)
	}
}

// Options take effect whatever their order.
func TestOptionOrderDoesNotMatter(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	for name, order := range map[string]func(s server.Server) []Option{
		"server first": func(s server.Server) []Option { return []Option{Server(s), Name("x"), Registry(reg)} },
		"server last":  func(s server.Server) []Option { return []Option{Name("x"), Registry(reg), Server(s)} },
	} {
		srv := server.NewRPCServer()
		newOptions(order(srv)...)
		if so := srv.Options(); so.Name != "x" || so.Registry != reg {
			t.Errorf("%s: name %q, registry %v", name, so.Name, so.Registry)
		}
	}
}

func TestClientKeepsEarlierOptions(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	c := client.NewClient()
	o := newOptions(Registry(reg), WrapCall(passCall), WrapClient(wrapClient), Client(c))
	w, ok := o.Client.(*wrappedClient)
	if !ok || w.Client != c {
		t.Fatalf("client %T: WrapClient not applied to the new client", o.Client)
	}
	co := c.Options()
	if co.Registry != reg {
		t.Fatalf("client registry %v: not the service's", co.Registry)
	}
	if len(co.CallOptions.CallWrappers) != 1 {
		t.Fatalf("call wrappers: %d, want 1", len(co.CallOptions.CallWrappers))
	}
}

func TestBrokerKeepsRegistry(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	b := broker.NewMemoryBroker()
	o := newOptions(Registry(reg), Broker(b))
	if o.Broker.Options().Registry != reg {
		t.Fatal("broker set after Registry has another registry")
	}
	if o.Server.Options().Broker != b || o.Client.Options().Broker != b {
		t.Fatal("server or client kept the default broker")
	}
}

// A handler added with Service.Handle survives a later server swap.
func TestServiceHandleSurvivesServerSwap(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	svc := New(Name("users"), Registry(reg), Address("127.0.0.1:0")).(*serviceImpl)
	if err := svc.Handle(new(Greeter)); err != nil {
		t.Fatal(err)
	}
	srv := server.NewRPCServer()
	Server(srv)(&svc.opts)
	if eps := endpoints(t, srv, reg, "users"); len(eps) != 1 || eps[0] != "Greeter.Hello" {
		t.Fatalf("endpoints %v after the swap", eps)
	}
}
