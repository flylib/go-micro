package hystrix

import (
	"context"
	"errors"
	"testing"
	"time"

	hx "github.com/afex/hystrix-go/hystrix"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/codec"
	merr "github.com/flylib/go-micro/errors"
)

type stubReq struct{}

func (stubReq) Service() string     { return "svc" }
func (stubReq) Method() string      { return "M" }
func (stubReq) Endpoint() string    { return "ep" }
func (stubReq) ContentType() string { return "application/json" }
func (stubReq) Body() interface{}   { return nil }
func (stubReq) Codec() codec.Writer { return nil }
func (stubReq) Stream() bool        { return false }

// failingClient always errors; only Call is exercised.
type failingClient struct {
	client.Client
}

func (failingClient) Call(context.Context, client.Request, interface{}, ...client.CallOption) error {
	return errors.New("downstream broken")
}

func TestClientWrapperOpensCircuit(t *testing.T) {
	Configure("svc.ep", hx.CommandConfig{
		RequestVolumeThreshold: 4,
		ErrorPercentThreshold:  50,
		SleepWindow:            60000,
		Timeout:                1000,
	})

	c := NewClientWrapper()(failingClient{})
	ctx := context.Background()

	// hystrix aggregates health metrics asynchronously — pace the failing
	// calls so the rolling window registers them, then expect a 503 fast-fail
	sawOpen := false
	for i := 0; i < 40; i++ {
		err := c.Call(ctx, stubReq{}, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if me := merr.FromError(err); me.Code == 503 {
			sawOpen = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !sawOpen {
		t.Fatal("circuit never opened / fast-failed with 503")
	}
}
