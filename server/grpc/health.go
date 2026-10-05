package grpc

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/flylib/go-micro/health"
	"github.com/flylib/go-micro/server"
)

// The standard gRPC health service (grpc.health.v1.Health), served by
// every gRPC server unless DisableHealth is set, so Kubernetes gRPC
// probes, load balancers and monitoring tools can check a service:
//
//	""  or the service name   readiness: SERVING while every critical
//	                          check registered with the health package
//	                          passes; NOT_SERVING once the server stops
//	"liveness"                SERVING while the process runs
//	anything else             NOT_FOUND (Watch: SERVICE_UNKNOWN)
const (
	// LivenessService is the health service name that reports liveness only.
	LivenessService = "liveness"

	healthCacheTTL = time.Second // results reused for probes this close together
)

// watchInterval is how often Watch re-evaluates; a variable for tests.
var watchInterval = 5 * time.Second

type disableHealthKey struct{}

// DisableHealth stops the server from serving grpc.health.v1.Health.
func DisableHealth() server.Option {
	return setServerOption(disableHealthKey{}, true)
}

type healthServer struct {
	healthpb.UnimplementedHealthServer
	g *grpcServer

	mu      sync.Mutex
	checked time.Time
	ready   bool
}

// registerHealth adds the health service to srv unless it is disabled
// or the application registered its own.
func (g *grpcServer) registerHealth(srv *grpc.Server) {
	if g.opts.Context != nil {
		if off, _ := g.opts.Context.Value(disableHealthKey{}).(bool); off {
			return
		}
	}
	if _, ok := srv.GetServiceInfo()[healthpb.Health_ServiceDesc.ServiceName]; ok {
		return
	}
	healthpb.RegisterHealthServer(srv, &healthServer{g: g})
}

// readiness runs the health package's checks, at most once per
// healthCacheTTL.
func (h *healthServer) readiness(ctx context.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.checked.IsZero() && time.Since(h.checked) < healthCacheTTL {
		return h.ready
	}
	h.ready = health.IsReady(ctx)
	h.checked = time.Now()
	return h.ready
}

// status answers for one service name; ok is false for unknown names.
func (h *healthServer) status(ctx context.Context, service string) (healthpb.HealthCheckResponse_ServingStatus, bool) {
	switch service {
	case LivenessService:
		return healthpb.HealthCheckResponse_SERVING, true
	case "", h.g.serviceName():
		if h.g.isStopping() || !h.readiness(ctx) {
			return healthpb.HealthCheckResponse_NOT_SERVING, true
		}
		return healthpb.HealthCheckResponse_SERVING, true
	}
	return healthpb.HealthCheckResponse_SERVICE_UNKNOWN, false
}

func (h *healthServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	st, ok := h.status(ctx, req.GetService())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unknown service %q", req.GetService())
	}
	return &healthpb.HealthCheckResponse{Status: st}, nil
}

// Watch sends the status now and whenever it changes.
func (h *healthServer) Watch(req *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	last := healthpb.HealthCheckResponse_ServingStatus(-1)
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		st, _ := h.status(stream.Context(), req.GetService())
		if st != last {
			if err := stream.Send(&healthpb.HealthCheckResponse{Status: st}); err != nil {
				return err
			}
			last = st
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-t.C:
		}
	}
}

func (g *grpcServer) serviceName() string {
	g.RLock()
	defer g.RUnlock()
	return g.opts.Name
}

func (g *grpcServer) isStopping() bool { return g.stopping.Load() }
