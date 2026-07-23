package shedding

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// Smoothed system CPU usage in per-mille, sampled in the background the
// first time a shedder needs it (250ms interval, EWMA beta 0.95 — same
// smoothing as go-zero).
var (
	cpuOnce  sync.Once
	cpuValue atomic.Int64
)

const cpuBeta = 0.95

func systemCpuUsage() int64 {
	cpuOnce.Do(func() {
		go func() {
			for {
				// Percent with an interval blocks for the sample period
				pcts, err := cpu.Percent(250*time.Millisecond, false)
				if err != nil || len(pcts) == 0 {
					time.Sleep(250 * time.Millisecond)
					continue
				}
				sample := int64(pcts[0] * 10) // % → per-mille
				old := cpuValue.Load()
				cpuValue.Store(int64(float64(old)*cpuBeta + float64(sample)*(1-cpuBeta)))
			}
		}()
	})
	return cpuValue.Load()
}
