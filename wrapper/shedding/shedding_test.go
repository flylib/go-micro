package shedding

import (
	"testing"
	"time"
)

// TestShedsUnderCpuPressure drives the shedder with a fake 100% CPU and
// more inflight than measured capacity — excess must be dropped.
func TestShedsUnderCpuPressure(t *testing.T) {
	s := NewShedder(WithCpuUsageFunc(func() int64 { return 1000 }))

	// establish capacity: ~1 pass per bucket at ~1ms rt
	for i := 0; i < 50; i++ {
		p, err := s.Allow()
		if err == nil {
			time.Sleep(time.Millisecond)
			p.Pass()
		}
	}

	// now hold slots open (never resolve) to push flying over capacity
	var held []Promise
	dropped := 0
	for i := 0; i < 200; i++ {
		p, err := s.Allow()
		if err != nil {
			dropped++
			continue
		}
		held = append(held, p)
	}
	if dropped == 0 {
		t.Fatal("expected drops under cpu pressure with saturated inflight")
	}
	for _, p := range held {
		p.Fail()
	}
	t.Logf("dropped %d/200 while overloaded (capacity maxFlight=%d)", dropped, s.maxFlight())
}

// TestNoSheddingWhenHealthy: low CPU → never drop regardless of load.
func TestNoSheddingWhenHealthy(t *testing.T) {
	s := NewShedder(WithCpuUsageFunc(func() int64 { return 100 })) // 10%
	for i := 0; i < 500; i++ {
		p, err := s.Allow()
		if err != nil {
			t.Fatalf("healthy system must not shed (i=%d)", i)
		}
		p.Pass()
	}
}
