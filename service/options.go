package service

import (
	"context"
	"time"

	"github.com/flylib/go-micro/auth"
	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/cache"
	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/cmd"
	"github.com/flylib/go-micro/config"
	"github.com/flylib/go-micro/debug/profile"
	"github.com/flylib/go-micro/debug/trace"
	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/model"
	"github.com/flylib/go-micro/model/memory"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
	"github.com/flylib/go-micro/server"
	"github.com/flylib/go-micro/store"
	"github.com/flylib/go-micro/transport"
	"github.com/urfave/cli/v2"
)

// Options for micro service.
type Options struct {
	Registry registry.Registry
	Store    store.Store
	Auth     auth.Auth
	Cmd      cmd.Cmd
	Config   config.Config
	Client   client.Client
	Server   server.Server
	Model    model.Model

	// Other options for implementations of the interface
	// can be stored in a context
	Context context.Context

	Cache     cache.Cache
	Profile   profile.Profile
	Transport transport.Transport
	Logger    logger.Logger
	Broker    broker.Broker
	// Before and After funcs
	BeforeStart []func() error
	AfterStart  []func() error
	AfterStop   []func() error

	BeforeStop []func() error

	Signal bool

	// Service-level settings of the server, client and broker, replayed
	// onto a replacement passed with Server, Client or Broker, so options
	// apply whatever their order.
	serverOps []func(server.Server)
	clientOps []func(client.Client) client.Client
	brokerOps []func(broker.Broker)
}

type Option func(*Options)

// onServer applies f to the server now and to any server set later.
func (o *Options) onServer(f func(server.Server)) {
	o.serverOps = append(o.serverOps, f)
	f(o.Server)
}

// onClient applies f to the client now and to any client set later; f
// may return a wrapped client.
func (o *Options) onClient(f func(client.Client) client.Client) {
	o.clientOps = append(o.clientOps, f)
	o.Client = f(o.Client)
}

// onBroker applies f to the broker now and to any broker set later.
func (o *Options) onBroker(f func(broker.Broker)) {
	o.brokerOps = append(o.brokerOps, f)
	f(o.Broker)
}

func initServer(opts ...server.Option) func(server.Server) {
	return func(s server.Server) { _ = s.Init(opts...) }
}

func initClient(opts ...client.Option) func(client.Client) client.Client {
	return func(c client.Client) client.Client {
		_ = c.Init(opts...)
		return c
	}
}

func newOptions(opts ...Option) Options {
	opt := Options{
		Auth:   auth.DefaultAuth,
		Broker: broker.DefaultBroker,
		Cmd:    cmd.NewCmd(),
		Config: config.DefaultConfig,
		// Per-service instances: each service gets its own server, client,
		// store, and cache to allow multiple services in a single binary.
		Client:    client.NewClient(),
		Server:    server.NewRPCServer(),
		Store:     store.NewStore(),
		Model:     memory.New(),
		Cache:     cache.NewCache(),
		Registry:  registry.DefaultRegistry,
		Transport: transport.DefaultTransport,
		Context:   context.Background(),
		Signal:    true,
		Logger:    logger.DefaultLogger,
	}

	for _, o := range opts {
		o(&opt)
	}

	return opt
}

// Broker to be used for service.
func Broker(b broker.Broker) Option {
	return func(o *Options) {
		o.Broker = b
		for _, f := range o.brokerOps {
			f(b)
		}
		// Update Client and Server
		o.onClient(initClient(client.Broker(b)))
		o.onServer(initServer(server.Broker(b)))
	}
}

func Cache(c cache.Cache) Option {
	return func(o *Options) {
		o.Cache = c
	}
}

func Cmd(c cmd.Cmd) Option {
	return func(o *Options) {
		o.Cmd = c
	}
}

// Client to be used for service. Service-level options given before or
// after it (Registry, Broker, Transport, Selector, WrapCall, WrapClient,
// ...) apply to it.
func Client(c client.Client) Option {
	return func(o *Options) {
		for _, f := range o.clientOps {
			c = f(c)
		}
		o.Client = c
	}
}

// Context specifies a context for the service.
// Can be used to signal shutdown of the service and for extra option values.
func Context(ctx context.Context) Option {
	return func(o *Options) {
		o.Context = ctx
	}
}

// Handle will register a handler without any fuss
func Handle(v interface{}) Option {
	return func(o *Options) {
		o.onServer(func(s server.Server) { _ = s.Handle(s.NewHandler(v)) })
	}
}

// HandleSignal toggles automatic installation of the signal handler that
// traps TERM, INT, and QUIT.  Users of this feature to disable the signal
// handler, should control liveness of the service through the context.
func HandleSignal(b bool) Option {
	return func(o *Options) {
		o.Signal = b
	}
}

// Profile to be used for debug profile.
func Profile(p profile.Profile) Option {
	return func(o *Options) {
		o.Profile = p
	}
}

// Server to be used for service. Service-level options given before or
// after it (Name, Version, Address, Registry, Broker, Transport, Handle,
// WrapHandler, ...) apply to it, and take precedence over the server's
// own settings for the same things.
func Server(s server.Server) Option {
	return func(o *Options) {
		for _, f := range o.serverOps {
			f(s)
		}
		o.Server = s
	}
}

// Store sets the store to use.
func Store(s store.Store) Option {
	return func(o *Options) {
		o.Store = s
	}
}

