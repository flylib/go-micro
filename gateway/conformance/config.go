// Package conformance is the black-box test suite every go-micro gateway
// implementation must pass (gateway/SPEC.md section 14). It drives a running
// gateway from the outside: it starts its own backend services, registers
// them in the registry the gateway watches, writes rules to the source the
// gateway loads, and calls through the gateway with a plain gRPC client.
//
// Configuration is by environment, using the same names as the gateway's
// bootstrap (SPEC.md section 11) so one env file can drive both:
//
//	GATEWAY_ADDR                  gateway listen address, e.g. localhost:8080 (unset: gateway cases skip)
//	GATEWAY_HTTP_ADDR             gateway HTTP/JSON entry address (unset: J cases skip)
//	GATEWAY_WS_URL                gateway WebSocket entry, e.g. ws://localhost:8090/ws (unset: W cases skip)
//	MICRO_BROKER                  broker the gateway uses for push: nats (unset: W5, W6 skip)
//	MICRO_BROKER_ADDRESS          comma-separated broker addresses
//	MICRO_REGISTRY                etcd | consul | nacos
//	MICRO_REGISTRY_ADDRESS        comma-separated host:port
//	MICRO_REGISTRY_NAMESPACE      nacos namespace (optional)
//	MICRO_REGISTRY_GROUP          nacos group (optional)
//	MICRO_REGISTRY_USERNAME       nacos username, for servers with auth on (optional)
//	MICRO_REGISTRY_PASSWORD       nacos password
//	MICRO_GATEWAY_RULES           rules source URI the gateway was started with
//	CONFORMANCE_ADVERTISE_HOST    host the gateway can reach backends at (default: first private IP)
//	CONFORMANCE_REGISTRY_STOP     shell command that stops the registry (D4; optional)
//	CONFORMANCE_REGISTRY_START    shell command that starts it again (D4; optional)
package conformance

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/registry/consul"
	"github.com/flylib/go-micro/registry/etcd"
	"github.com/flylib/go-micro/registry/nacos"
	"github.com/flylib/go-micro/util/addr"
)

// Deadlines the spec puts on gateways; a short grace absorbs test overhead.
const (
	propagation = 5 * time.Second         // SPEC 4.4 and 10: changes visible within 5s
	grace       = 1500 * time.Millisecond // polling and scheduling slack
	callTimeout = 5 * time.Second
)

type config struct {
	gateway       string
	httpGateway   string
	wsURL         string
	broker        string
	brokerAddrs   []string
	registry      string
	registryAddrs []string
	namespace     string
	group         string
	username      string
	password      string
	rules         string
	advertiseHost string
	registryStop  string
	registryStart string
}

func loadConfig() config {
	c := config{
		gateway:       os.Getenv("GATEWAY_ADDR"),
		httpGateway:   os.Getenv("GATEWAY_HTTP_ADDR"),
		wsURL:         os.Getenv("GATEWAY_WS_URL"),
		broker:        os.Getenv("MICRO_BROKER"),
		registry:      os.Getenv("MICRO_REGISTRY"),
		namespace:     os.Getenv("MICRO_REGISTRY_NAMESPACE"),
		group:         os.Getenv("MICRO_REGISTRY_GROUP"),
		username:      os.Getenv("MICRO_REGISTRY_USERNAME"),
		password:      os.Getenv("MICRO_REGISTRY_PASSWORD"),
		rules:         os.Getenv("MICRO_GATEWAY_RULES"),
		advertiseHost: os.Getenv("CONFORMANCE_ADVERTISE_HOST"),
		registryStop:  os.Getenv("CONFORMANCE_REGISTRY_STOP"),
		registryStart: os.Getenv("CONFORMANCE_REGISTRY_START"),
	}
	if v := os.Getenv("MICRO_REGISTRY_ADDRESS"); v != "" {
		c.registryAddrs = strings.Split(v, ",")
	}
	if v := os.Getenv("MICRO_BROKER_ADDRESS"); v != "" {
		c.brokerAddrs = strings.Split(v, ",")
	}
	if c.advertiseHost == "" {
		if h, err := addr.Extract(""); err == nil {
			c.advertiseHost = h
		}
	}
	return c
}

// newRegistry builds the go-micro registry the backends register in. It
// must be the same registry, with the same settings, the gateway reads.
func (c config) newRegistry() (registry.Registry, error) {
	opts := []registry.Option{registry.Addrs(c.registryAddrs...)}
	switch c.registry {
	case "etcd":
		return etcd.NewEtcdRegistry(opts...), nil
	case "consul":
		return consul.NewConsulRegistry(opts...), nil
	case "nacos":
		if c.namespace != "" {
			opts = append(opts, nacos.WithNamespaceId(c.namespace))
		}
		if c.group != "" {
			opts = append(opts, nacos.WithGroupName(c.group))
		}
		if c.username != "" {
			opts = append(opts, nacos.WithAuth(c.username, c.password))
		}
		return nacos.NewRegistry(opts...), nil
	default:
		return nil, fmt.Errorf("MICRO_REGISTRY must be etcd, consul or nacos, got %q", c.registry)
	}
}
