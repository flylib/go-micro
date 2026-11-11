package client

import (
	"errors"

	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
)

// ErrNoNodeLeft is returned by NodePicker.Next when every node of the
// service has failed to connect during this call.
var ErrNoNodeLeft = errors.New("no node left to try")

// NodePicker chooses the node for each attempt of one call. A node that
// could not be connected to (Failed) is not chosen again, whatever the
// selector strategy, so a retry always goes to a node not yet tried. It
// is for Client implementations and is not safe for concurrent use.
type NodePicker struct {
	next     selector.Next
	reselect func(...selector.SelectOption) (selector.Next, error)
	failed   map[string]bool
}

// NewNodePicker returns a picker that starts with next. reselect selects
// again with extra options; it is used once a node has failed, with a
// filter that drops the failed nodes.
func NewNodePicker(next selector.Next, reselect func(...selector.SelectOption) (selector.Next, error)) *NodePicker {
	return &NodePicker{next: next, reselect: reselect}
}

// Failed records that no connection to node could be made.
func (p *NodePicker) Failed(node *registry.Node) {
	if p.failed == nil {
		p.failed = map[string]bool{}
	}
	p.failed[node.Address] = true
}

// Next returns the node for the next attempt, or ErrNoNodeLeft.
func (p *NodePicker) Next() (*registry.Node, error) {
	if len(p.failed) == 0 {
		return p.next()
	}
	next, err := p.reselect(selector.WithFilter(p.exclude))
	if err != nil {
		// nothing passes the filter: every node has been tried
		return nil, ErrNoNodeLeft
	}
	node, err := next()
	if err != nil || p.failed[node.Address] {
		// a fixed address list (proxy) ignores the filter
		return nil, ErrNoNodeLeft
	}
	return node, nil
}

// exclude drops the failed nodes, and services left without nodes.
func (p *NodePicker) exclude(services []*registry.Service) []*registry.Service {
	out := make([]*registry.Service, 0, len(services))
	for _, s := range services {
		cp := *s
		cp.Nodes = nil
		for _, n := range s.Nodes {
			if !p.failed[n.Address] {
				cp.Nodes = append(cp.Nodes, n)
			}
		}
		if len(cp.Nodes) > 0 {
			out = append(out, &cp)
		}
	}
	return out
}
