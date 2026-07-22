package nacos

import (
	"errors"
	"net"
	"strconv"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/flylib/go-micro/registry"
)

// nacosWatcher watches a single service via a nacos subscription.
// Nacos pushes the full healthy instance set on every change, so each
// callback is surfaced as one "update" Result carrying the snapshot.
type nacosWatcher struct {
	r       *nacosRegistry
	service string
	results chan *registry.Result
	exit    chan struct{}
	param   *vo.SubscribeParam
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
		results: make(chan *registry.Result, 16),
		exit:    make(chan struct{}),
	}

	w.param = &vo.SubscribeParam{
		ServiceName: wo.Service,
		GroupName:   r.group,
		SubscribeCallback: func(instances []model.Instance, err error) {
			if err != nil {
				return
			}
			svc := &registry.Service{Name: w.service}
			for _, in := range instances {
				if svc.Version == "" {
					svc.Version = in.Metadata[metaVersionKey]
				}
				svc.Nodes = append(svc.Nodes, &registry.Node{
					Id:       in.InstanceId,
					Address:  net.JoinHostPort(in.Ip, strconv.FormatUint(in.Port, 10)),
					Metadata: in.Metadata,
				})
			}
			select {
			case w.results <- &registry.Result{Action: "update", Service: svc}:
			case <-w.exit:
			default: // drop if the consumer is slow rather than block nacos callbacks
			}
		},
	}

	if err := r.client.Subscribe(w.param); err != nil {
		return nil, err
	}
	return w, nil
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
