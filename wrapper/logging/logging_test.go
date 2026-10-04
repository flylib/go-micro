package logging

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/codec"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/server"
	"github.com/flylib/go-micro/transport/headers"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
	testParent  = "00-" + testTraceID + "-" + testSpanID + "-01"
)

type stubCliReq struct{}

func (stubCliReq) Service() string     { return "svc" }
func (stubCliReq) Method() string      { return "M" }
func (stubCliReq) Endpoint() string    { return "Svc.Do" }
func (stubCliReq) ContentType() string { return "application/json" }
func (stubCliReq) Body() interface{}   { return nil }
func (stubCliReq) Codec() codec.Writer { return nil }
func (stubCliReq) Stream() bool        { return false }

type stubSrvReq struct{}

func (stubSrvReq) Service() string           { return "svc" }
func (stubSrvReq) Method() string            { return "M" }
func (stubSrvReq) Endpoint() string          { return "Svc.Do" }
func (stubSrvReq) ContentType() string       { return "application/json" }
func (stubSrvReq) Header() map[string]string { return nil }
func (stubSrvReq) Body() interface{}         { return nil }
func (stubSrvReq) Read() ([]byte, error)     { return nil, nil }
func (stubSrvReq) Codec() codec.Reader       { return nil }
func (stubSrvReq) Stream() bool              { return false }

type stubMsg struct{ topic string }

func (m stubMsg) Topic() string           { return m.topic }
func (stubMsg) Payload() interface{}      { return nil }
func (stubMsg) ContentType() string       { return "application/json" }
func (stubMsg) Header() map[string]string { return nil }
func (stubMsg) Body() []byte              { return nil }
func (stubMsg) Codec() codec.Reader       { return nil }

type stubClient struct {
	client.Client
	err error
}

func (c *stubClient) Call(context.Context, client.Request, interface{}, ...client.CallOption) error {
	return c.err
}

// syncBuffer lets the logger write from the code under test while the test
// reads it afterwards.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestLogger() (logger.Logger, *syncBuffer) {
	out := &syncBuffer{}
	return logger.NewLogger(logger.WithOutput(out), logger.WithLevel(logger.InfoLevel)), out
}

func tracedCtx() context.Context {
	return metadata.Set(context.Background(), "Traceparent", testParent)
}

func TestHandlerLogsTraceAndOutcome(t *testing.T) {
	l, out := newTestLogger()
	h := NewHandlerWrapper(WithLogger(l))(func(context.Context, server.Request, interface{}) error { return nil })

	if err := h(tracedCtx(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"rpc server Svc.Do", "trace_id=" + testTraceID, "span_id=" + testSpanID, "service=svc", "code=200", "duration="} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}
}

