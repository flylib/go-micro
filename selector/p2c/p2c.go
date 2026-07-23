// Package p2c implements power-of-two-choices load balancing with EWMA
// latency feedback: each pick samples two random nodes and routes to the
// one with the lower load (ewma_latency × (inflight+1)).
//
// Wire BOTH pieces — the strategy picks, the call wrapper feeds it latency:
//
//	client.NewClient(
//	    client.Selector(selector.NewSelector(selector.SetStrategy(p2c.Strategy))),
//	    client.WrapCall(p2c.NewCallWrapper()),
//	)
package p2c

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
)

// tau is the EWMA decay window — observations older than this have little
// weight (same constant as go-zero/finagle).
const tau = int64(600 * time.Millisecond)

// penalty is the assumed load of a node we have no data for yet; high so
// unknown nodes are tried once but don't monopolize traffic.
const penalty = int64(time.Second * 10)

type nodeStat struct {
	lag      atomic.Int64 // EWMA latency (ns)
	inflight atomic.Int64
	stamp    atomic.Int64 // last observation (ns since epoch)
}

var stats sync.Map // address -> *nodeStat

func stat(addr string) *nodeStat {
	if v, ok := stats.Load(addr); ok {
		return v.(*nodeStat)
	}
	v, _ := stats.LoadOrStore(addr, &nodeStat{})
	return v.(*nodeStat)
}

// load = ewma_latency × (inflight + 1), with time-decayed lag.
func load(addr string, now int64) int64 {
	s := stat(addr)
	lag := s.lag.Load()
	if lag == 0 {
		lag = penalty
	} else {
		// decay stale observations toward zero so a once-slow node recovers
		elapsed := now - s.stamp.Load()
		if elapsed > 0 {
			w := math.Exp(float64(-elapsed) / float64(tau))
			lag = int64(float64(lag) * w)
			if lag <= 0 {
				lag = 1
			}
		}
	}
	return lag * (s.inflight.Load() + 1)
}

// Strategy is a selector.Strategy implementing P2C over all nodes.
func Strategy(services []*registry.Service) selector.Next {
	var nodes []*registry.Node
	for _, svc := range services {
		nodes = append(nodes, svc.Nodes...)
	}

	return func() (*registry.Node, error) {
		if len(nodes) == 0 {
			return nil, selector.ErrNoneAvailable
		}
		if len(nodes) == 1 {
			return nodes[0], nil
		}
		a := rand.Intn(len(nodes))
		b := rand.Intn(len(nodes) - 1)
		if b >= a {
			b++
		}
		now := time.Now().UnixNano()
		if load(nodes[b].Address, now) < load(nodes[a].Address, now) {
			a = b
		}
		return nodes[a], nil
	}
}

// NewCallWrapper returns the client.CallWrapper that feeds per-node latency
// and inflight counts back into the strategy.
func NewCallWrapper() client.CallWrapper {
	return func(next client.CallFunc) client.CallFunc {
		return func(ctx context.Context, node *registry.Node, req client.Request, rsp interface{}, opts client.CallOptions) error {
			s := stat(node.Address)
			s.inflight.Add(1)
			start := time.Now()
			err := next(ctx, node, req, rsp, opts)
			observe(s, time.Since(start).Nanoseconds(), start.UnixNano())
			s.inflight.Add(-1)
			return err
		}
	}
}

// observe folds one latency sample into the node's EWMA.
func observe(s *nodeStat, rt, now int64) {
	old := s.lag.Load()
	if old == 0 {
		s.lag.Store(rt)
	} else {
		elapsed := now - s.stamp.Load()
		w := math.Exp(float64(-elapsed) / float64(tau))
		s.lag.Store(int64(float64(old)*w + float64(rt)*(1-w)))
	}
	s.stamp.Store(now)
}
