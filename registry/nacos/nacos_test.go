package nacos

import (
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"

	"github.com/flylib/go-micro/registry"
)

// TestSplitAddress is a pure unit test for address parsing.
func TestSplitAddress(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port uint64
	}{
		{"10.0.0.1:8848", "10.0.0.1", 8848},
		{"nacos-host", "nacos-host", defaultPort},
	}
	for _, c := range cases {
		h, p, err := splitAddress(c.in)
		if err != nil || h != c.host || p != c.port {
			t.Fatalf("splitAddress(%q) = (%q,%d,%v), want (%q,%d)", c.in, h, p, err, c.host, c.port)
		}
	}
}

// fakeNaming stands in for the SDK client, so no server is needed.
type fakeNaming struct{ naming_client.INamingClient }

func TestEnvironmentFallbacks(t *testing.T) {
	t.Setenv(EnvNamespace, "env-ns")
	t.Setenv(EnvGroup, "ENV_GROUP")
	t.Setenv(EnvUsername, "env-user")
	t.Setenv(EnvPassword, "env-pass")

	// nothing set in code: the environment applies
	r := NewRegistry(WithNamingClient(fakeNaming{})).(*nacosRegistry)
	if r.namespace != "env-ns" || r.group != "ENV_GROUP" {
		t.Fatalf("namespace %q group %q", r.namespace, r.group)
	}
	if cc := r.clientConfig(); cc.NamespaceId != "env-ns" || cc.Username != "env-user" || cc.Password != "env-pass" {
		t.Fatalf("client config %q %q %q", cc.NamespaceId, cc.Username, cc.Password)
	}

	// options win
	r = NewRegistry(WithNamingClient(fakeNaming{}), WithNamespaceId("code-ns"), WithGroupName("CODE_GROUP"),
		WithAuth("code-user", "code-pass")).(*nacosRegistry)
	if r.namespace != "code-ns" || r.group != "CODE_GROUP" {
		t.Fatalf("namespace %q group %q", r.namespace, r.group)
	}
	if cc := r.clientConfig(); cc.NamespaceId != "code-ns" || cc.Username != "code-user" || cc.Password != "code-pass" {
		t.Fatalf("client config %q %q %q", cc.NamespaceId, cc.Username, cc.Password)
	}
}

func TestRegistersAsPlugin(t *testing.T) {
	fn, ok := registry.Plugin("nacos")
	if !ok || fn == nil {
		t.Fatal("nacos is not registered as a registry plugin")
	}
}
