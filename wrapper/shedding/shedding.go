// Package shedding provides adaptive load shedding, a port of go-zero's
// BBR-inspired adaptiveShedder: when CPU crosses a threshold, requests
// beyond the measured system capacity (maxPass × minRt) are dropped with
// a 503 so the service degrades instead of collapsing.
package shedding

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"

	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/server"
)

// ErrServiceOverloaded is returned (wrapped in a 503) for shed requests.
var ErrServiceOverloaded = errors.New("service overloaded")

const (
	defaultCpuThreshold = 900 // per-mille (90%)
	defaultWindow       = 5 * time.Second
	defaultBuckets      = 50
	coolOffDuration     = time.Second
	flyingBeta          = 0.9 // EWMA factor for smoothed inflight
)

type Option func(*Shedder)

// WithCpuThreshold sets the CPU per-mille (0-1000) above which shedding
// engages. Default 900.
func WithCpuThreshold(t int64) Option {
	return func(s *Shedder) { s.cpuThreshold = t }
}

// WithCpuUsageFunc overrides the CPU probe — used by tests and by hosts
// with their own cgroup-aware measurement. Must return per-mille (0-1000).
func WithCpuUsageFunc(fn func() int64) Option {
	return func(s *Shedder) { s.cpuUsage = fn }
}

// Shedder implements adaptive load shedding.
type Shedder struct {
	cpuThreshold int64
	cpuUsage     func() int64

	flying    atomic.Int64
	avgFlying atomic.Int64 // EWMA of flying, stored ×1000

	dropTime     atomic.Int64 // last drop (ns)
	droppedRecen atomic.Bool

	passCounter *rollingWindow // completed requests per bucket
	rtCounter   *rollingWindow // response times per bucket (for min avg rt)
}

// NewShedder builds a Shedder with a 5s/50-bucket sliding window.
func NewShedder(opts ...Option) *Shedder {
	s := &Shedder{
		cpuThreshold: defaultCpuThreshold,
		cpuUsage:     systemCpuUsage,
		passCounter:  newRollingWindow(defaultBuckets, defaultWindow/defaultBuckets),
		rtCounter:    newRollingWindow(defaultBuckets, defaultWindow/defaultBuckets),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Promise must be resolved by the caller once the request finishes.
type Promise interface {
	Pass() // finished successfully — record rt & throughput
	Fail() // finished with error — only release the slot
}

type promise struct {
	s     *Shedder
	start time.Time
}

func (p promise) Pass() {
	rt := time.Since(p.start).Milliseconds()
	p.s.flying.Add(-1)
	p.s.rtCounter.add(float64(rt) + 1) // +1 so sub-ms calls still count
	p.s.passCounter.add(1)
}

func (p promise) Fail() {
	p.s.flying.Add(-1)
}

// Allow admits or sheds one request.
func (s *Shedder) Allow() (Promise, error) {
	if s.shouldDrop() {
		s.dropTime.Store(time.Now().UnixNano())
		s.droppedRecen.Store(true)
		return nil, ErrServiceOverloaded
	}
	s.flying.Add(1)
	// EWMA the flying count (go-zero smooths it to avoid jitter)
	f := s.flying.Load() * 1000
	old := s.avgFlying.Load()
	s.avgFlying.Store(int64(float64(old)*flyingBeta + float64(f)*(1-flyingBeta)))
	return promise{s: s, start: time.Now()}, nil
}

func (s *Shedder) shouldDrop() bool {
	if s.systemOverloaded() || s.stillHot() {
		return s.highThru()
	}
	return false
}

func (s *Shedder) systemOverloaded() bool {
	return s.cpuUsage() >= s.cpuThreshold
}

// stillHot keeps shedding for a cool-off window after the last drop so we
// don't flap while the backlog drains.
func (s *Shedder) stillHot() bool {
	if !s.droppedRecen.Load() {
		return false
	}
	if time.Now().UnixNano()-s.dropTime.Load() < int64(coolOffDuration) {
		return true
	}
	s.droppedRecen.Store(false)
	return false
}

// highThru: current smoothed inflight exceeds the measured capacity
// maxFlight = maxPass(bucket) × minRt(ms) × buckets_per_second / 1000.
func (s *Shedder) highThru() bool {
	maxFlight := s.maxFlight()
	return s.avgFlying.Load()/1000 > maxFlight && s.flying.Load() > maxFlight
}

func (s *Shedder) maxFlight() int64 {
	bucketsPerSec := float64(time.Second) / float64(s.passCounter.interval)
	return int64(math.Max(1, s.passCounter.max()*bucketsPerSec/1000*s.rtCounter.minAvg()))
}

// NewHandlerWrapper mounts the shedder around every RPC handler.
func NewHandlerWrapper(opts ...Option) server.HandlerWrapper {
	s := NewShedder(opts...)
	return func(next server.HandlerFunc) server.HandlerFunc {
		return func(ctx context.Context, req server.Request, rsp interface{}) error {
			p, err := s.Allow()
			if err != nil {
				return merr.New(req.Service(), err.Error(), 503)
			}
			if err := next(ctx, req, rsp); err != nil {
				p.Fail()
				return err
			}
			p.Pass()
			return nil
		}
	}
}
