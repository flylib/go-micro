package nacos

import (
	"testing"

	"github.com/flylib/go-micro/registry"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
)

func inst(id, ip, version string, healthy bool) model.Instance {
	return model.Instance{
		InstanceId: id, Ip: ip, Port: 1, Healthy: healthy, Enable: true,
		Metadata: map[string]string{metaVersionKey: version},
	}
}

func drain(w *nacosWatcher) map[string]string {
	got := map[string]string{}
	for {
		select {
		case r := <-w.results:
			got[r.Service.Nodes[0].Id] = r.Action + "/" + r.Service.Version
		default:
			return got
		}
	}
}

// TestPushEmitsDiffs checks that full snapshots become per-node
// create/delete results grouped by version, so registry/cache drops
// removed nodes.
func TestPushEmitsDiffs(t *testing.T) {
	w := &nacosWatcher{service: "s", results: make(chan *registry.Result, 64), exit: make(chan struct{}), known: map[string]*registry.Service{}}

	w.push([]model.Instance{inst("a", "10.0.0.1", "v1", true), inst("b", "10.0.0.2", "v2", true), inst("c", "10.0.0.3", "v1", false)})
	if got := drain(w); len(got) != 2 || got["a"] != "create/v1" || got["b"] != "create/v2" {
		t.Fatalf("first push: %v (unhealthy c must be skipped)", got)
	}

	w.push([]model.Instance{inst("b", "10.0.0.2", "v2", true)})
	if got := drain(w); len(got) != 1 || got["a"] != "delete/v1" {
		t.Fatalf("removal: %v", got)
	}

	w.push([]model.Instance{inst("b", "10.0.0.2", "v2", true)})
	if got := drain(w); len(got) != 0 {
		t.Fatalf("unchanged snapshot emitted %v", got)
	}

	w.push([]model.Instance{inst("b", "10.0.0.2", "v3", true)})
	if got := drain(w); got["b"] != "create/v3" {
		t.Fatalf("changed node: %v", got)
	}
}
