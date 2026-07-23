package weighted

import (
	"testing"

	"github.com/flylib/go-micro/registry"
)

func dist(t *testing.T, next func() (*registry.Node, error), n int) map[string]int {
	t.Helper()
	out := map[string]int{}
	for i := 0; i < n; i++ {
		node, err := next()
		if err != nil {
			t.Fatal(err)
		}
		out[node.Id]++
	}
	return out
}

func TestMetadataWeights(t *testing.T) {
	svcs := []*registry.Service{{
		Name: "svc",
		Nodes: []*registry.Node{
			{Id: "heavy", Address: "a:1", Metadata: map[string]string{"weight": "90"}},
			{Id: "light", Address: "b:1", Metadata: map[string]string{"weight": "10"}},
		},
	}}
	d := dist(t, Strategy(svcs), 5000)
	ratio := float64(d["heavy"]) / float64(d["light"]+1)
	if ratio < 5 || ratio > 15 { // expect ~9
		t.Fatalf("bad split heavy=%d light=%d", d["heavy"], d["light"])
	}
}

func TestVersionWeightsCanary(t *testing.T) {
	svcs := []*registry.Service{
		{Name: "svc", Version: "v1", Nodes: []*registry.Node{{Id: "v1-a", Address: "a:1"}}},
		{Name: "svc", Version: "v2", Nodes: []*registry.Node{{Id: "v2-a", Address: "b:1"}}},
	}
	d := dist(t, VersionWeights(map[string]int{"v1": 95, "v2": 5})(svcs), 5000)
	pct := float64(d["v2-a"]) / 5000 * 100
	if pct < 2 || pct > 9 { // expect ~5%
		t.Fatalf("canary share off: %.1f%% (v1=%d v2=%d)", pct, d["v1-a"], d["v2-a"])
	}
	// unlisted version gets zero
	d2 := dist(t, VersionWeights(map[string]int{"v1": 100})(svcs), 500)
	if d2["v2-a"] != 0 {
		t.Fatalf("unlisted version must get no traffic: %v", d2)
	}
}
