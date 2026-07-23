// Package ratelimiter provides token-bucket rate limiting wrappers built
// on golang.org/x/time/rate.
package ratelimiter

import (
	"context"

	"golang.org/x/time/rate"

	"github.com/flylib/go-micro/client"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/server"
)

// NewHandlerWrapper limits inbound requests to r per second with burst b.
// When wait is true excess requests block until a token is available;
// otherwise they fail fast with a 429 error.
func NewHandlerWrapper(r rate.Limit, b int, wait bool) server.HandlerWrapper {
	l := rate.NewLimiter(r, b)
	return func(next server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			if err := limit(ctx, l, wait, req.Service()); err != nil {
				return err
			}
			return next(ctx, req, rsp)
		}
	}
}

// NewClientWrapper limits outbound calls to r per second with burst b.
func NewClientWrapper(r rate.Limit, b int, wait bool) client.Wrapper {
	l := rate.NewLimiter(r, b)
	return func(c client.Client) client.Client {
		return &clientWrapper{c, l, wait}
	}
}

type clientWrapper struct {
	client.Client
	l    *rate.Limiter
	wait bool
}

func (c *clientWrapper) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	if err := limit(ctx, c.l, c.wait, req.Service()); err != nil {
		return err
	}
	return c.Client.Call(ctx, req, rsp, opts...)
}

func limit(ctx context.Context, l *rate.Limiter, wait bool, id string) error {
	if wait {
		return l.Wait(ctx)
	}
	if !l.Allow() {
		return merr.New(id, "rate limit exceeded", 429)
	}
	return nil
}
