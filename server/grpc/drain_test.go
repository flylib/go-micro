package grpc

import (
	"context"
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

func TestGracefulStopSettings(t *testing.T) {
	g := NewServer().(*grpcServer)
	if d := g.getGracefulStopTimeout(); d != 15*time.Second {
		t.Fatalf("default graceful stop timeout %v", d)
	}
	if d := g.getDrainDelay(); d != 0 {
		t.Fatalf("default drain delay %v", d)
	}

	// the environment applies when the code sets nothing
	t.Setenv(EnvGracefulStopTimeout, "40s")
	t.Setenv(EnvDrainDelay, "5s")
	if d := g.getGracefulStopTimeout(); d != 40*time.Second {
		t.Fatalf("graceful stop timeout from the environment: %v", d)
	}
	if d := g.getDrainDelay(); d != 5*time.Second {
		t.Fatalf("drain delay from the environment: %v", d)
	}

	// options win over the environment
	o := NewServer(GracefulStopTimeout(3*time.Second), DrainDelay(0)).(*grpcServer)
	if d := o.getGracefulStopTimeout(); d != 3*time.Second {
		t.Fatalf("option should win: %v", d)
	}
	if d := o.getDrainDelay(); d != 0 {
		t.Fatalf("DrainDelay(0) should win over the environment: %v", d)
	}

	// values that do not parse are ignored
	t.Setenv(EnvGracefulStopTimeout, "soon")
	t.Setenv(EnvDrainDelay, "-1s")
	if d := g.getGracefulStopTimeout(); d != DefaultGracefulStopTimeout {
		t.Fatalf("bad value not ignored: %v", d)
	}
	if d := g.getDrainDelay(); d != 0 {
		t.Fatalf("bad drain delay not ignored: %v", d)
	}
}

// An open health Watch must not hold Stop until the graceful stop timeout:
// the watcher is told NOT_SERVING and its stream ends.
func TestStopEndsHealthWatch(t *testing.T) {
	health.Reset()
	srv, c := startHealthServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := c.Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if rsp, err := stream.Recv(); err != nil || rsp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("first status %v, %v", rsp.GetStatus(), err)
	}

	start := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Stop() }()
	if rsp, err := stream.Recv(); err != nil || rsp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status while stopping %v, %v", rsp.GetStatus(), err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("watch stream still open")
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Stop took %v with an open health watch", d)
	}
}
