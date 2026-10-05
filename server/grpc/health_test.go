package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/flylib/go-micro/health"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

func startHealthServer(t *testing.T, opts ...server.Option) (*grpcServer, healthpb.HealthClient) {
	t.Helper()
	srv := NewServer(append([]server.Option{server.Name("users"), server.Registry(registry.NewMemoryRegistry()),
		server.Address("127.0.0.1:0")}, opts...)...).(*grpcServer)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	conn, err := grpc.NewClient(srv.Options().Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return srv, healthpb.NewHealthClient(conn)
}

func check(t *testing.T, c healthpb.HealthClient, service string) (healthpb.HealthCheckResponse_ServingStatus, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rsp, err := c.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	return rsp.GetStatus(), err
}

func TestHealthCheck(t *testing.T) {
	health.Reset()
	t.Cleanup(health.Reset)
	_, c := startHealthServer(t)

	for _, svc := range []string{"", "users", LivenessService} {
		if st, err := check(t, c, svc); err != nil || st != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("Check(%q) = %v, %v; want SERVING", svc, st, err)
		}
	}
	if _, err := check(t, c, "orders"); status.Code(err) != codes.NotFound {
		t.Fatalf("Check(unknown service): %v, want NotFound", err)
	}

	// a failing critical dependency: not ready, still live
	health.Register("db", func(context.Context) error { return errors.New("db down") })
	time.Sleep(healthCacheTTL + 100*time.Millisecond)
	if st, _ := check(t, c, ""); st != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("readiness with a failing check: %v", st)
	}
	if st, _ := check(t, c, LivenessService); st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("liveness with a failing check: %v", st)
	}
}

func TestHealthNotServingWhileStopping(t *testing.T) {
	health.Reset()
	srv, c := startHealthServer(t)
	srv.stopping.Store(true)
	time.Sleep(healthCacheTTL)
	if st, _ := check(t, c, ""); st != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("readiness while stopping: %v", st)
	}
	srv.stopping.Store(false)
}

func TestHealthWatch(t *testing.T) {
	health.Reset()
	t.Cleanup(health.Reset)
	old := watchInterval
	watchInterval = 100 * time.Millisecond
	t.Cleanup(func() { watchInterval = old })
	_, c := startHealthServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := c.Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if rsp, err := stream.Recv(); err != nil || rsp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("first watch status %v, %v", rsp.GetStatus(), err)
	}
	health.Register("cache", func(context.Context) error { return errors.New("down") })
	if rsp, err := stream.Recv(); err != nil || rsp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("watch after a failure %v, %v", rsp.GetStatus(), err)
	}

	unknown, err := c.Watch(ctx, &healthpb.HealthCheckRequest{Service: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if rsp, err := unknown.Recv(); err != nil || rsp.Status != healthpb.HealthCheckResponse_SERVICE_UNKNOWN {
		t.Fatalf("watch of an unknown service %v, %v", rsp.GetStatus(), err)
	}
}

func TestDisableHealth(t *testing.T) {
	health.Reset()
	_, c := startHealthServer(t, DisableHealth())
	if st, err := check(t, c, ""); err == nil || st == healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health served although disabled: %v, %v", st, err)
	}
}
