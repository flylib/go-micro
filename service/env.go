package service

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/cmd"
	log "github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
	"github.com/flylib/go-micro/store"
)

// FromEnv configures the service from the environment, so one binary runs
// in every environment without code changes:
//
//	MICRO_REGISTRY           registry plugin: nacos, etcd, consul, ...
//	MICRO_REGISTRY_ADDRESS   comma-separated addresses of that registry
//	MICRO_BROKER             broker plugin: nats, kafka, rabbitmq, ...
//	MICRO_BROKER_ADDRESS     comma-separated addresses of that broker
//	MICRO_STORE              store plugin: redis, mongo, ...
//	MICRO_STORE_ADDRESS      comma-separated addresses of that store
//	MICRO_STORE_DATABASE     store database
//	MICRO_STORE_TABLE        store table
//	MICRO_SERVER_ADDRESS     listen address, e.g. :9001
//	MICRO_SERVER_ADVERTISE   address registered for callers, if not the listen address
//	MICRO_SERVER_VERSION     service version
//	MICRO_SERVER_METADATA    comma-separated key=value pairs added to the node metadata
//
// A plugin is found by name once its package is linked in, usually with a
// blank import: _ "github.com/flylib/go-micro/registry/nacos". An unknown
// name stops the service at start-up.
//
// Options after FromEnv win over the environment, options before it lose:
// put it first to let code override, last to let the environment override.
// Unlike Service.Init it parses no command line flags.
func FromEnv() Option {
	return func(o *Options) {
		opts, err := envOptions(os.Getenv)
		if err != nil {
			o.Logger.Log(log.FatalLevel, err)
			return
		}
		for _, opt := range opts {
			opt(o)
		}
	}
}

// envOptions turns the environment into options.
func envOptions(getenv func(string) string) ([]Option, error) {
	var opts []Option

	if name := getenv("MICRO_REGISTRY"); name != "" {
		newRegistry, ok := cmd.DefaultRegistries[name]
		if !ok {
			newRegistry, ok = registry.Plugin(name)
		}
		if !ok {
			return nil, unknownPlugin("MICRO_REGISTRY", name, "registry", append(keys(cmd.DefaultRegistries), registry.Plugins()...))
		}
		var ro []registry.Option
		if addrs := split(getenv("MICRO_REGISTRY_ADDRESS")); len(addrs) > 0 {
			ro = append(ro, registry.Addrs(addrs...))
		}
		opts = append(opts, Registry(newRegistry(ro...)))
	} else if getenv("MICRO_REGISTRY_ADDRESS") != "" {
		return nil, fmt.Errorf("MICRO_REGISTRY_ADDRESS is set without MICRO_REGISTRY: name the registry it belongs to")
	}

	if name := getenv("MICRO_BROKER"); name != "" {
		newBroker, ok := cmd.DefaultBrokers[name]
		if !ok {
			newBroker, ok = broker.Plugin(name)
		}
		if !ok {
			return nil, unknownPlugin("MICRO_BROKER", name, "broker", append(keys(cmd.DefaultBrokers), broker.Plugins()...))
		}
		var bo []broker.Option
		if addrs := split(getenv("MICRO_BROKER_ADDRESS")); len(addrs) > 0 {
			bo = append(bo, broker.Addrs(addrs...))
		}
		opts = append(opts, Broker(newBroker(bo...)))
	} else if getenv("MICRO_BROKER_ADDRESS") != "" {
		return nil, fmt.Errorf("MICRO_BROKER_ADDRESS is set without MICRO_BROKER: name the broker it belongs to")
	}

	if name := getenv("MICRO_STORE"); name != "" {
		newStore, ok := cmd.DefaultStores[name]
		if !ok {
			newStore, ok = store.Plugin(name)
		}
		if !ok {
			return nil, unknownPlugin("MICRO_STORE", name, "store", append(keys(cmd.DefaultStores), store.Plugins()...))
		}
		var so []store.Option
		if addrs := split(getenv("MICRO_STORE_ADDRESS")); len(addrs) > 0 {
			so = append(so, store.Nodes(addrs...))
		}
		if db := getenv("MICRO_STORE_DATABASE"); db != "" {
			so = append(so, store.Database(db))
		}
		if table := getenv("MICRO_STORE_TABLE"); table != "" {
			so = append(so, store.Table(table))
		}
		opts = append(opts, Store(newStore(so...)))
	}

	if v := getenv("MICRO_SERVER_ADDRESS"); v != "" {
		opts = append(opts, Address(v))
	}
	if v := getenv("MICRO_SERVER_ADVERTISE"); v != "" {
		opts = append(opts, AddListenOption(server.Advertise(v)))
	}
	if v := getenv("MICRO_SERVER_VERSION"); v != "" {
		opts = append(opts, Version(v))
	}
	if pairs := split(getenv("MICRO_SERVER_METADATA")); len(pairs) > 0 {
		md := make(map[string]string, len(pairs))
		for _, p := range pairs {
			k, v, ok := strings.Cut(p, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("MICRO_SERVER_METADATA: %q is not key=value", p)
			}
			md[k] = v
		}
		opts = append(opts, Metadata(md))
	}
	return opts, nil
}

func unknownPlugin(env, name, kind string, have []string) error {
	return fmt.Errorf("%s=%s: no such %s plugin is linked into this binary (have: %s); import its package, e.g. _ \"github.com/flylib/go-micro/%s/%s\"",
		env, name, kind, strings.Join(have, ", "), kind, name)
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func split(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
