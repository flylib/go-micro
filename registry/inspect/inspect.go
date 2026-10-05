// Package inspect finds registry instances that do not answer: nodes still
// registered in Nacos, etcd or Consul whose process is gone, hung or
// unreachable ("zombies"), and nodes that answer but report themselves
// unhealthy.
//
// Every round it lists the registry and probes each node:
//
//	gRPC nodes   grpc.health.v1 Check: SERVING is ok, NOT_SERVING is
//	             unhealthy, no connection is unreachable. A server without
//	             the health service counts as ok when it accepts the call.
//	other nodes  a TCP connection: accepted is ok, else unreachable.
//
// A node becomes unreachable or unhealthy after Threshold bad rounds in a
// row, and ok again after one good round; each change is an Event for the
// Notifier. The inspector only reports: it never deregisters a node.
package inspect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/registry"
)

// State of a node.
type State string

const (
	StateOK          State = "ok"
	StateUnhealthy   State = "unhealthy"   // answers, but its health check fails
	StateUnreachable State = "unreachable" // registered, but does not answer
	StateGone        State = "deregistered"
)

// Node is what the inspector knows about one registered node.
type Node struct {
	Service  string    `json:"service"`
	Version  string    `json:"version"`
	ID       string    `json:"id"`
	Address  string    `json:"address"`
	Protocol string    `json:"protocol"`
	State    State     `json:"state"`
	Since    time.Time `json:"since"`             // when State began
	Failures int       `json:"failures"`          // bad rounds in a row
	Error    string    `json:"error,omitempty"`   // last probe error
	Checked  time.Time `json:"checked"`           // last probe
	Latency  string    `json:"latency,omitempty"` // last probe duration
}

// Event is a change of a node's state.
type Event struct {
	Node Node      `json:"node"`
	From State     `json:"from"`
	To   State     `json:"to"`
	Time time.Time `json:"time"`
}

// Notifier receives state changes, for example a Webhook.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

// Options configure an Inspector.
type Options struct {
	Interval  time.Duration // time between rounds (default 30s)
	Timeout   time.Duration // per probe (default 3s)
	Threshold int           // bad rounds before a node is reported (default 3)
	Services  []string      // only these services (default all)
	Notifier  Notifier
	Logger    logger.Logger
}

// Inspector probes the nodes of a registry.
type Inspector struct {
	reg  registry.Registry
	opts Options

	mu    sync.RWMutex
	nodes map[string]*Node // by service + node id
}

// New returns an Inspector of reg.
func New(reg registry.Registry, opts Options) *Inspector {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}
	if opts.Threshold <= 0 {
		opts.Threshold = 3
	}
	if opts.Logger == nil {
		opts.Logger = logger.DefaultLogger
	}
	return &Inspector{reg: reg, opts: opts, nodes: map[string]*Node{}}
}

