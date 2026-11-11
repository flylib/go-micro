package grpc

import (
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/flylib/go-micro/health"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

// During the drain delay the node is deregistered and reports
// NOT_SERVING, but still answers; afterwards it is gone.
func TestDrainDelay(t *testing.T) {
	health.Reset()
	reg := registry.NewMemoryRegistry()
	srv, c := startHealthServer(t, server.Registry(reg), DrainDelay(600*time.Millisecond))
	if svcs, err := reg.GetService("users"); err != nil || len(svcs) == 0 {
		t.Fatalf("not registered: %v", err)
	}

	start := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Stop() }()

	time.Sleep(200 * time.Millisecond) // inside the drain delay
	if svcs, _ := reg.GetService("users"); len(svcs) != 0 {
		t.Fatal("still registered while draining")
	}
	st, err := check(t, c, "")
	if err != nil || st != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("while draining: status %v, err %v; want NOT_SERVING from a reachable server", st, err)
	}
	if st, err := check(t, c, LivenessService); err != nil || st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("liveness while draining: %v %v", st, err)
	}

	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 600*time.Millisecond {
		t.Fatalf("stopped after %v, before the drain delay", d)
	}
	if _, err := check(t, c, ""); err == nil {
		t.Fatal("still answering after Stop")
	}
}

func TestNoDrainDelayByDefault(t *testing.T) {
	health.Reset()
	srv, _ := startHealthServer(t)
	start := time.Now()
	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("Stop took %v without a drain delay", d)
	}
}
