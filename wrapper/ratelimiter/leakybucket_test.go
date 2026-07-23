package ratelimiter

import (
	"context"
	"testing"
	"time"

	"github.com/flylib/go-micro/server"
)

// TestLeakyBucketSmoothing verifies calls are paced to ~1/rps intervals
// rather than admitted as a burst.
func TestLeakyBucketSmoothing(t *testing.T) {
	const rps = 100 // 10ms per slot
	h := NewLeakyBucketHandlerWrapper(rps)(func(ctx context.Context, req server.Request, rsp interface{}) error {
		return nil
	})
	ctx := context.Background()

	start := time.Now()
	const n = 10
	for i := 0; i < n; i++ {
		if err := h(ctx, stubReq{}, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	// 10 calls at 100rps ≈ 90ms of spacing after the first; allow scheduling
	// jitter but a burst (~0ms) must be impossible
	if elapsed < 70*time.Millisecond {
		t.Fatalf("calls were not smoothed: %d calls in %v", n, elapsed)
	}
	t.Logf("%d calls paced over %v (~%v/call)", n, elapsed, elapsed/time.Duration(n))
}
