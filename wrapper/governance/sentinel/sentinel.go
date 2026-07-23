// Package sentinel provides alibaba/sentinel-golang flow-control and
// circuit-breaking wrappers. Load flow/breaker rules with the sentinel
// APIs (flow.LoadRules / circuitbreaker.LoadRules); the wrappers guard
// each call as resource "<service>.<endpoint>".
package sentinel

import (
	"context"
	"sync"

	sentinel "github.com/alibaba/sentinel-golang/api"
	"github.com/alibaba/sentinel-golang/core/base"

	"github.com/flylib/go-micro/client"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/server"
)

var initOnce sync.Once

func ensureInit() {
	initOnce.Do(func() {
		_ = sentinel.InitDefault()
	})
}

// NewClientWrapper guards outbound calls (traffic type Outbound).
func NewClientWrapper() client.Wrapper {
	ensureInit()
	return func(c client.Client) client.Client {
		return &clientWrapper{c}
	}
}

type clientWrapper struct {
	client.Client
}

func (c *clientWrapper) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	res := req.Service() + "." + req.Endpoint()
	e, blocked := sentinel.Entry(res, sentinel.WithTrafficType(base.Outbound))
	if blocked != nil {
		return merr.New(req.Service(), "blocked by sentinel: "+blocked.BlockType().String(), 429)
	}
	defer e.Exit()
	err := c.Client.Call(ctx, req, rsp, opts...)
	if err != nil {
		sentinel.TraceError(e, err)
	}
	return err
}

// NewHandlerWrapper guards inbound requests (traffic type Inbound).
func NewHandlerWrapper() server.HandlerWrapper {
	ensureInit()
	return func(next server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			res := req.Service() + "." + req.Endpoint()
			e, blocked := sentinel.Entry(res, sentinel.WithTrafficType(base.Inbound))
			if blocked != nil {
				return merr.New(req.Service(), "blocked by sentinel: "+blocked.BlockType().String(), 429)
			}
			defer e.Exit()
			err := next(ctx, req, rsp)
			if err != nil {
				sentinel.TraceError(e, err)
			}
			return err
		}
	}
}
