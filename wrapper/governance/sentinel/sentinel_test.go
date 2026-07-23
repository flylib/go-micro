package sentinel

import (
	"context"
	"testing"

	"github.com/alibaba/sentinel-golang/core/flow"

	"github.com/flylib/go-micro/codec"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/server"
)

type stubReq struct{}

func (stubReq) Service() string           { return "svc" }
func (stubReq) Method() string            { return "M" }
func (stubReq) Endpoint() string          { return "ep" }
func (stubReq) ContentType() string       { return "application/json" }
func (stubReq) Header() map[string]string { return nil }
func (stubReq) Body() interface{}         { return nil }
func (stubReq) Read() ([]byte, error)     { return nil, nil }
func (stubReq) Codec() codec.Reader       { return nil }
func (stubReq) Stream() bool              { return false }

func TestHandlerWrapperFlowControl(t *testing.T) {
	h := NewHandlerWrapper()(func(ctx context.Context, req server.Request, rsp interface{}) error {
		return nil
	})

	if _, err := flow.LoadRules([]*flow.Rule{{
		Resource:               "svc.ep",
		Threshold:              2,
		TokenCalculateStrategy: flow.Direct,
		ControlBehavior:        flow.Reject,
		StatIntervalInMs:       1000,
	}}); err != nil {
		t.Fatalf("load rules: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := h(ctx, stubReq{}, nil); err != nil {
			t.Fatalf("call %d should pass: %v", i, err)
		}
	}
	err := h(ctx, stubReq{}, nil)
	if err == nil {
		t.Fatal("third call should be blocked by sentinel")
	}
	if me := merr.FromError(err); me.Code != 429 {
		t.Fatalf("want 429, got %v", err)
	}
}
