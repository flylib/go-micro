package grpc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
	pgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	pb "google.golang.org/grpc/examples/helloworld/helloworld"
	"google.golang.org/grpc/status"
)

// countingGreeter counts calls; the name "Timeout" fails with 408.
type countingGreeter struct {
	pb.UnimplementedGreeterServer
	calls atomic.Int64
}

func (g *countingGreeter) SayHello(ctx context.Context, in *pb.HelloRequest) (*pb.HelloReply, error) {
	g.calls.Add(1)
	if in.Name == "Timeout" {
		return nil, errors.Timeout("helloworld", "too slow")
	}
	return &pb.HelloReply{Message: "Hello " + in.Name}, nil
}

// deadAddr is an address nothing listens on.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// retryClient returns a client of "helloworld" with one node per address.
func retryClient(t *testing.T, addrs []string, opts ...client.Option) client.Client {
	t.Helper()
	r := registry.NewMemoryRegistry()
	var nodes []*registry.Node
	for i, a := range addrs {
		nodes = append(nodes, &registry.Node{Id: "n" + string(rune('0'+i)), Address: a, Metadata: map[string]string{"protocol": "grpc"}})
	}
	if err := r.Register(&registry.Service{Name: "helloworld", Version: "test", Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	return NewClient(append([]client.Option{
		client.Registry(r),
		client.Selector(selector.NewSelector(selector.Registry(r))),
		client.DialTimeout(time.Second),
	}, opts...)...)
}

func liveGreeter(t *testing.T) (string, *countingGreeter) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &countingGreeter{}
	s := pgrpc.NewServer()
	pb.RegisterGreeterServer(s, g)
	go s.Serve(l)
	t.Cleanup(s.Stop)
	return l.Addr().String(), g
}

func hello(c client.Client, name string) error {
	req := c.NewRequest("helloworld", "/helloworld.Greeter/SayHello", &pb.HelloRequest{Name: name})
	return c.Call(context.Background(), req, &pb.HelloReply{})
}

// A node that cannot be reached is skipped: the call moves to another node.
func TestRetriesAnotherNodeOnConnectFailure(t *testing.T) {
	addr, g := liveGreeter(t)
	c := retryClient(t, []string{deadAddr(t), addr, deadAddr(t)})
	for i := 0; i < 12; i++ {
		if err := hello(c, "John"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := g.calls.Load(); n != 12 {
		t.Fatalf("live node served %d calls, want 12", n)
	}
}

func TestConnectErrorWhenNoNodeAnswers(t *testing.T) {
	c := retryClient(t, []string{deadAddr(t)}, client.Retries(1))
	err := hello(c, "John")
	if !client.IsConnectError(err) {
		t.Fatalf("want a connect error, got %v", err)
	}
	if e := errors.Parse(err.Error()); e.Code != 503 {
		t.Fatalf("code %d, want 503", e.Code)
	}
}

// A request that reached the service is not retried by default, even on a
// timeout: the service may have acted on it.
func TestSentRequestIsNotRetriedByDefault(t *testing.T) {
	addr, g := liveGreeter(t)
	c := retryClient(t, []string{addr})
	if err := hello(c, "Timeout"); err == nil {
		t.Fatal("want the service's error")
	}
	if n := g.calls.Load(); n != 1 {
		t.Fatalf("service called %d times, want 1", n)
	}

	// RetryOnError opts in to retrying timeouts, for idempotent endpoints
	c = retryClient(t, []string{addr}, client.Retry(client.RetryOnError), client.Retries(2))
	g.calls.Store(0)
	_ = hello(c, "Timeout")
	if n := g.calls.Load(); n != 3 {
		t.Fatalf("with RetryOnError the service was called %d times, want 3", n)
	}
}

// With every node down the call fails once each has been tried, without
// waiting out the retry backoff.
func TestFailsFastWhenEveryNodeIsDown(t *testing.T) {
	c := retryClient(t, []string{deadAddr(t), deadAddr(t), deadAddr(t)})
	start := time.Now()
	err := hello(c, "John")
	if !client.IsConnectError(err) {
		t.Fatalf("want a connect error, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v: nodes were retried or the backoff was waited out", d)
	}
}

func TestStreamRetriesAnotherNodeOnConnectFailure(t *testing.T) {
	addr, _ := liveGreeter(t)
	c := retryClient(t, []string{deadAddr(t), deadAddr(t), addr})
	for i := 0; i < 8; i++ {
		req := c.NewRequest("helloworld", "/helloworld.Greeter/SayHello", &pb.HelloRequest{}, client.StreamingRequest())
		s, err := c.Stream(context.Background(), req)
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		_ = s.Close()
	}
}

// countingListener counts accepted connections.
type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

// A call that fails with the service's error leaves the connection in the
// pool: only a broken connection is closed.
func TestPoolKeepsConnectionAfterCallErrors(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &countingListener{Listener: inner}
	s := pgrpc.NewServer()
	pb.RegisterGreeterServer(s, &countingGreeter{})
	go s.Serve(l)
	t.Cleanup(s.Stop)

	c := retryClient(t, []string{inner.Addr().String()})
	for i := 0; i < 5; i++ {
		if err := hello(c, "Timeout"); err == nil {
			t.Fatal("want the service's error")
		}
	}
	if err := hello(c, "John"); err != nil {
		t.Fatal(err)
	}
	if n := l.accepted.Load(); n != 1 {
		t.Fatalf("%d connections for 6 calls, want 1", n)
	}
}

func TestConnBroken(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.BadRequest("svc", "bad input"), false},
		{status.Error(codes.InvalidArgument, "bad"), false},
		{status.Error(codes.DeadlineExceeded, "slow"), false},
		{status.Error(codes.Unavailable, "transport is closing"), true},
		{client.ConnectError("10.0.0.1:1", errors.New("x", "refused", 0)), true},
	} {
		if got := connBroken(tc.err); got != tc.want {
			t.Errorf("connBroken(%v) = %v", tc.err, got)
		}
	}
}
