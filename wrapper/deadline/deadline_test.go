package deadline

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/codec"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/server"
	"github.com/flylib/go-micro/transport/headers"
)

type stubCliReq struct{}

func (stubCliReq) Service() string     { return "svc" }
func (stubCliReq) Method() string      { return "M" }
func (stubCliReq) Endpoint() string    { return "ep" }
func (stubCliReq) ContentType() string { return "application/json" }
func (stubCliReq) Body() interface{}   { return nil }
func (stubCliReq) Codec() codec.Writer { return nil }
func (stubCliReq) Stream() bool        { return false }

type stubSrvReq struct{}

func (stubSrvReq) Service() string           { return "svc" }
func (stubSrvReq) Method() string            { return "M" }
func (stubSrvReq) Endpoint() string          { return "ep" }
func (stubSrvReq) ContentType() string       { return "application/json" }
func (stubSrvReq) Header() map[string]string { return nil }
func (stubSrvReq) Body() interface{}         { return nil }
func (stubSrvReq) Read() ([]byte, error)     { return nil, nil }
func (stubSrvReq) Codec() codec.Reader       { return nil }
func (stubSrvReq) Stream() bool              { return false }

// capturingClient records the ctx it was called with.
type capturingClient struct {
	client.Client
	ctx context.Context
}

func (c *capturingClient) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	c.ctx = ctx
	return nil
}

func TestClientWritesRemainingBudget(t *testing.T) {
	inner := &capturingClient{}
	c := NewClientWrapper()(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, stubCliReq{}, nil); err != nil {
		t.Fatal(err)
	}
	v, ok := metadata.Get(inner.ctx, headers.Deadline)
	if !ok {
		t.Fatal("deadline metadata not set")
	}
	ms, _ := strconv.ParseInt(v, 10, 64)
	if ms <= 0 || ms > 300 {
		t.Fatalf("remaining budget out of range: %dms", ms)
	}
}

func TestClientNoDeadlineNoHeader(t *testing.T) {
	inner := &capturingClient{}
	c := NewClientWrapper()(inner)
	if err := c.Call(context.Background(), stubCliReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := metadata.Get(inner.ctx, headers.Deadline); ok {
		t.Fatal("must not set header without a deadline")
	}
}

func TestClientExpiredFailsFast(t *testing.T) {
	inner := &capturingClient{}
	c := NewClientWrapper()(inner)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	err := c.Call(ctx, stubCliReq{}, nil)
	if err == nil || merr.FromError(err).Code != 408 {
		t.Fatalf("want fast 408, got %v", err)
	}
	if inner.ctx != nil {
		t.Fatal("inner client must not be called")
	}
}

func TestServerShrinksContext(t *testing.T) {
	var gotDeadline time.Time
	var had bool
	h := NewHandlerWrapper()(func(ctx context.Context, req server.Request, rsp interface{}) error {
		gotDeadline, had = ctx.Deadline()
		return nil
	})

	ctx := metadata.Set(context.Background(), headers.Deadline, "50")
	if err := h(ctx, stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if !had {
		t.Fatal("handler ctx should carry a deadline")
	}
	if remaining := time.Until(gotDeadline); remaining > 50*time.Millisecond || remaining <= 0 {
		t.Fatalf("ctx not shrunk to budget: %v", remaining)
	}
}

func TestServerExhaustedFailsFast(t *testing.T) {
	called := false
	h := NewHandlerWrapper()(func(ctx context.Context, req server.Request, rsp interface{}) error {
		called = true
		return nil
	})
	ctx := metadata.Set(context.Background(), headers.Deadline, "0")
	err := h(ctx, stubSrvReq{}, nil)
	if err == nil || merr.FromError(err).Code != 408 {
		t.Fatalf("want 408, got %v", err)
	}
	if called {
		t.Fatal("handler must not run with exhausted budget")
	}
}

func TestServerNoHeaderPassThrough(t *testing.T) {
	h := NewHandlerWrapper()(func(ctx context.Context, req server.Request, rsp interface{}) error {
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("no deadline expected")
		}
		return nil
	})
	if err := h(context.Background(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
}
