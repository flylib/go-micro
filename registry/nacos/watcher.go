package nacos

import (
	"errors"
	"net"
	"strconv"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/flylib/go-micro/registry"
)

// nacosWatcher watches a single service via a nacos subscription. Nacos
// pushes the full instance set on every change; the watcher diffs it
// against the previous push and emits one create/delete Result per node,
// grouped by version, as the etcd and consul watchers do. Emitting the
// snapshot as a single "update" would leave removed nodes in
// registry/cache, which merges updates into what it already holds.
type nacosWatcher struct {
	r       *nacosRegistry
	service string
	results chan *registry.Result
	exit    chan struct{}
	param   *vo.SubscribeParam

	mu    sync.Mutex
	known map[string]*registry.Service // instance id -> single-node service
}

func newWatcher(r *nacosRegistry, opts ...registry.WatchOption) (registry.Watcher, error) {
	var wo registry.WatchOptions
	for _, o := range opts {
		o(&wo)
	}
	if wo.Service == "" {
		return nil, errors.New("nacos registry requires a service name to watch: use registry.WatchService(name)")
	}

	w := &nacosWatcher{
		r:       r,
		service: wo.Service,
		results: make(chan *registry.Result, 64),
		exit:    make(chan struct{}),
		known:   map[string]*registry.Service{},
	}

	w.param = &vo.SubscribeParam{
		ServiceName: wo.Service,
		GroupName:   r.group,
		SubscribeCallback: func(instances []model.Instance, err error) {
			if err != nil {
				return
			}
			w.push(instances)
		},
	}

	if err := r.client.Subscribe(w.param); err != nil {
		return nil, err
	}
	return w, nil
}

// push diffs a pushed snapshot against the last one. Results are diffs,
// so none may be dropped: a slow consumer blocks the nacos callback.
func (w *nacosWatcher) push(instances []model.Instance) {
	w.mu.Lock()
	defer w.mu.Unlock()

	current := make(map[string]*registry.Service, len(instances))
	for _, in := range instances {
		// same eligibility as GetService (HealthyOnly)
		if !in.Healthy || !in.Enable {
			continue
		}
		current[in.InstanceId] = &registry.Service{
			Name:    w.service,
			Version: in.Metadata[metaVersionKey],
			Nodes: []*registry.Node{{
				Id:       in.InstanceId,
				Address:  net.JoinHostPort(in.Ip, strconv.FormatUint(in.Port, 10)),
				Metadata: in.Metadata,
			}},
		}
	}

	var out []*registry.Result
	for id, svc := range current {
		if old, ok := w.known[id]; !ok || !sameNode(old, svc) {
			out = append(out, &registry.Result{Action: "create", Service: svc})
		}
	}
	for id, svc := range w.known {
		if _, ok := current[id]; !ok {
			out = append(out, &registry.Result{Action: "delete", Service: svc})
		}
	}
	w.known = current

	for _, r := range out {
		select {
		case w.results <- r:
		case <-w.exit:
			return
		}
	}
}

func sameNode(a, b *registry.Service) bool {
	if a.Version != b.Version || a.Nodes[0].Address != b.Nodes[0].Address {
		return false
	}
	am, bm := a.Nodes[0].Metadata, b.Nodes[0].Metadata
	if len(am) != len(bm) {
		return false
	}
	for k, v := range am {
		if bm[k] != v {
			return false
		}
	}
	return true
}

func (w *nacosWatcher) Next() (*registry.Result, error) {
	select {
	case r := <-w.results:
		return r, nil
	case <-w.exit:
		return nil, errors.New("watcher stopped")
	}
}

func (w *nacosWatcher) Stop() {
	select {
	case <-w.exit:
		return
	default:
		close(w.exit)
		_ = w.r.client.Unsubscribe(w.param)
	}
}
