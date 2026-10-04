// Package logging writes one structured log line per RPC and ties it to the
// distributed trace: every line carries trace_id / span_id, and the handler
// context gets a logger that already has them, so business logs written via
// logger.Extract(ctx) correlate with the access log and with the trace
// backend without any per-handler code.
//
//	micro.New("orders",
//	    micro.WrapHandler(
//	        opentelemetry.NewHandlerWrapper(), // outer: opens the span
//	        logging.NewHandlerWrapper(),       // inner: logs under that span
//	    ),
//	    micro.WrapClient(logging.NewClientWrapper()),
//	)
//
// Trace identity is read from the W3C traceparent that the opentelemetry
// wrapper propagates in metadata, falling back to the framework's own
// Micro-Trace-ID header. Mount logging inside the tracing wrapper to log the
// server span's own span_id; mount it outside to log the caller's.
package logging

import (
	"context"
	"strings"
	"time"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/debug/trace"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/server"
)

const (
	traceparentKey = "traceparent"

	// field names shared by every log line
	fieldTraceID  = "trace_id"
	fieldSpanID   = "span_id"
	fieldService  = "service"
	fieldEndpoint = "endpoint"
	fieldTopic    = "topic"
	fieldDuration = "duration"
	fieldCode     = "code"
	fieldError    = "error"
)

// NewHandlerWrapper logs every inbound RPC after the handler returns and
// injects a trace-aware logger into the handler context.
func NewHandlerWrapper(opts ...Option) server.HandlerWrapper {
	o := newOptions(opts)
	return func(next server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			if o.skip(ctx, req.Service(), req.Endpoint()) {
				return next(ctx, req, rsp)
			}
			base := o.base(ctx)
			fields := o.traceFields(ctx)
			fields[fieldService] = req.Service()
			fields[fieldEndpoint] = req.Endpoint()
			if o.injectLogger {
				ctx = logger.NewContext(ctx, base.Fields(fields))
			}

			start := time.Now()
			err := next(ctx, req, rsp)
			o.record(base, "rpc server", req.Endpoint(), fields, time.Since(start), err)
			return err
		}
	}
}

// NewSubscriberWrapper logs every event delivery after the subscriber
// returns and injects a trace-aware logger into the subscriber context.
func NewSubscriberWrapper(opts ...Option) server.SubscriberWrapper {
	o := newOptions(opts)
	return func(next server.SubscriberFunc) server.SubscriberFunc {
		return func(ctx context.Context, msg server.Message) error {
			if o.skip(ctx, "", msg.Topic()) {
				return next(ctx, msg)
			}
			base := o.base(ctx)
			fields := o.traceFields(ctx)
			fields[fieldTopic] = msg.Topic()
			if o.injectLogger {
				ctx = logger.NewContext(ctx, base.Fields(fields))
			}

			start := time.Now()
			err := next(ctx, msg)
			o.record(base, "event", msg.Topic(), fields, time.Since(start), err)
			return err
		}
	}
}

// NewClientWrapper logs every outbound Call, including calls that fail
// before reaching the network. Streams and publishes pass through.
func NewClientWrapper(opts ...Option) client.Wrapper {
	o := newOptions(opts)
	return func(c client.Client) client.Client {
		return &clientWrapper{Client: c, o: o}
	}
}

type clientWrapper struct {
	client.Client
	o *options
}

func (c *clientWrapper) Call(ctx context.Context, req client.Request, rsp interface{}, opts ...client.CallOption) error {
	if c.o.skip(ctx, req.Service(), req.Endpoint()) {
		return c.Client.Call(ctx, req, rsp, opts...)
	}
	fields := c.o.traceFields(ctx)
	fields[fieldService] = req.Service()
	fields[fieldEndpoint] = req.Endpoint()

	start := time.Now()
	err := c.Client.Call(ctx, req, rsp, opts...)
	c.o.record(c.o.base(ctx), "rpc client", req.Service()+"."+req.Endpoint(), fields, time.Since(start), err)
	return err
}

// record emits the single access-log line for one finished operation.
func (o *options) record(base logger.Logger, kind, name string, fields map[string]interface{}, took time.Duration, err error) {
	code := int32(200)
	if err != nil {
		code = errorCode(err)
	}
	level := o.level(code, took)
	if !base.Options().Level.Enabled(level) {
		return
	}

	out := make(map[string]interface{}, len(fields)+3)
	for k, v := range fields {
		out[k] = v
	}
	out[fieldDuration] = took.String()
	out[fieldCode] = code
	if err != nil {
		out[fieldError] = errorDetail(err)
	}
	base.Fields(out).Log(level, kind+" "+name)
}

// level maps an outcome to a log level: server faults are errors, client
// faults and slow-but-successful requests are warnings.
func (o *options) level(code int32, took time.Duration) logger.Level {
	switch {
	case code >= 500:
		return logger.ErrorLevel
	case code >= 400:
		return logger.WarnLevel
	case o.slow > 0 && took >= o.slow:
		return logger.WarnLevel
	default:
		return logger.InfoLevel
	}
}

// errorCode extracts the HTTP-style status from a micro error. Errors that
// are not micro errors carry no status and count as internal failures.
func errorCode(err error) int32 {
	if e := merr.FromError(err); e != nil && e.Code != 0 {
		return e.Code
	}
	return 500
}

func errorDetail(err error) string {
	if e := merr.FromError(err); e != nil && e.Detail != "" {
		return e.Detail
	}
	return err.Error()
}

// traceFields returns a fresh field map holding the ids found in ctx.
func (o *options) traceFields(ctx context.Context) map[string]interface{} {
	fields := make(map[string]interface{}, 6)
	traceID, spanID := o.extract(ctx)
	if traceID != "" {
		fields[fieldTraceID] = traceID
	}
	if spanID != "" {
		fields[fieldSpanID] = spanID
	}
	return fields
}

// defaultExtractor reads W3C traceparent first, then the Micro-Trace-ID
// header used by the framework's built-in tracer.
func defaultExtractor(ctx context.Context) (traceID, spanID string) {
	if v, ok := metadata.Get(ctx, traceparentKey); ok {
		if t, s, ok := parseTraceparent(v); ok {
			return t, s
		}
	}
	traceID, spanID, _ = trace.FromContext(ctx)
	return traceID, spanID
}

// parseTraceparent splits "00-<trace-id>-<span-id>-<flags>" and rejects
// malformed or all-zero ids, which the spec defines as invalid.
func parseTraceparent(v string) (traceID, spanID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	const (
		traceIDLen = 32
		spanIDLen  = 16
		minParts   = 4
	)
	if len(parts) < minParts || len(parts[1]) != traceIDLen || len(parts[2]) != spanIDLen {
		return "", "", false
	}
	if !isHex(parts[1]) || !isHex(parts[2]) || isZero(parts[1]) || isZero(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func isZero(s string) bool {
	return strings.Trim(s, "0") == ""
}
