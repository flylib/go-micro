package mock

import (
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

type MockSubscriber struct {
	Opts server.SubscriberOptions
	Sub  interface{}
	Id   string
}

func (m *MockSubscriber) Topic() string {
	return m.Id
}

func (m *MockSubscriber) Subscriber() interface{} {
	return m.Sub
}

func (m *MockSubscriber) Endpoints() []*registry.Endpoint {
	return []*registry.Endpoint{}
}

func (m *MockSubscriber) Options() server.SubscriberOptions {
	return m.Opts
}