// Model sets the model backend to use.
func Model(m model.Model) Option {
	return func(o *Options) {
		o.Model = m
	}
}

// Registry sets the registry for the service
// and the underlying components.
func Registry(r registry.Registry) Option {
	return func(o *Options) {
		o.Registry = r
		// Update Client and Server
		o.onClient(initClient(client.Registry(r)))
		o.onServer(initServer(server.Registry(r)))
		// Update Broker
		o.onBroker(func(b broker.Broker) { _ = b.Init(broker.Registry(r)) })
	}
}

// Tracer sets the tracer for the service.
func Tracer(t trace.Tracer) Option {
	return func(o *Options) {
		o.onServer(initServer(server.Tracer(t)))
	}

}

// Auth sets the auth for the service.
func Auth(a auth.Auth) Option {
	return func(o *Options) {
		o.Auth = a
	}
}

// Config sets the config for the service.
func Config(c config.Config) Option {
	return func(o *Options) {
		o.Config = c
	}
}

// Selector sets the selector for the service client.
func Selector(s selector.Selector) Option {
	return func(o *Options) {
		o.onClient(initClient(client.Selector(s)))
	}
}

// Transport sets the transport for the service
// and the underlying components.
func Transport(t transport.Transport) Option {
	return func(o *Options) {
		o.Transport = t
		// Update Client and Server
		o.onClient(initClient(client.Transport(t)))
		o.onServer(initServer(server.Transport(t)))
	}
}

// Convenience options

// Address sets the address of the server.
func Address(addr string) Option {
	return func(o *Options) {
		o.onServer(initServer(server.Address(addr)))
	}
}

// Name of the service.
func Name(n string) Option {
	return func(o *Options) {
		o.onServer(initServer(server.Name(n)))
	}
}

// Version of the service.
func Version(v string) Option {
	return func(o *Options) {
		o.onServer(initServer(server.Version(v)))
	}
}

// Metadata associated with the service.
func Metadata(md map[string]string) Option {
	return func(o *Options) {
		o.onServer(initServer(server.Metadata(md)))
	}
}

// Flags that can be passed to service.
func Flags(flags ...cli.Flag) Option {
	return func(o *Options) {
		o.Cmd.App().Flags = append(o.Cmd.App().Flags, flags...)
	}
}

// Action can be used to parse user provided cli options.
func Action(a func(*cli.Context) error) Option {
	return func(o *Options) {
		o.Cmd.App().Action = a
	}
}

// RegisterTTL specifies the TTL to use when registering the service.
func RegisterTTL(t time.Duration) Option {
	return func(o *Options) {
		o.onServer(initServer(server.RegisterTTL(t)))
	}
}

// RegisterInterval specifies the interval on which to re-register.
func RegisterInterval(t time.Duration) Option {
	return func(o *Options) {
		o.onServer(initServer(server.RegisterInterval(t)))
	}
}

// WrapClient is a convenience method for wrapping a Client with
// some middleware component. A list of wrappers can be provided.
// Wrappers are applied in reverse order so the last is executed first.
func WrapClient(w ...client.Wrapper) Option {
	return func(o *Options) {
		o.onClient(func(c client.Client) client.Client {
			// apply in reverse
			for i := len(w); i > 0; i-- {
				c = w[i-1](c)
			}
			return c
		})
	}
}

// WrapCall is a convenience method for wrapping a Client CallFunc.
func WrapCall(w ...client.CallWrapper) Option {
	return func(o *Options) {
		o.onClient(initClient(client.WrapCall(w...)))
	}
}

// WrapHandler adds a handler Wrapper to a list of options passed into the server.
func WrapHandler(w ...server.HandlerWrapper) Option {
	return func(o *Options) {
		var wrappers []server.Option

		for _, wrap := range w {
			wrappers = append(wrappers, server.WrapHandler(wrap))
		}

		// Init once
		o.onServer(initServer(wrappers...))
	}
}

// WrapSubscriber adds a subscriber Wrapper to a list of options passed into the server.
func WrapSubscriber(w ...server.SubscriberWrapper) Option {
	return func(o *Options) {
		var wrappers []server.Option

		for _, wrap := range w {
			wrappers = append(wrappers, server.WrapSubscriber(wrap))
		}

		// Init once
		o.onServer(initServer(wrappers...))
	}
}

// Add opt to server option.
func AddListenOption(option server.Option) Option {
	return func(o *Options) {
		o.onServer(initServer(option))
	}
}

// Before and Afters

// BeforeStart run funcs before service starts.
func BeforeStart(fn func() error) Option {
	return func(o *Options) {
		o.BeforeStart = append(o.BeforeStart, fn)
	}
}

// BeforeStop run funcs before service stops.
func BeforeStop(fn func() error) Option {
	return func(o *Options) {
		o.BeforeStop = append(o.BeforeStop, fn)
	}
}

// AfterStart run funcs after service starts.
func AfterStart(fn func() error) Option {
	return func(o *Options) {
		o.AfterStart = append(o.AfterStart, fn)
	}
}

// AfterStop run funcs after service stops.
func AfterStop(fn func() error) Option {
	return func(o *Options) {
		o.AfterStop = append(o.AfterStop, fn)
	}
}

// Logger sets the logger for the service.
func Logger(l logger.Logger) Option {
	return func(o *Options) {
		o.Logger = l
	}
}
