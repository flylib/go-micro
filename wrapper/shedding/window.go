package shedding

import (
	"sync"
	"time"
)

// rollingWindow is a fixed-bucket sliding window over time.
type rollingWindow struct {
	mu       sync.Mutex
	buckets  []bucket
	size     int
	interval time.Duration
	offset   int
	lastTime time.Time
}

type bucket struct {
	sum   float64
	count int64
}

func newRollingWindow(size int, interval time.Duration) *rollingWindow {
	return &rollingWindow{
		buckets:  make([]bucket, size),
		size:     size,
		interval: interval,
		lastTime: time.Now(),
	}
}

// rotate expires buckets older than the window; callers hold mu.
func (w *rollingWindow) rotate(now time.Time) {
	elapsed := int(now.Sub(w.lastTime) / w.interval)
	if elapsed <= 0 {
		return
	}
	if elapsed > w.size {
		elapsed = w.size
	}
	for i := 0; i < elapsed; i++ {
		w.offset = (w.offset + 1) % w.size
		w.buckets[w.offset] = bucket{}
	}
	w.lastTime = w.lastTime.Add(time.Duration(elapsed) * w.interval)
}

func (w *rollingWindow) add(v float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rotate(time.Now())
	w.buckets[w.offset].sum += v
	w.buckets[w.offset].count++
}

// max returns the largest bucket sum in the window (≥1 to avoid zeroes).
func (w *rollingWindow) max() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rotate(time.Now())
	m := 1.0
	for _, b := range w.buckets {
		if b.sum > m {
			m = b.sum
		}
	}
	return m
}

// minAvg returns the smallest per-bucket average in the window — the
// observed floor response time (ms). Defaults to 1000ms with no data.
func (w *rollingWindow) minAvg() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rotate(time.Now())
	m := 1000.0
	seen := false
	for _, b := range w.buckets {
		if b.count > 0 {
			avg := b.sum / float64(b.count)
			if !seen || avg < m {
				m = avg
				seen = true
			}
		}
	}
	return m
}
