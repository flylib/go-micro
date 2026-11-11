package grpc

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/codec"
	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
	"github.com/flylib/go-micro/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

type codecsKey struct{}
type grpcOptions struct{}
type netListener struct{}
type maxMsgSizeKey struct{}
type maxConnKey struct{}
type tlsAuth struct{}
type grpcServerKey struct{}
type gracefulStopTimeoutKey struct{}
type drainDelayKey struct{}

// gRPC Codec to be used to encode/decode requests for a given content type.
func Codec(contentType string, c encoding.Codec) server.Option {
	return func(o *server.Options) {
		codecs := make(map[string]encoding.Codec)
		if o.Context == nil {
			o.Context = context.Background()
		}
		if v, ok := o.Context.Value(codecsKey{}).(map[string]encoding.Codec); ok && v != nil {
			codecs = v
		}
		codecs[contentType] = c
		o.Context = context.WithValue(o.Context, codecsKey{}, codecs)
	}
}

// AuthTLS should be used to setup a secure authentication using TLS.
func AuthTLS(t *tls.Config) server.Option {
	return setServerOption(tlsAuth{}, t)
}

// MaxConn specifies maximum number of max simultaneous connections to server.
func MaxConn(n int) server.Option {
	return setServerOption(maxConnKey{}, n)
}

// Listener specifies the net.Listener to use instead of the default.
func Listener(l net.Listener) server.Option {
	return setServerOption(netListener{}, l)
}

// Server specifies a *grpc.Server to use instead of the default
// This is for rare use case where user need to expose grpc.Server for
// customization. Please NOTE however user injected grpcServer doesn't support
// server Handler abstraction.
func Server(srv *grpc.Server) server.Option {
	return setServerOption(grpcServerKey{}, srv)
}

// Options to be used to configure gRPC options.
func Options(opts ...grpc.ServerOption) server.Option {
	return setServerOption(grpcOptions{}, opts)
}

// MaxMsgSize set the maximum message in bytes the server can receive and
// send.  Default maximum message size is 4 MB.
func MaxMsgSize(s int) server.Option {
	return setServerOption(maxMsgSizeKey{}, s)
}

const (
	// DefaultGracefulStopTimeout is how long Stop waits for calls in
	// flight before it closes them. With the default drain delay of 0 it
	// fits Kubernetes' default 30s termination grace period.
	DefaultGracefulStopTimeout = 15 * time.Second

	// EnvGracefulStopTimeout and EnvDrainDelay set GracefulStopTimeout and
	// DrainDelay from the environment, as durations such as "20s", for
	// servers whose code sets neither.
	EnvGracefulStopTimeout = "MICRO_SERVER_GRACEFUL_STOP_TIMEOUT"
	EnvDrainDelay          = "MICRO_SERVER_DRAIN_DELAY"
)

// GracefulStopTimeout sets how long Stop waits for calls in flight before
// it closes them (default DefaultGracefulStopTimeout). Without this
// option, MICRO_SERVER_GRACEFUL_STOP_TIMEOUT applies.
func GracefulStopTimeout(timeout time.Duration) server.Option {
	return setServerOption(gracefulStopTimeoutKey{}, timeout)
}

// DrainDelay makes Stop keep serving for d after the node is deregistered
// and its health turns NOT_SERVING, before it stops accepting calls. It
// gives callers time to learn that the node is gone: registry watches
// and caches, gateways that poll, Kubernetes endpoints. The default is 0.
// A few seconds avoid failed calls during rolling deploys. Without this
// option, MICRO_SERVER_DRAIN_DELAY applies.
func DrainDelay(d time.Duration) server.Option {
	return setServerOption(drainDelayKey{}, d)
}

func newOptions(opt ...server.Option) server.Options {
	opts := server.Options{
		Codecs:        make(map[string]codec.NewCodec),
		Metadata:      map[string]string{},
		Broker:        broker.DefaultBroker,
		Registry:      registry.DefaultRegistry,
		RegisterCheck: server.DefaultRegisterCheck,
		Transport:     transport.DefaultTransport,
		Address:       server.DefaultAddress,
		Name:          server.DefaultName,
		Id:            server.DefaultId,
		Version:       server.DefaultVersion,
		Logger:        logger.DefaultLogger,
	}

	for _, o := range opt {
		o(&opts)
	}

	return opts
}
