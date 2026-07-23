// Package zap provides a go.uber.org/zap backed logger for go-micro.
package zap

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/flylib/go-micro/logger"
)

type zapKey struct{}
type configKey struct{}

// WithLogger injects a pre-built *zap.Logger.
func WithLogger(l *zap.Logger) logger.Option {
	return setCtx(zapKey{}, l)
}

// WithConfig builds the logger from a zap.Config.
func WithConfig(c zap.Config) logger.Option {
	return setCtx(configKey{}, c)
}

func setCtx(k, v interface{}) logger.Option {
	return func(o *logger.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, k, v)
	}
}

type zapLogger struct {
	zap  *zap.SugaredLogger
	opts logger.Options
}

// NewLogger returns a zap-backed logger.
func NewLogger(opts ...logger.Option) logger.Logger {
	l := &zapLogger{opts: logger.Options{
		Level:  logger.InfoLevel,
		Fields: make(map[string]interface{}),
	}}
	_ = l.Init(opts...)
	return l
}

func (l *zapLogger) Init(opts ...logger.Option) error {
	for _, o := range opts {
		o(&l.opts)
	}

	var zl *zap.Logger
	if l.opts.Context != nil {
		if v, ok := l.opts.Context.Value(zapKey{}).(*zap.Logger); ok && v != nil {
			zl = v
		}
		if v, ok := l.opts.Context.Value(configKey{}).(zap.Config); ok {
			built, err := v.Build()
			if err != nil {
				return err
			}
			zl = built
		}
	}
	if zl == nil {
		// production-style JSON encoder writing to Out (default stderr),
		// leveled by the framework option
		encCfg := zap.NewProductionEncoderConfig()
		out := l.opts.Out
		if out == nil {
			out = os.Stderr
		}
		core := zapcore.NewCore(
			zapcore.NewJSONEncoder(encCfg),
			zapcore.AddSync(out),
			toZapLevel(l.opts.Level),
		)
		zl = zap.New(core)
	}

	if len(l.opts.Fields) > 0 {
		zl = zl.With(fieldsToZap(l.opts.Fields)...)
	}
	l.zap = zl.Sugar()
	return nil
}

func (l *zapLogger) Options() logger.Options {
	return l.opts
}

func (l *zapLogger) Fields(fields map[string]interface{}) logger.Logger {
	return &zapLogger{opts: l.opts, zap: l.zap.Desugar().With(fieldsToZap(fields)...).Sugar()}
}

func (l *zapLogger) Log(level logger.Level, v ...interface{}) {
	if !l.opts.Level.Enabled(level) {
		return
	}
	msg := fmt.Sprint(v...)
	switch level {
	case logger.TraceLevel, logger.DebugLevel:
		l.zap.Debug(msg)
	case logger.InfoLevel:
		l.zap.Info(msg)
	case logger.WarnLevel:
		l.zap.Warn(msg)
	case logger.ErrorLevel:
		l.zap.Error(msg)
	case logger.FatalLevel:
		l.zap.Fatal(msg)
	}
}

func (l *zapLogger) Logf(level logger.Level, format string, v ...interface{}) {
	l.Log(level, fmt.Sprintf(format, v...))
}

func (l *zapLogger) String() string {
	return "zap"
}

func toZapLevel(level logger.Level) zapcore.Level {
	switch level {
	case logger.TraceLevel, logger.DebugLevel:
		return zapcore.DebugLevel
	case logger.InfoLevel:
		return zapcore.InfoLevel
	case logger.WarnLevel:
		return zapcore.WarnLevel
	case logger.ErrorLevel:
		return zapcore.ErrorLevel
	case logger.FatalLevel:
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

func fieldsToZap(fields map[string]interface{}) []zap.Field {
	zf := make([]zap.Field, 0, len(fields))
	for k, v := range fields {
		zf = append(zf, zap.Any(k, v))
	}
	return zf
}
