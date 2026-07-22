package nacos

import (
	"context"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"

	"github.com/flylib/go-micro/registry"
)

type namingClientKey struct{}
type clientConfigKey struct{}
type serverConfigsKey struct{}
type groupNameKey struct{}
type namespaceKey struct{}

func setCtx(o *registry.Options, k, v interface{}) {
	if o.Context == nil {
		o.Context = context.Background()
	}
	o.Context = context.WithValue(o.Context, k, v)
}

// WithNamingClient injects a pre-built nacos naming client. When set,
// address/client/server config options are ignored.
func WithNamingClient(c naming_client.INamingClient) registry.Option {
	return func(o *registry.Options) { setCtx(o, namingClientKey{}, c) }
}

// WithClientConfig sets the nacos client config (namespace, timeouts, logging...).
func WithClientConfig(cc constant.ClientConfig) registry.Option {
	return func(o *registry.Options) { setCtx(o, clientConfigKey{}, cc) }
}

// WithServerConfigs sets the nacos server configs explicitly, overriding
// registry.Addrs.
func WithServerConfigs(scs ...constant.ServerConfig) registry.Option {
	return func(o *registry.Options) { setCtx(o, serverConfigsKey{}, scs) }
}

// WithGroupName sets the nacos group services are registered under
// (default: DEFAULT_GROUP).
func WithGroupName(name string) registry.Option {
	return func(o *registry.Options) { setCtx(o, groupNameKey{}, name) }
}

// WithNamespaceId sets the nacos namespace used for listing services
// (default: public).
func WithNamespaceId(ns string) registry.Option {
	return func(o *registry.Options) { setCtx(o, namespaceKey{}, ns) }
}
