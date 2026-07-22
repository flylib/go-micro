package nacos

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/flylib/go-micro/registry"
)

// nacosAddr returns the nacos address for integration tests, defaulting to
// the local podman container. Override with NACOS_ADDR.
func nacosAddr() string {
	if v := os.Getenv("NACOS_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:8848"
}

// waitReady polls the nacos readiness endpoint so the test tolerates a
// container that is still booting.
func waitReady(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://%s/nacos/v1/console/health/readiness", addr)
	for time.Now().Before(deadline) {
		if rsp, err := http.Get(url); err == nil {
			rsp.Body.Close()
			if rsp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(time.Second)
	}
	return false
}

// TestSmokeNacosRegistry exercises the plugin against a real nacos server:
// register → GetService → ListServices → Watch → Deregister.
func TestSmokeNacosRegistry(t *testing.T) {
	addr := nacosAddr()
	if _, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		t.Skipf("nacos not reachable at %s: %v", addr, err)
	}
	if !waitReady(t, addr, 90*time.Second) {
		t.Skipf("nacos at %s never became ready", addr)
	}

	r := NewRegistry(registry.Addrs(addr))

	svc := &registry.Service{
		Name:    "smoke.nacos",
		Version: "v1",
		Nodes: []*registry.Node{{
			Id:       "smoke-node-1",
			Address:  "127.0.0.1:9999",
			Metadata: map[string]string{"env": "smoke"},
		}},
	}

	// register
	if err := r.Register(svc); err != nil {
		t.Fatalf("register: %v", err)
	}

	// getservice (allow a moment for propagation)
	var got []*registry.Service
	var err error
	for i := 0; i < 20; i++ {
		got, err = r.GetService("smoke.nacos")
		if err == nil && len(got) > 0 && len(got[0].Nodes) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil || len(got) == 0 || len(got[0].Nodes) == 0 {
		t.Fatalf("getservice: got=%v err=%v", got, err)
	}
	if got[0].Version != "v1" {
		t.Fatalf("version metadata lost: %+v", got[0])
	}
	if host, _, _ := net.SplitHostPort(got[0].Nodes[0].Address); host != "127.0.0.1" {
		t.Fatalf("unexpected node address: %s", got[0].Nodes[0].Address)
	}

	// listservices
	list, err := r.ListServices()
	if err != nil {
		t.Fatalf("listservices: %v", err)
	}
	found := false
	for _, s := range list {
		if s.Name == "smoke.nacos" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("smoke.nacos missing from list of %d services", len(list))
	}

	// watch: subscribe, then trigger a change by registering a second node
	w, err := r.Watch(registry.WatchService("smoke.nacos"))
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Stop()

	svc2 := &registry.Service{
		Name:    "smoke.nacos",
		Version: "v1",
		Nodes:   []*registry.Node{{Id: "smoke-node-2", Address: "127.0.0.1:9998"}},
	}
	if err := r.Register(svc2); err != nil {
		t.Fatalf("register node2: %v", err)
	}

	type res struct {
		r   *registry.Result
		err error
	}
	ch := make(chan res, 1)
	go func() {
		result, werr := w.Next()
		ch <- res{result, werr}
	}()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("watch next: %v", out.err)
		}
		if out.r.Service.Name != "smoke.nacos" {
			t.Fatalf("watch got wrong service: %+v", out.r.Service)
		}
		t.Logf("watch event: action=%s nodes=%d", out.r.Action, len(out.r.Service.Nodes))
	case <-time.After(30 * time.Second):
		t.Fatal("no watch event within 30s")
	}

	// deregister both nodes; service should disappear (or lose all nodes)
	if err := r.Deregister(svc); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	if err := r.Deregister(svc2); err != nil {
		t.Fatalf("deregister node2: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got, err = r.GetService("smoke.nacos")
		if err == registry.ErrNotFound || (err == nil && (len(got) == 0 || len(got[0].Nodes) == 0)) {
			return // clean
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("service still visible after deregister: %v err=%v", got, err)
}

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
