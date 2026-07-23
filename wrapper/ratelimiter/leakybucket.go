package ratelimiter

import (
	"context"

	uberratelimit "go.uber.org/ratelimit"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/server"
)

// Leaky bucket (uber-go/ratelimit): requests are smoothed to a fixed
// interval (1/rps) instead of admitting bursts — callers BLOCK until their
// slot. Use the token-bucket wrappers when you want bursts or fail-fast
// rejection; use these when downstream needs an even request rate.
// Pass uberratelimit.WithSlack(n) via opts to tolerate small bursts.

// NewLeakyBucketHandlerWrapper smooths inbound requests to rps per second.
func NewLeakyBucketHandlerWrapper(rps int, opts ...uberratelimit.Option) server.HandlerWrapper {
	l := uberratelimit.New(rps, opts...)
	return func(next server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			l.Take() // blocks until this request's slot
			return next(ctx, req, rsp)
		}
	}
}

// NewLeakyBucketClientWrapper smooths outbound calls to rps per second.
func NewLeakyBucketClientWrapper(rps int, opts ...uberratelimit.Option) client.Wrapper {
	l := uberratelimit.New(rps, opts...)
	return func(c client.Client) client.Client {
		return &leakyClientWrapper{c, l}
	}
}

type leakyClientWrapper struct {
	client.Client
	l uberratelimit.Limiter
}

func (c *leakyClientWrapper) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	c.l.Take()
	return c.Client.Call(ctx, req, rsp, opts...)
}
