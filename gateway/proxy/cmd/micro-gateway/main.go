// Command micro-gateway runs the Go gateway (gateway/proxy). It is
// configured by the bootstrap settings of gateway/SPEC.md section 11:
//
//	MICRO_GATEWAY_ADDRESS          listen address (default :8080)
//	MICRO_GATEWAY_HTTP_ADDRESS     HTTP/JSON entry listen address (empty: disabled)
//	MICRO_GATEWAY_HTTP_SERVER      nethttp (default; HTTP/1.1 + h2c) or fasthttp (HTTP/1.1)
//	MICRO_REGISTRY                 etcd | consul | nacos
//	MICRO_REGISTRY_ADDRESS         comma-separated host:port
//	MICRO_REGISTRY_NAMESPACE       nacos namespace
//	MICRO_REGISTRY_GROUP           nacos group
//	MICRO_REGISTRY_USERNAME        nacos username (servers with auth on)
//	MICRO_REGISTRY_PASSWORD        nacos password
//	MICRO_GATEWAY_RULES            rules source URI, or "none"
//	MICRO_GATEWAY_TRUSTED_PROXIES  comma-separated CIDRs
//	MICRO_GATEWAY_UPSTREAM_TLS     "true" to dial nodes with TLS
//	MICRO_BROKER                   nats, or empty: broker for WebSocket push and topics
//	MICRO_BROKER_ADDRESS           comma-separated broker addresses
package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/broker/nats"
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
		src, err := proxy.SourceFromURI(withRegistryAuth(rules))
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
	switch b := os.Getenv("MICRO_BROKER"); b {
	case "":
	case "nats":
		opts = append(opts, proxy.Broker(nats.NewNatsBroker(broker.Addrs(split(os.Getenv("MICRO_BROKER_ADDRESS"))...))))
	default:
		return fmt.Errorf("MICRO_BROKER must be nats or empty, got %q", b)
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

	// the HTTP/JSON entry is optional (SPEC 2.1)
	var hl net.Listener
	if haddr := os.Getenv("MICRO_GATEWAY_HTTP_ADDRESS"); haddr != "" {
		if hl, err = net.Listen("tcp", haddr); err != nil {
			return err
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		gw.Stop()
	}()

	errc := make(chan error, 2)
	if hl != nil {
		serve := gw.ServeAPI // net/http + chi: HTTP/1.1 and h2c
		kind := os.Getenv("MICRO_GATEWAY_HTTP_SERVER")
		switch kind {
		case "", "nethttp":
			kind = "nethttp"
		case "fasthttp":
			serve = gw.ServeAPIFast // HTTP/1.1 only
		default:
			return fmt.Errorf("MICRO_GATEWAY_HTTP_SERVER must be nethttp or fasthttp, got %q", kind)
		}
		logger.Logf(logger.InfoLevel, "micro-gateway HTTP/JSON entry (%s) listening on %s", kind, hl.Addr())
		go func() { errc <- serve(hl) }()
	}
	logger.Logf(logger.InfoLevel, "micro-gateway listening on %s (registry %s, rules %s)", l.Addr(), reg, proxy.RedactURI(rules))
	go func() { errc <- gw.Serve(l) }()

	// either entry failing stops the gateway; Stop ends both
	err = <-errc
	gw.Stop()
	return err
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
		if u := os.Getenv("MICRO_REGISTRY_USERNAME"); u != "" {
			opts = append(opts, nacos.WithAuth(u, os.Getenv("MICRO_REGISTRY_PASSWORD")))
		}
		return nacos.NewRegistry(opts...), nil
	default:
		return nil, fmt.Errorf("MICRO_REGISTRY must be etcd, consul or nacos, got %q", r)
	}
}

// withRegistryAuth gives a nacos:// rules URI without credentials the
// registry's, as rules and services usually live on one Nacos.
func withRegistryAuth(uri string) string {
	user := os.Getenv("MICRO_REGISTRY_USERNAME")
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "nacos" || u.User != nil || user == "" {
		return uri
	}
	u.User = url.UserPassword(user, os.Getenv("MICRO_REGISTRY_PASSWORD"))
	return u.String()
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
