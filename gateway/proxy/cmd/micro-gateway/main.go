// Command micro-gateway runs the Go gateway (gateway/proxy). It is
// configured by the bootstrap settings of gateway/SPEC.md section 11:
//
//	MICRO_GATEWAY_ADDRESS          listen address (default :8080)
//	MICRO_REGISTRY                 etcd | consul | nacos
//	MICRO_REGISTRY_ADDRESS         comma-separated host:port
//	MICRO_REGISTRY_NAMESPACE       nacos namespace
//	MICRO_REGISTRY_GROUP           nacos group
//	MICRO_GATEWAY_RULES            rules source URI, or "none"
//	MICRO_GATEWAY_TRUSTED_PROXIES  comma-separated CIDRs
//	MICRO_GATEWAY_UPSTREAM_TLS     "true" to dial nodes with TLS
package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/flylib/go-micro/gateway/proxy"
	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/registry/consul"
	"github.com/flylib/go-micro/registry/etcd"
	"github.com/flylib/go-micro/registry/nacos"
)

func main() {
	if err := run(); err != nil {
		logger.Logf(logger.ErrorLevel, "micro-gateway: %v", err)
		os.Exit(1)
	}
}

func run() error {
	reg, err := newRegistry()
	if err != nil {
		return err
	}
	opts := []proxy.Option{proxy.Registry(reg)}

	rules := os.Getenv("MICRO_GATEWAY_RULES")
	switch rules {
	case "":
		return fmt.Errorf("MICRO_GATEWAY_RULES is required (a rules source URI, or %q)", proxy.RulesNone)
	case proxy.RulesNone:
	default:
		src, err := proxy.SourceFromURI(rules)
		if err != nil {
			return err
		}
		opts = append(opts, proxy.RulesFrom(src))
	}

	if v := os.Getenv("MICRO_GATEWAY_TRUSTED_PROXIES"); v != "" {
		prefixes, err := proxy.ParsePrefixes(split(v))
		if err != nil {
			return fmt.Errorf("MICRO_GATEWAY_TRUSTED_PROXIES: %w", err)
		}
		opts = append(opts, proxy.TrustedProxies(prefixes...))
	}
	if os.Getenv("MICRO_GATEWAY_UPSTREAM_TLS") == "true" {
		opts = append(opts, proxy.UpstreamTLS(&tls.Config{MinVersion: tls.VersionTLS12}))
	}

	gw, err := proxy.New(opts...)
	if err != nil {
		return err
	}

	addr := os.Getenv("MICRO_GATEWAY_ADDRESS")
	if addr == "" {
		addr = ":8080"
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		gw.Stop()
	}()

	logger.Logf(logger.InfoLevel, "micro-gateway listening on %s (registry %s, rules %s)", l.Addr(), reg, rules)
	return gw.Serve(l)
}

func newRegistry() (registry.Registry, error) {
	opts := []registry.Option{registry.Addrs(split(os.Getenv("MICRO_REGISTRY_ADDRESS"))...)}
	switch r := os.Getenv("MICRO_REGISTRY"); r {
	case "etcd":
		return etcd.NewEtcdRegistry(opts...), nil
	case "consul":
		return consul.NewConsulRegistry(opts...), nil
	case "nacos":
		if ns := os.Getenv("MICRO_REGISTRY_NAMESPACE"); ns != "" {
			opts = append(opts, nacos.WithNamespaceId(ns))
		}
		if g := os.Getenv("MICRO_REGISTRY_GROUP"); g != "" {
			opts = append(opts, nacos.WithGroupName(g))
		}
		return nacos.NewRegistry(opts...), nil
	default:
		return nil, fmt.Errorf("MICRO_REGISTRY must be etcd, consul or nacos, got %q", r)
	}
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
