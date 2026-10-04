package logging

import (
	"context"
	"time"

	"github.com/flylib/go-micro/logger"
)

// Option configures a logging wrapper.
type Option func(*options)

// Filter reports whether an operation should go unlogged, e.g. health
// checks. For subscribers service is empty and endpoint is the topic.
type Filter func(ctx context.Context, service, endpoint string) bool

// TraceExtractor returns the trace and span ids for ctx; empty strings mean
// no trace is active.
type TraceExtractor func(ctx context.Context) (traceID, spanID string)

type options struct {
	logger       logger.Logger
	filter       Filter
	extract      TraceExtractor
	slow         time.Duration
	injectLogger bool
}

func newOptions(opts []Option) *options {
	o := &options{
		extract:      defaultExtractor,
		injectLogger: true,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// base picks the logger lines are derived from: the configured one, else
// one already in ctx, else the framework default (resolved per call so a
// later logger.Init still applies).
func (o *options) base(ctx context.Context) logger.Logger {
	if o.logger != nil {
		return o.logger
	}
	if l, ok := logger.FromContext(ctx); ok {
		return l
	}
	return logger.DefaultLogger
}

func (o *options) skip(ctx context.Context, service, endpoint string) bool {
	return o.filter != nil && o.filter(ctx, service, endpoint)
}

// WithLogger sets the logger access lines are written to. Default: the
// logger in the request context, else logger.DefaultLogger.
func WithLogger(l logger.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithFilter skips logging (and logger injection) for matching operations.
func WithFilter(f Filter) Option {
	return func(o *options) { o.filter = f }
}

// WithTraceExtractor replaces how trace_id / span_id are found, for
// tracers that do not propagate through metadata.
func WithTraceExtractor(e TraceExtractor) Option {
	return func(o *options) {
		if e != nil {
			o.extract = e
		}
	}
}

// WithSlowThreshold logs successful operations at least this slow as
// warnings instead of info. Zero (the default) disables it.
func WithSlowThreshold(d time.Duration) Option {
	return func(o *options) { o.slow = d }
}

// WithContextLogger controls whether server-side wrappers put a logger
// carrying trace_id / span_id into the handler context (default true).
// Each request then pays for one derived logger.
func WithContextLogger(enabled bool) Option {
	return func(o *options) { o.injectLogger = enabled }
}
