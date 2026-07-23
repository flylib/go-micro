// Package weighted implements weight-based node selection for canary /
// gray releases. Each node's share of traffic is proportional to its
// weight, read from node metadata (key "weight", default 100), or from
// per-version weights for percentage-based canary rollout.
//
//	// nodes carry metadata weight=95 / weight=5
//	selector.NewSelector(selector.SetStrategy(weighted.Strategy))
//
//	// or: split by version — 95% v1, 5% v2 (canary)
//	selector.NewSelector(selector.SetStrategy(
//	    weighted.VersionWeights(map[string]int{"v1": 95, "v2": 5})))
//
// Pair with the built-in selector.FilterLabel / FilterVersion filters for
// label-based (染色) routing: filters prune candidates, weights split the
// remainder.
package weighted

import (
	"math/rand"
	"strconv"

	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
)

// MetadataKey is the node metadata key holding the weight.
const MetadataKey = "weight"

const defaultWeight = 100

type entry struct {
	node   *registry.Node
	weight int
}

func next(entries []entry) selector.Next {
	total := 0
	for _, e := range entries {
		total += e.weight
	}
	return func() (*registry.Node, error) {
		if len(entries) == 0 || total <= 0 {
			return nil, selector.ErrNoneAvailable
		}
		n := rand.Intn(total)
		for _, e := range entries {
			n -= e.weight
			if n < 0 {
				return e.node, nil
			}
		}
		return entries[len(entries)-1].node, nil
	}
}

// Strategy weights each node by its metadata "weight" (default 100).
func Strategy(services []*registry.Service) selector.Next {
	var entries []entry
	for _, svc := range services {
		for _, node := range svc.Nodes {
			w := defaultWeight
			if node.Metadata != nil {
				if v, err := strconv.Atoi(node.Metadata[MetadataKey]); err == nil && v >= 0 {
					w = v
				}
			}
			entries = append(entries, entry{node, w})
		}
	}
	return next(entries)
}

// VersionWeights returns a Strategy that splits traffic between service
// versions by the given weights (e.g. {"v1": 95, "v2": 5}). Versions not
// listed get weight 0 (no traffic); nodes within a version share its
// weight equally.
func VersionWeights(weights map[string]int) selector.Strategy {
	return func(services []*registry.Service) selector.Next {
		var entries []entry
		for _, svc := range services {
			w, ok := weights[svc.Version]
			if !ok || w <= 0 || len(svc.Nodes) == 0 {
				continue
			}
			per := w / len(svc.Nodes)
			if per < 1 {
				per = 1
			}
			for _, node := range svc.Nodes {
				entries = append(entries, entry{node, per})
			}
		}
		return next(entries)
	}
}