// Run inspects every Interval until ctx ends.
func (i *Inspector) Run(ctx context.Context) {
	t := time.NewTicker(i.opts.Interval)
	defer t.Stop()
	for {
		if _, err := i.Round(ctx); err != nil {
			i.opts.Logger.Logf(logger.WarnLevel, "inspect: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Nodes returns the current state of every node, sorted by service and id.
func (i *Inspector) Nodes() []Node {
	i.mu.RLock()
	out := make([]Node, 0, len(i.nodes))
	for _, n := range i.nodes {
		out = append(out, *n)
	}
	i.mu.RUnlock()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Service != out[b].Service {
			return out[a].Service < out[b].Service
		}
		return out[a].ID < out[b].ID
	})
	return out
}

type target struct {
	service, version string
	node             *registry.Node
}

// Round lists the registry, probes every node once and returns the state
// changes, which have also been sent to the Notifier.
func (i *Inspector) Round(ctx context.Context) ([]Event, error) {
	targets, err := i.list()
	if err != nil {
		return nil, err
	}

	type result struct {
		t       target
		state   State
		err     error
		latency time.Duration
	}
	results := make([]result, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for k, t := range targets {
		wg.Add(1)
		go func(k int, t target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			st, perr := i.probe(ctx, t.node)
			results[k] = result{t, st, perr, time.Since(start)}
		}(k, t)
	}
	wg.Wait()

	now := time.Now()
	var events []Event
	seen := map[string]bool{}
	i.mu.Lock()
	for _, r := range results {
		key := r.t.service + "/" + r.t.node.Id
		seen[key] = true
		n, ok := i.nodes[key]
		if !ok {
			n = &Node{Service: r.t.service, ID: r.t.node.Id, State: StateOK, Since: now}
			i.nodes[key] = n
		}
		n.Version, n.Address, n.Protocol = r.t.version, r.t.node.Address, r.t.node.Metadata["protocol"]
		n.Checked, n.Latency = now, r.latency.Round(time.Microsecond).String()
		n.Error = ""
		if r.err != nil {
			n.Error = r.err.Error()
		}
		if r.state == StateOK {
			n.Failures = 0
			if n.State != StateOK {
				events = append(events, Event{Node: *n, From: n.State, To: StateOK, Time: now})
				n.State, n.Since = StateOK, now
			}
			continue
		}
		n.Failures++
		if n.Failures >= i.opts.Threshold && n.State != r.state {
			events = append(events, Event{Node: *n, From: n.State, To: r.state, Time: now})
			n.State, n.Since = r.state, now
		}
	}
	// nodes that left the registry: report the ones that had been reported
	for key, n := range i.nodes {
		if seen[key] {
			continue
		}
		if n.State != StateOK {
			events = append(events, Event{Node: *n, From: n.State, To: StateGone, Time: now})
		}
		delete(i.nodes, key)
	}
	i.mu.Unlock()

	for _, e := range events {
		i.opts.Logger.Logf(logger.WarnLevel, "inspect: %s %s (%s) %s -> %s %s", e.Node.Service, e.Node.ID, e.Node.Address, e.From, e.To, e.Node.Error)
		if i.opts.Notifier != nil {
			if err := i.opts.Notifier.Notify(ctx, e); err != nil {
				i.opts.Logger.Logf(logger.ErrorLevel, "inspect: notify: %v", err)
			}
		}
	}
	return events, nil
}

func (i *Inspector) list() ([]target, error) {
	names := i.opts.Services
	if len(names) == 0 {
		svcs, err := i.reg.ListServices()
		if err != nil {
			return nil, fmt.Errorf("list services: %w", err)
		}
		for _, s := range svcs {
			names = append(names, s.Name)
		}
	}
	var out []target
	done := map[string]bool{}
	for _, name := range names {
		if done[name] {
			continue
		}
		done[name] = true
		svcs, err := i.reg.GetService(name)
		if err != nil && !errors.Is(err, registry.ErrNotFound) {
			return nil, fmt.Errorf("get service %s: %w", name, err)
		}
		for _, s := range svcs {
			for _, n := range s.Nodes {
				out = append(out, target{service: s.Name, version: s.Version, node: n})
			}
		}
	}
	return out, nil
}

// probe checks one node.
func (i *Inspector) probe(ctx context.Context, n *registry.Node) (State, error) {
	ctx, cancel := context.WithTimeout(ctx, i.opts.Timeout)
	defer cancel()
	if n.Metadata["protocol"] != "grpc" {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", n.Address)
		if err != nil {
			return StateUnreachable, err
		}
		c.Close()
		return StateOK, nil
	}

	conn, err := grpc.NewClient(n.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return StateUnreachable, err
	}
	defer conn.Close()
	rsp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	switch {
	case err == nil && rsp.GetStatus() == healthpb.HealthCheckResponse_SERVING:
		return StateOK, nil
	case err == nil:
		return StateUnhealthy, fmt.Errorf("health %s", rsp.GetStatus())
	}
	switch status.Code(err) {
	case codes.Unimplemented, codes.NotFound, codes.Unknown, codes.Internal:
		// the server answered, it just has no health service
		return StateOK, nil
	}
	return StateUnreachable, err
}
