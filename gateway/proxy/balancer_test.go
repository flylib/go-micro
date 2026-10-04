package proxy

import (
	"testing"

	"github.com/flylib/go-micro/registry"
)

func node(id, proto string) *registry.Node {
	return &registry.Node{Id: id, Address: id + ":1", Metadata: map[string]string{"protocol": proto}}
}

func TestEligibleSkipsMUCP(t *testing.T) {
	svcs := []*registry.Service{{Name: "s", Version: "v1", Nodes: []*registry.Node{node("a", "grpc"), node("b", "mucp"), node("c", "")}}}
	out := eligible(svcs, FilterSpec{}, "")
	if countNodes(out) != 1 || out[0].Nodes[0].Id != "a" {
		t.Fatalf("eligible = %+v", out[0].Nodes)
	}
	if len(svcs[0].Nodes) != 3 {
		t.Fatal("eligible mutated the registry's services")
	}
}

func TestEligibleFilters(t *testing.T) {
	a, b := node("a", "grpc"), node("b", "grpc")
	b.Metadata["zone"] = "z1"
	svcs := []*registry.Service{
		{Name: "s", Version: "v1", Nodes: []*registry.Node{a}},
		{Name: "s", Version: "v2", Nodes: []*registry.Node{b}, Endpoints: []*registry.Endpoint{{Name: "Svc.Call"}}},
	}
	if out := eligible(svcs, FilterSpec{Version: "v2"}, ""); countNodes(out) != 1 || out[0].Version != "v2" {
		t.Error("version filter")
	}
	if out := eligible(svcs, FilterSpec{Labels: map[string]string{"zone": "z1"}}, ""); countNodes(out) != 1 || out[0].Nodes[0].Id != "b" {
		t.Error("label filter")
	}
	if out := eligible(svcs, FilterSpec{Endpoint: true}, "Svc.Call"); countNodes(out) != 1 || out[0].Version != "v2" {
		t.Error("endpoint filter")
	}
}

func TestRoundRobinRotatesAcrossCalls(t *testing.T) {
	rt := &route{selector: SelectorSpec{Strategy: "roundrobin"}}
	svcs := []*registry.Service{{Nodes: []*registry.Node{node("a", "grpc"), node("b", "grpc"), node("c", "grpc")}}}
	seen := map[string]int{}
	for i := 0; i < 30; i++ {
		seen[picker(rt, svcs)().Id]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if seen[id] != 10 {
			t.Fatalf("uneven rotation: %v", seen)
		}
	}
}

func TestPickerNeverRepeats(t *testing.T) {
	for _, strat := range []string{"roundrobin", "random", "weighted", "p2c"} {
		rt := &route{selector: SelectorSpec{Strategy: strat}}
		svcs := []*registry.Service{{Nodes: []*registry.Node{node("a", "grpc"), node("b", "grpc")}}}
		next := picker(rt, svcs)
		n1, n2, n3 := next(), next(), next()
		if n1 == nil || n2 == nil || n1.Address == n2.Address || n3 != nil {
			t.Errorf("%s: picks %v %v %v", strat, n1, n2, n3)
		}
	}
}

func TestVersionWeightsExcludeZero(t *testing.T) {
	rt := &route{selector: SelectorSpec{Strategy: "roundrobin", VersionWeights: map[string]int{"v1": 100, "v2": 0}}}
	svcs := []*registry.Service{
		{Version: "v1", Nodes: []*registry.Node{node("a", "grpc")}},
		{Version: "v2", Nodes: []*registry.Node{node("b", "grpc")}},
	}
	for i := 0; i < 50; i++ {
		if n := picker(rt, svcs)(); n == nil || n.Id != "a" {
			t.Fatalf("picked %v", n)
		}
	}
}
