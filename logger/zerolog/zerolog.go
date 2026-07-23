// Package zerolog provides a github.com/rs/zerolog backed logger for go-micro.
package zerolog

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"

	"github.com/flylib/go-micro/logger"
)

type zerologKey struct{}

// WithLogger injects a pre-built zerolog.Logger.
func WithLogger(l zerolog.Logger) logger.Option {
	return func(o *logger.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, zerologKey{}, l)
	}
}

type zeroLogger struct {
	zl   zerolog.Logger
	opts logger.Options
}

// NewLogger returns a zerolog-backed logger.
func NewLogger(opts ...logger.Option) logger.Logger {
	l := &zeroLogger{opts: logger.Options{
		Level:  logger.InfoLevel,
		Fields: make(map[string]interface{}),
	}}
	_ = l.Init(opts...)
	return l
}

func (l *zeroLogger) Init(opts ...logger.Option) error {
	for _, o := range opts {
		o(&l.opts)
	}

	injected := false
	if l.opts.Context != nil {
		if v, ok := l.opts.Context.Value(zerologKey{}).(zerolog.Logger); ok {
			l.zl = v
			injected = true
		}
	}
	if !injected {
		out := l.opts.Out
		if out == nil {
			out = os.Stderr
		}
		l.zl = zerolog.New(out).Level(toZerologLevel(l.opts.Level)).With().Timestamp().Logger()
	}
	if len(l.opts.Fields) > 0 {
		l.zl = l.zl.With().Fields(l.opts.Fields).Logger()
	}
	return nil
}

func (l *zeroLogger) Options() logger.Options {
	return l.opts
}

func (l *zeroLogger) Fields(fields map[string]interface{}) logger.Logger {
	return &zeroLogger{opts: l.opts, zl: l.zl.With().Fields(fields).Logger()}
}

func (l *zeroLogger) Log(level logger.Level, v ...interface{}) {
	if !l.opts.Level.Enabled(level) {
		return
	}
	msg := fmt.Sprint(v...)
	switch level {
	case logger.TraceLevel:
		l.zl.Trace().Msg(msg)
	case logger.DebugLevel:
		l.zl.Debug().Msg(msg)
	case logger.InfoLevel:
		l.zl.Info().Msg(msg)
	case logger.WarnLevel:
		l.zl.Warn().Msg(msg)
	case logger.ErrorLevel:
		l.zl.Error().Msg(msg)
	case logger.FatalLevel:
		l.zl.Fatal().Msg(msg) // zerolog exits
	}
}

func (l *zeroLogger) Logf(level logger.Level, format string, v ...interface{}) {
	l.Log(level, fmt.Sprintf(format, v...))
}

func (l *zeroLogger) String() string {
	return "zerolog"
}

func toZerologLevel(level logger.Level) zerolog.Level {
	switch level {
	case logger.TraceLevel:
		return zerolog.TraceLevel
	case logger.DebugLevel:
		return zerolog.DebugLevel
	case logger.InfoLevel:
		return zerolog.InfoLevel
	case logger.WarnLevel:
		return zerolog.WarnLevel
	case logger.ErrorLevel:
		return zerolog.ErrorLevel
	case logger.FatalLevel:
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}
