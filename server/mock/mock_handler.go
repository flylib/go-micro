package mock

import (
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

type MockHandler struct {
	Opts server.HandlerOptions
	Hdlr interface{}
	Id   string
}

func (m *MockHandler) Name() string {
	return m.Id
}

func (m *MockHandler) Handler() interface{} {
	return m.Hdlr
}

func (m *MockHandler) Endpoints() []*registry.Endpoint {
	return []*registry.Endpoint{}
}

func (m *MockHandler) Options() server.HandlerOptions {
	return m.Opts
}
