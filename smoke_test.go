package micro_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	micro "github.com/flylib/go-micro"
	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/service"
	"github.com/flylib/go-micro/store"
)

type SmokeReq struct {
	Name string `json:"name"`
}

type SmokeRsp struct {
	Msg string `json:"msg"`
}

type Greeter struct{}

func (g *Greeter) Hello(ctx context.Context, req *SmokeReq, rsp *SmokeRsp) error {
	rsp.Msg = "hello " + req.Name
	return nil
}

// newSmokeService builds a service on in-memory registry/broker so the
// smoke test needs no external infrastructure and no mDNS.
func newSmokeService(t *testing.T, name string) micro.Service {
	t.Helper()
	svc := micro.New(name,
		service.Registry(registry.NewMemoryRegistry()),
		service.Broker(broker.NewMemoryBroker()),
		service.Address("127.0.0.1:0"),
		service.HandleSignal(false),
	)
	return svc
}

// TestSmokeRPC covers the full request path: client → selector → registry
// lookup → transport → server → codec → handler → reply.
func TestSmokeRPC(t *testing.T) {
	svc := newSmokeService(t, "smoke.greeter")
	if err := svc.Handle(new(Greeter)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Stop()

	req := svc.Client().NewRequest("smoke.greeter", "Greeter.Hello",
		&SmokeReq{Name: "world"}, client.WithContentType("application/json"))
	var rsp SmokeRsp
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Client().Call(ctx, req, &rsp); err != nil {
		t.Fatalf("call: %v", err)
	}
	if rsp.Msg != "hello world" {
		t.Fatalf("unexpected reply: %q", rsp.Msg)
	}
}

// TestSmokeRegistry verifies the service registered itself and deregisters
// cleanly on stop.
func TestSmokeRegistry(t *testing.T) {
	svc := newSmokeService(t, "smoke.reg")
	if err := svc.Handle(new(Greeter)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	reg := svc.Options().Registry
	services, err := reg.GetService("smoke.reg")
	if err != nil || len(services) == 0 || len(services[0].Nodes) == 0 {
		t.Fatalf("expected registered node, got %v err=%v", services, err)
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if services, _ := reg.GetService("smoke.reg"); len(services) != 0 {
		t.Fatalf("expected deregistration on stop, still got %v", services)
	}
}

type SmokeEvent struct {
	ID string `json:"id"`
}

// TestSmokePubSub covers broker publish → server subscriber dispatch.
func TestSmokePubSub(t *testing.T) {
	svc := newSmokeService(t, "smoke.pubsub")

	var got atomic.Int32
	err := micro.RegisterSubscriber("smoke.topic", svc.Server(),
		func(ctx context.Context, ev *SmokeEvent) error {
			if ev.ID == "e1" {
				got.Add(1)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Stop()

	ev := micro.NewEvent("smoke.topic", svc.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ev.Publish(ctx, &SmokeEvent{ID: "e1"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for got.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got.Load() == 0 {
		t.Fatal("subscriber never received the event")
	}
}

// TestSmokeStore covers the KV store abstraction with the memory backend.
func TestSmokeStore(t *testing.T) {
	s := store.NewMemoryStore()
	if err := s.Write(&store.Record{Key: "k1", Value: []byte("v1")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	recs, err := s.Read("k1")
	if err != nil || len(recs) != 1 || string(recs[0].Value) != "v1" {
		t.Fatalf("read: recs=%v err=%v", recs, err)
	}
	if err := s.Delete("k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if recs, _ := s.Read("k1"); len(recs) != 0 {
		t.Fatal("expected key deleted")
	}
}

// TestSmokeMultiService runs two services in one process on a shared
// registry and calls across them — the Group scenario without the
// blocking Run loop.
func TestSmokeMultiService(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	mk := func(name string) micro.Service {
		return micro.New(name,
			service.Registry(reg),
			service.Broker(broker.NewMemoryBroker()),
			service.Address("127.0.0.1:0"),
			service.HandleSignal(false),
		)
	}
	a, b := mk("smoke.a"), mk("smoke.b")
	if err := a.Handle(new(Greeter)); err != nil {
		t.Fatal(err)
	}
	if err := b.Handle(new(Greeter)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []micro.Service{a, b} {
		if err := s.Start(); err != nil {
			t.Fatalf("start %s: %v", s.Name(), err)
		}
		defer s.Stop()
	}

	// b calls a through the shared registry
	req := b.Client().NewRequest("smoke.a", "Greeter.Hello",
		&SmokeReq{Name: "b"}, client.WithContentType("application/json"))
	var rsp SmokeRsp
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Client().Call(ctx, req, &rsp); err != nil {
		t.Fatalf("cross-service call: %v", err)
	}
	if rsp.Msg != "hello b" {
		t.Fatalf("unexpected reply: %q", rsp.Msg)
	}
}
