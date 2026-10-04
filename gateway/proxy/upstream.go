package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// frame is one gRPC message, forwarded without decoding (SPEC 2).
type frame struct{ data []byte }

// rawCodec moves frames as opaque bytes. Its name becomes the upstream
// content-subtype, so it carries the inbound one (proto, json, ...).
type rawCodec struct{ name string }

func (c rawCodec) Marshal(v any) ([]byte, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, errors.New("rawCodec: not a frame")
	}
	return f.data, nil
}

func (c rawCodec) Unmarshal(data []byte, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return errors.New("rawCodec: not a frame")
	}
	// grpc may reuse data after Unmarshal returns
	f.data = append(f.data[:0], data...)
	return nil
}

func (c rawCodec) Name() string { return c.name }

// connIdle is how long an unused upstream connection is kept.
const connIdle = 5 * time.Minute

// conns keeps one gRPC client connection per node address.
type conns struct {
	creds credentials.TransportCredentials

	mu sync.Mutex
	m  map[string]*upstreamConn
}

type upstreamConn struct {
	cc   *grpc.ClientConn
	used time.Time
}

func newConns(tlsConf *tls.Config) *conns {
	creds := insecure.NewCredentials()
	if tlsConf != nil {
		creds = credentials.NewTLS(tlsConf)
	}
	return &conns{creds: creds, m: map[string]*upstreamConn{}}
}

func (p *conns) get(addr string) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, ok := p.m[addr]; ok {
		u.used = time.Now()
		return u.cc, nil
	}
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(p.creds),
		// reconnect quickly once a node is back; the default backoff
		// would keep a recovered node out for up to two minutes
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 100 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 2 * time.Second},
			MinConnectTimeout: time.Second,
		}),
	)
	if err != nil {
		return nil, err
	}
	p.m[addr] = &upstreamConn{cc: cc, used: time.Now()}
	return cc, nil
}

// ready waits up to timeout for cc to be connected. A connection in
// transient failure fails at once, so the call moves to the next node
// before anything was sent (SPEC 6).
func ready(ctx context.Context, cc *grpc.ClientConn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cc.Connect()
	for {
		switch s := cc.GetState(); s {
		case connectivity.Ready:
			return nil
		case connectivity.TransientFailure, connectivity.Shutdown:
			// let the next call to this node retry the dial right away
			cc.ResetConnectBackoff()
			return errors.New("connection " + s.String())
		default:
			if !cc.WaitForStateChange(ctx, s) {
				return errors.New("connect timeout")
			}
		}
	}
}

// sweep closes connections unused for connIdle.
func (p *conns) sweep(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for addr, u := range p.m {
		if now.Sub(u.used) > connIdle {
			_ = u.cc.Close()
			delete(p.m, addr)
		}
	}
}

func (p *conns) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for addr, u := range p.m {
		_ = u.cc.Close()
		delete(p.m, addr)
	}
}
