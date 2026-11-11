package service

import (
	"strings"
	"testing"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/cmd"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

// apply builds options from an environment and then from code.
func apply(t *testing.T, getenv func(string) string, code ...Option) Options {
	t.Helper()
	fromEnv, err := envOptions(getenv)
	if err != nil {
		t.Fatal(err)
	}
	return newOptions(append(append([]Option{Name("svc")}, fromEnv...), code...)...)
}

type fakeRegistry struct {
	registry.Registry
	addrs []string
}

func (fakeRegistry) String() string { return "fake" }

func TestFromEnv(t *testing.T) {
	var made *fakeRegistry
	cmd.DefaultRegistries["fake"] = func(opts ...registry.Option) registry.Registry {
		var o registry.Options
		for _, opt := range opts {
			opt(&o)
		}
		made = &fakeRegistry{Registry: registry.NewMemoryRegistry(), addrs: o.Addrs}
		return made
	}
	t.Cleanup(func() { delete(cmd.DefaultRegistries, "fake") })

	o := apply(t, env(
		"MICRO_REGISTRY", "fake", "MICRO_REGISTRY_ADDRESS", "a:1, b:2",
		"MICRO_BROKER", "memory",
		"MICRO_SERVER_ADDRESS", ":9001", "MICRO_SERVER_ADVERTISE", "10.0.0.5:9001",
		"MICRO_SERVER_VERSION", "v7", "MICRO_SERVER_METADATA", "zone=a,tier=web",
	))
	if o.Registry != registry.Registry(made) || strings.Join(made.addrs, ",") != "a:1,b:2" {
		t.Fatalf("registry %v with addresses %v", o.Registry, made.addrs)
	}
	so := o.Server.Options()
	if so.Registry != registry.Registry(made) || o.Client.Options().Registry != registry.Registry(made) {
		t.Fatal("server or client did not get the registry from the environment")
	}
	if o.Broker.String() != "memory" || so.Broker != o.Broker {
		t.Fatalf("broker %s", o.Broker)
	}
	if so.Address != ":9001" || so.Advertise != "10.0.0.5:9001" || so.Version != "v7" || so.Metadata["zone"] != "a" || so.Metadata["tier"] != "web" {
		t.Fatalf("server settings: %q %q %q %v", so.Address, so.Advertise, so.Version, so.Metadata)
	}
	if so.Name != "svc" {
		t.Fatalf("name %q", so.Name)
	}
}

// Code after the environment wins; the environment fills in the rest.
func TestCodeWinsOverEnv(t *testing.T) {
	codeReg := registry.NewMemoryRegistry()
	o := apply(t, env("MICRO_REGISTRY", "mdns", "MICRO_BROKER", "memory", "MICRO_SERVER_ADDRESS", ":9001"),
		Registry(codeReg), Address(":7000"))
	if o.Registry != codeReg || o.Server.Options().Registry != codeReg {
		t.Fatal("the registry set in code lost to the environment")
	}
	if a := o.Server.Options().Address; a != ":7000" {
		t.Fatalf("address %q", a)
	}
	if o.Broker.String() != "memory" {
		t.Fatal("the broker from the environment was dropped")
	}
	// a server set in code still gets the environment's settings
	srv := server.NewRPCServer()
	o = apply(t, env("MICRO_SERVER_ADDRESS", ":9001", "MICRO_BROKER", "memory"), Server(srv))
	if so := srv.Options(); so.Address != ":9001" || so.Name != "svc" || so.Broker.String() != "memory" {
		t.Fatalf("server set in code: %q %q %s", so.Address, so.Name, so.Broker)
	}
}

func TestFromEnvErrors(t *testing.T) {
	for name, e := range map[string]func(string) string{
		"unknown registry":       env("MICRO_REGISTRY", "nacos"),
		"unknown broker":         env("MICRO_BROKER", "nats"),
		"unknown store":          env("MICRO_STORE", "redis"),
		"address without a kind": env("MICRO_REGISTRY_ADDRESS", "a:1"),
		"bad metadata":           env("MICRO_SERVER_METADATA", "zone"),
	} {
		if _, err := envOptions(e); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	_, err := envOptions(env("MICRO_REGISTRY", "nacos"))
	if !strings.Contains(err.Error(), `_ "github.com/flylib/go-micro/registry/nacos"`) {
		t.Fatalf("error does not say what to import: %v", err)
	}
	if opts, err := envOptions(env()); err != nil || len(opts) != 0 {
		t.Fatalf("empty environment: %d options, %v", len(opts), err)
	}
	_ = broker.DefaultBroker
}
