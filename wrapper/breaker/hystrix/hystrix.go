// Package hystrix provides a hystrix-go circuit-breaker client wrapper.
// One command per service.endpoint; tune via hystrix.ConfigureCommand.
package hystrix

import (
	"context"

	"github.com/afex/hystrix-go/hystrix"

	"github.com/flylib/go-micro/client"
	merr "github.com/flylib/go-micro/errors"
)

type clientWrapper struct {
	client.Client
}

// NewClientWrapper returns a client.Wrapper that runs every Call inside a
// hystrix command named "<service>.<endpoint>". When the circuit is open
// calls fail fast with a 503 error.
func NewClientWrapper() client.Wrapper {
	return func(c client.Client) client.Client {
		return &clientWrapper{c}
	}
}

func (c *clientWrapper) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	name := req.Service() + "." + req.Endpoint()
	err := hystrix.Do(name, func() error {
		return c.Client.Call(ctx, req, rsp, opts...)
	}, nil)
	// map breaker-internal errors to a transport-level 503; note hystrix
	// wraps errors returned by fallbacks, so mapping happens out here
	if err == hystrix.ErrCircuitOpen || err == hystrix.ErrMaxConcurrency {
		return merr.New(req.Service(), err.Error(), 503)
	}
	return err
}

// Configure sets the hystrix command config for a service.endpoint.
func Configure(name string, conf hystrix.CommandConfig) {
	hystrix.ConfigureCommand(name, conf)
}
