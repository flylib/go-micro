// Package slog provides a log/slog backed logger for go-micro.
package slog

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/flylib/go-micro/logger"
)

type slogKey struct{}
type handlerKey struct{}

// WithLogger injects a pre-built *slog.Logger. Out/Level options are then
// controlled by that logger's handler.
func WithLogger(l *slog.Logger) logger.Option {
	return setCtx(slogKey{}, l)
}

// WithHandler injects a custom slog.Handler (e.g. JSONHandler or a
// third-party handler).
func WithHandler(h slog.Handler) logger.Option {
	return setCtx(handlerKey{}, h)
}

func setCtx(k, v interface{}) logger.Option {
	return func(o *logger.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, k, v)
	}
}

type slogLogger struct {
	slog *slog.Logger
	opts logger.Options
}

// NewLogger returns a slog-backed logger.
func NewLogger(opts ...logger.Option) logger.Logger {
	l := &slogLogger{opts: logger.Options{
		Level:  logger.InfoLevel,
		Fields: make(map[string]interface{}),
		Out:    os.Stderr,
	}}
	_ = l.Init(opts...)
	return l
}

func (l *slogLogger) Init(opts ...logger.Option) error {
	for _, o := range opts {
		o(&l.opts)
	}

	if l.opts.Context != nil {
		if v, ok := l.opts.Context.Value(slogKey{}).(*slog.Logger); ok && v != nil {
			l.slog = v
		}
		if v, ok := l.opts.Context.Value(handlerKey{}).(slog.Handler); ok && v != nil {
			l.slog = slog.New(v)
		}
	}
	if l.slog == nil {
		out := l.opts.Out
		if out == nil {
			out = os.Stderr
		}
		l.slog = slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
			Level: toSlogLevel(l.opts.Level),
		}))
	}
	if len(l.opts.Fields) > 0 {
		l.slog = l.slog.With(fieldsToArgs(l.opts.Fields)...)
	}
	return nil
}

func (l *slogLogger) Options() logger.Options {
	return l.opts
}

func (l *slogLogger) Fields(fields map[string]interface{}) logger.Logger {
	nl := &slogLogger{opts: l.opts, slog: l.slog.With(fieldsToArgs(fields)...)}
	return nl
}

func (l *slogLogger) Log(level logger.Level, v ...interface{}) {
	if !l.opts.Level.Enabled(level) {
		return
	}
	l.slog.Log(context.Background(), toSlogLevel(level), fmt.Sprint(v...))
	if level == logger.FatalLevel {
		os.Exit(1)
	}
}

func (l *slogLogger) Logf(level logger.Level, format string, v ...interface{}) {
	if !l.opts.Level.Enabled(level) {
		return
	}
	l.slog.Log(context.Background(), toSlogLevel(level), fmt.Sprintf(format, v...))
	if level == logger.FatalLevel {
		os.Exit(1)
	}
}

func (l *slogLogger) String() string {
	return "slog"
}

func toSlogLevel(level logger.Level) slog.Level {
	switch level {
	case logger.TraceLevel:
		return slog.LevelDebug - 4
	case logger.DebugLevel:
		return slog.LevelDebug
	case logger.InfoLevel:
		return slog.LevelInfo
	case logger.WarnLevel:
		return slog.LevelWarn
	case logger.ErrorLevel, logger.FatalLevel:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func fieldsToArgs(fields map[string]interface{}) []any {
	args := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return args
}