func TestHandlerInjectsTraceAwareLogger(t *testing.T) {
	l, out := newTestLogger()
	h := NewHandlerWrapper(WithLogger(l))(func(ctx context.Context, _ server.Request, _ interface{}) error {
		logger.Extract(ctx).Info("business event")
		return nil
	})
	if err := h(tracedCtx(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}

	var line string
	for _, s := range strings.Split(out.String(), "\n") {
		if strings.Contains(s, "business event") {
			line = s
		}
	}
	if !strings.Contains(line, "trace_id="+testTraceID) {
		t.Fatalf("handler log lacks trace_id: %q", line)
	}
}

func TestHandlerContextLoggerCanBeDisabled(t *testing.T) {
	l, _ := newTestLogger()
	var injected bool
	h := NewHandlerWrapper(WithLogger(l), WithContextLogger(false))(func(ctx context.Context, _ server.Request, _ interface{}) error {
		_, injected = logger.FromContext(ctx)
		return nil
	})
	if err := h(tracedCtx(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if injected {
		t.Fatal("logger injected despite WithContextLogger(false)")
	}
}

func TestHandlerErrorLevels(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantLevel string
		wantCode  string
	}{
		{"client fault is warn", merr.NotFound("svc", "no such order"), "WARN", "code=404"},
		{"server fault is error", merr.InternalServerError("svc", "boom"), "ERROR", "code=500"},
		{"plain error is 500", errors.New("boom"), "ERROR", "code=500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, out := newTestLogger()
			h := NewHandlerWrapper(WithLogger(l))(func(context.Context, server.Request, interface{}) error { return tt.err })

			if err := h(context.Background(), stubSrvReq{}, nil); err != tt.err {
				t.Fatalf("error not passed through: %v", err)
			}
			got := out.String()
			if !strings.Contains(got, "level="+tt.wantLevel) || !strings.Contains(got, tt.wantCode) || !strings.Contains(got, "error=") {
				t.Errorf("want %s/%s with error field:\n%s", tt.wantLevel, tt.wantCode, got)
			}
		})
	}
}

func TestSlowSuccessIsWarn(t *testing.T) {
	l, out := newTestLogger()
	h := NewHandlerWrapper(WithLogger(l), WithSlowThreshold(time.Millisecond))(func(context.Context, server.Request, interface{}) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	if err := h(context.Background(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "level=WARN") {
		t.Fatalf("slow request not warned:\n%s", out.String())
	}
}

func TestFilterSkipsLoggingAndStillCallsNext(t *testing.T) {
	l, out := newTestLogger()
	called := false
	h := NewHandlerWrapper(
		WithLogger(l),
		WithFilter(func(_ context.Context, _, endpoint string) bool { return endpoint == "Svc.Do" }),
	)(func(context.Context, server.Request, interface{}) error { called = true; return nil })

	if err := h(context.Background(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("next not called")
	}
	if out.String() != "" {
		t.Fatalf("filtered request was logged:\n%s", out.String())
	}
}

func TestFallsBackToMicroTraceID(t *testing.T) {
	l, out := newTestLogger()
	h := NewHandlerWrapper(WithLogger(l))(func(context.Context, server.Request, interface{}) error { return nil })

	ctx := metadata.Set(context.Background(), headers.TraceIDKey, "legacy-trace")
	if err := h(ctx, stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "trace_id=legacy-trace") {
		t.Fatalf("Micro-Trace-ID not used:\n%s", out.String())
	}
}

func TestNoTraceOmitsTraceFields(t *testing.T) {
	l, out := newTestLogger()
	h := NewHandlerWrapper(WithLogger(l))(func(context.Context, server.Request, interface{}) error { return nil })
	if err := h(context.Background(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "trace_id") {
		t.Fatalf("trace_id logged without a trace:\n%s", out.String())
	}
}

func TestCustomTraceExtractor(t *testing.T) {
	l, out := newTestLogger()
	h := NewHandlerWrapper(
		WithLogger(l),
		WithTraceExtractor(func(context.Context) (string, string) { return "custom-t", "custom-s" }),
	)(func(context.Context, server.Request, interface{}) error { return nil })
	if err := h(context.Background(), stubSrvReq{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "trace_id=custom-t") || !strings.Contains(got, "span_id=custom-s") {
		t.Fatalf("custom extractor ignored:\n%s", got)
	}
}

func TestClientLogsCall(t *testing.T) {
	l, out := newTestLogger()
	c := NewClientWrapper(WithLogger(l))(&stubClient{err: merr.Timeout("svc", "slow")})

	if err := c.Call(tracedCtx(), stubCliReq{}, nil); err == nil {
		t.Fatal("error swallowed")
	}
	got := out.String()
	for _, want := range []string{"rpc client svc.Svc.Do", "trace_id=" + testTraceID, "code=408", "level=WARN"} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}
}

func TestSubscriberLogsTopic(t *testing.T) {
	l, out := newTestLogger()
	s := NewSubscriberWrapper(WithLogger(l))(func(context.Context, server.Message) error { return nil })

	if err := s(tracedCtx(), stubMsg{topic: "orders.created"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"event orders.created", "topic=orders.created", "trace_id=" + testTraceID} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}
}

func TestParseTraceparent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", testParent, true},
		{"too few parts", "00-" + testTraceID, false},
		{"short trace id", "00-abc-" + testSpanID + "-01", false},
		{"non hex", "00-" + strings.Repeat("g", 32) + "-" + testSpanID + "-01", false},
		{"zero trace id", "00-" + strings.Repeat("0", 32) + "-" + testSpanID + "-01", false},
		{"zero span id", "00-" + testTraceID + "-" + strings.Repeat("0", 16) + "-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traceID, spanID, ok := parseTraceparent(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (traceID != testTraceID || spanID != testSpanID) {
				t.Fatalf("got %s/%s", traceID, spanID)
			}
		})
	}
}
