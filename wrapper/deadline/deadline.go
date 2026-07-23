// Package deadline propagates the remaining request budget across hops:
// the client writes its context's remaining deadline (ms) into request
// metadata, the server shrinks its handler context to that budget. When
// the budget is exhausted anywhere along the chain, the whole downstream
// gives up together instead of doing work the caller already abandoned.
//
//	micro.New("orders",
//	    micro.WrapClient(deadline.NewClientWrapper()),
//	    micro.WrapHandler(deadline.NewHandlerWrapper()),
//	)
package deadline

import (
	"context"
	"strconv"
	"time"

	"github.com/flylib/go-micro/client"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/server"
	"github.com/flylib/go-micro/transport/headers"
)

// NewClientWrapper writes the caller's remaining deadline into outgoing
// metadata. Calls whose budget is already spent fail fast with 408
// without touching the network.
func NewClientWrapper() client.Wrapper {
	return func(c client.Client) client.Client {
		return &clientWrapper{c}
	}
}

type clientWrapper struct {
	client.Client
}

func (c *clientWrapper) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	if d, ok := ctx.Deadline(); ok {
		remaining := time.Until(d)
		if remaining <= 0 {
			return merr.New(req.Service(), "deadline already exceeded", 408)
		}
		ctx = metadata.Set(ctx, headers.Deadline, strconv.FormatInt(remaining.Milliseconds(), 10))
	}
	return c.Client.Call(ctx, req, rsp, opts...)
}

// NewHandlerWrapper shrinks the handler context to the caller's remaining
// budget. Requests arriving with no budget left fail fast with 408
// before the handler runs.
func NewHandlerWrapper() server.HandlerWrapper {
	return func(next server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			v, ok := metadata.Get(ctx, headers.Deadline)
			if !ok {
				return next(ctx, req, rsp)
			}
			ms, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return next(ctx, req, rsp)
			}
			if ms <= 0 {
				return merr.New(req.Service(), "deadline already exceeded", 408)
			}
			// WithTimeout only ever shrinks: if the local ctx is already
			// tighter than the propagated budget, the tighter one wins
			ctx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
			defer cancel()
			return next(ctx, req, rsp)
		}
	}
}
