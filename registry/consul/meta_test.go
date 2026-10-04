package consul

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/flylib/go-micro/registry"
	consul "github.com/hashicorp/consul/api"
)

// validMetaKey is Consul's rule for Meta keys.
var validMetaKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TestRegisterWritesPlainVersionMeta checks that Register puts the node
// metadata plus micro_version into Meta in plain text, so readers that
// cannot decode the zlib+hex tags still see the version.
func TestRegisterWritesPlainVersionMeta(t *testing.T) {
	var got consul.AgentServiceRegistration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent/service/register") {
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode register body: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewConsulRegistry(registry.Addrs(strings.TrimPrefix(srv.URL, "http://")))
	nodeMeta := map[string]string{"protocol": "grpc"}
	svc := &registry.Service{
		Name:    "greeter",
		Version: "v2",
		Nodes:   []*registry.Node{{Id: "greeter-1", Address: "10.0.0.1:50051", Metadata: nodeMeta}},
	}
	if err := r.Register(svc); err != nil {
		t.Fatal(err)
	}

	if got.Meta[metaVersionKey] != "v2" {
		t.Errorf("Meta[%s] = %q, want v2", metaVersionKey, got.Meta[metaVersionKey])
	}
	if got.Meta["protocol"] != "grpc" {
		t.Errorf("node metadata lost from Meta: %v", got.Meta)
	}
	// a real agent rejects the whole registration over one bad Meta key
	for k := range got.Meta {
		if !validMetaKey.MatchString(k) {
			t.Errorf("Meta key %q is invalid for Consul", k)
		}
	}
	if _, ok := nodeMeta[metaVersionKey]; ok {
		t.Error("Register mutated the caller's node metadata")
	}
}
