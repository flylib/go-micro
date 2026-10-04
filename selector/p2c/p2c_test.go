package p2c

import (
	"testing"
	"time"

	"github.com/flylib/go-micro/registry"
)

func TestP2CPrefersLowLoad(t *testing.T) {
	fast, slow := "10.0.0.1:1", "10.0.0.2:1"
	now := time.Now().UnixNano()

	sf := stat(fast)
	sf.lag.Store(int64(5 * time.Millisecond))
	sf.stamp.Store(now)

	ss := stat(slow)
	ss.lag.Store(int64(500 * time.Millisecond))
	ss.inflight.Store(10)
	ss.stamp.Store(now)

	next := Strategy([]*registry.Service{{
		Name:  "svc",
		Nodes: []*registry.Node{{Id: "a", Address: fast}, {Id: "b", Address: slow}},
	}})

	fastPicks := 0
	for i := 0; i < 200; i++ {
		n, err := next()
		if err != nil {
			t.Fatal(err)
		}
		if n.Address == fast {
			fastPicks++
		}
	}
	// P2C with 2 nodes compares them every time → the low-load node must
	// win essentially always
	if fastPicks < 190 {
		t.Fatalf("expected fast node to dominate, got %d/200", fastPicks)
	}
}

func TestObserveUpdatesEwma(t *testing.T) {
	s := stat("10.0.0.9:1")
	start := time.Now().UnixNano()
	observe(s, int64(10*time.Millisecond), start)
	observe(s, int64(30*time.Millisecond), start+int64(100*time.Millisecond))
	lag := s.lag.Load()
	if lag <= int64(10*time.Millisecond) || lag >= int64(30*time.Millisecond) {
		t.Fatalf("ewma should land between samples, got %v", time.Duration(lag))
	}
}

func TestTrackFeedsLoad(t *testing.T) {
	const addr = "track-test:1"
	done := Track(addr)
	if got := stat(addr).inflight.Load(); got != 1 {
		t.Fatalf("inflight during call = %d, want 1", got)
	}
	done()
	if got := stat(addr).inflight.Load(); got != 0 {
		t.Fatalf("inflight after call = %d, want 0", got)
	}
	if stat(addr).lag.Load() == 0 {
		t.Fatal("latency not observed")
	}
}
