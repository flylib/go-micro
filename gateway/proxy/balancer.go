package proxy

import (
	"sort"

	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
	"github.com/flylib/go-micro/selector/p2c"
	"github.com/flylib/go-micro/selector/weighted"
)

// eligible returns the services' nodes the gateway may use (SPEC 4.3):
// gRPC nodes only — mucp nodes speak a protocol the gateway does not —
// narrowed by the route's filters, which reuse go-micro's selector filters.
func eligible(services []*registry.Service, f FilterSpec, endpoint string) []*registry.Service {
	out := make([]*registry.Service, 0, len(services))
	for _, s := range services {
		cp := *s
		cp.Nodes = nil
		for _, n := range s.Nodes {
			if n.Metadata["protocol"] == "grpc" {
				cp.Nodes = append(cp.Nodes, n)
			}
		}
		if len(cp.Nodes) > 0 {
			out = append(out, &cp)
		}
	}
	if f.Version != "" {
		out = selector.FilterVersion(f.Version)(out)
	}
	for k, v := range f.Labels {
		out = selector.FilterLabel(k, v)(out)
	}
	if f.Endpoint {
		out = selector.FilterEndpoint(endpoint)(out)
	}
	return out
}

func countNodes(services []*registry.Service) int {
	n := 0
	for _, s := range services {
		n += len(s.Nodes)
	}
	return n
}

// picker returns successive candidate nodes for one call; retries take
// the next one (SPEC 5.1). It never returns the same address twice.
func picker(rt *route, services []*registry.Service) func() *registry.Node {
	next := strategy(rt, services)
	total := countNodes(services)
	tried := map[string]bool{}
	return func() *registry.Node {
		// random strategies may repeat a node; give them a few draws
		for draws := 0; draws < 3*total && len(tried) < total; draws++ {
			n, err := next()
			if err != nil || n == nil {
				return nil
			}
			if !tried[n.Address] {
				tried[n.Address] = true
				return n
			}
		}
		return nil
	}
}

func strategy(rt *route, services []*registry.Service) selector.Next {
	if len(rt.selector.VersionWeights) > 0 {
		return weighted.VersionWeights(rt.selector.VersionWeights)(services)
	}
	switch rt.selector.Strategy {
	case "random":
		return selector.Random(services)
	case "weighted":
		return weighted.Strategy(services)
	case "p2c":
		return p2c.Strategy(services)
	default:
		return roundRobin(rt, services)
	}
}

// roundRobin rotates across calls, unlike selector.RoundRobin which starts
// at a random node each time it is built: the route keeps the position.
func roundRobin(rt *route, services []*registry.Service) selector.Next {
	var nodes []*registry.Node
	for _, s := range services {
		nodes = append(nodes, s.Nodes...)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Id < nodes[j].Id })
	start := rt.rr.Add(1)
	i := uint64(0)
	return func() (*registry.Node, error) {
		if len(nodes) == 0 {
			return nil, selector.ErrNoneAvailable
		}
		n := nodes[(start+i)%uint64(len(nodes))]
		i++
		return n, nil
	}
}
