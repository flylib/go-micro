package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
)

func pickerFor(t *testing.T, addrs ...string) *NodePicker {
	t.Helper()
	reg := registry.NewMemoryRegistry()
	var nodes []*registry.Node
	for _, a := range addrs {
		nodes = append(nodes, &registry.Node{Id: a, Address: a})
	}
	if err := reg.Register(&registry.Service{Name: "svc", Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	sel := selector.NewSelector(selector.Registry(reg))
	next, err := sel.Select("svc")
	if err != nil {
		t.Fatal(err)
	}
	return NewNodePicker(next, func(so ...selector.SelectOption) (selector.Next, error) { return sel.Select("svc", so...) })
}

func TestNodePickerNeverRepeatsAFailedNode(t *testing.T) {
	p := pickerFor(t, "a:1", "b:1", "c:1")
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		n, err := p.Next()
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if seen[n.Address] {
			t.Fatalf("attempt %d chose %s again", i, n.Address)
		}
		seen[n.Address] = true
		p.Failed(n)
	}
	if _, err := p.Next(); !errors.Is(err, ErrNoNodeLeft) {
		t.Fatalf("after every node failed: %v", err)
	}
}

// A fixed address (proxy) ignores selector filters: it must not be retried.
func TestNodePickerFixedAddress(t *testing.T) {
	fixed := func() (*registry.Node, error) { return &registry.Node{Address: "proxy:1"}, nil }
	p := NewNodePicker(fixed, func(...selector.SelectOption) (selector.Next, error) { return fixed, nil })
	n, _ := p.Next()
	p.Failed(n)
	if _, err := p.Next(); !errors.Is(err, ErrNoNodeLeft) {
		t.Fatalf("fixed address retried: %v", err)
	}
}

func TestRetryPolicies(t *testing.T) {
	conn := ConnectError("10.0.0.1:9001", errors.New("refused"))
	if !IsConnectError(conn) || IsConnectError(errors.New("plain")) || IsConnectError(nil) {
		t.Fatal("IsConnectError")
	}
	timeout := errors.New(`{"id":"svc","code":408,"detail":"slow","status":"Request Timeout"}`)
	for _, tc := range []struct {
		name   string
		policy RetryFunc
		err    error
		want   bool
	}{
		{"default retries a connect failure", RetryOnConnect, conn, true},
		{"default does not retry a timeout", RetryOnConnect, timeout, false},
		{"RetryOnError retries a connect failure", RetryOnError, conn, true},
		{"RetryOnError retries a timeout", RetryOnError, timeout, true},
		{"RetryOnError does not retry 500", RetryOnError, errors.New(`{"id":"svc","code":500}`), false},
	} {
		if got, _ := tc.policy(context.Background(), nil, 0, tc.err); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	if NewOptions().CallOptions.Retry == nil || !func() bool { r, _ := NewOptions().CallOptions.Retry(context.Background(), nil, 0, conn); return r }() {
		t.Fatal("the default policy does not retry connect failures")
	}
}

// The mucp client turns a failed dial into a connect error and fails once
// every node has been tried.
func TestRPCClientConnectFailure(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	if err := reg.Register(&registry.Service{Name: "down", Nodes: []*registry.Node{
		{Id: "d1", Address: "127.0.0.1:1"}, {Id: "d2", Address: "127.0.0.1:2"},
	}}); err != nil {
		t.Fatal(err)
	}
	c := NewClient(Registry(reg), Selector(selector.NewSelector(selector.Registry(reg))))
	var tried []string
	wrap := func(fn CallFunc) CallFunc {
		return func(ctx context.Context, node *registry.Node, req Request, rsp interface{}, opts CallOptions) error {
			tried = append(tried, node.Address)
			return fn(ctx, node, req, rsp, opts)
		}
	}
	start := time.Now()
	err := c.Call(context.Background(), c.NewRequest("down", "X.Y", map[string]string{}), &map[string]string{}, WithCallWrapper(wrap))
	if !IsConnectError(err) {
		t.Fatalf("want a connect error, got %v", err)
	}
	if len(tried) != 2 || tried[0] == tried[1] {
		t.Fatalf("tried %v, want each node once", tried)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
}
