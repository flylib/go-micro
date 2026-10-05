package grpc

import (
	"errors"
	"testing"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

// A server without subscribers still connects its broker, so services
// that only publish (gateway/push) can.
func TestStartConnectsBrokerWithoutSubscribers(t *testing.T) {
	b := broker.NewMemoryBroker()
	srv := NewServer(server.Name("pub-only"), server.Registry(registry.NewMemoryRegistry()),
		server.Broker(b), server.Address("127.0.0.1:0"))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := b.Publish("topic", &broker.Message{Body: []byte("{}")}); err != nil {
		t.Fatalf("publish after start: %v", err)
	}
}

type downBroker struct{ broker.Broker }

func (downBroker) Connect() error { return errors.New("broker down") }

// Without subscribers an unreachable broker does not stop the server.
func TestStartWithUnreachableBrokerAndNoSubscribers(t *testing.T) {
	srv := NewServer(server.Name("pub-only"), server.Registry(registry.NewMemoryRegistry()),
		server.Broker(downBroker{broker.NewMemoryBroker()}), server.Address("127.0.0.1:0"))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = srv.Stop()
}
