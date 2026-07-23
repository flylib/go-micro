// Package nacos provides a nacos config-center source: reads one dataId
// and hot-reloads on change via ListenConfig.
package nacos

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/flylib/go-micro/config/source"
)

type nacosSource struct {
	client config_client.IConfigClient
	opts   source.Options
	dataId string
	group  string
	format string
}

// NewSource returns a nacos-backed config source.
//
//	src := nacos.NewSource(
//	    nacos.WithAddress("127.0.0.1:8848"),
//	    nacos.WithDataId("orders.yaml"), nacos.WithGroup("DEFAULT_GROUP"),
//	    nacos.WithNamespaceId("dev"),
//	)
func NewSource(opts ...source.Option) source.Source {
	options := source.NewOptions(opts...)
	s := &nacosSource{opts: options, group: "DEFAULT_GROUP", format: "yaml"}

	var addrs []string
	var namespace string
	if options.Context != nil {
		if v, ok := options.Context.Value(addressKey{}).([]string); ok {
			addrs = v
		}
		if v, ok := options.Context.Value(dataIdKey{}).(string); ok {
			s.dataId = v
		}
		if v, ok := options.Context.Value(groupKey{}).(string); ok {
			s.group = v
		}
		if v, ok := options.Context.Value(namespaceKey{}).(string); ok {
			namespace = v
		}
		if v, ok := options.Context.Value(formatKey{}).(string); ok {
			s.format = v
		}
	}
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1:8848"}
	}

	var scs []constant.ServerConfig
	for _, addr := range addrs {
		host, portStr, err := net.SplitHostPort(addr)
		port := uint64(8848)
		if err != nil {
			host = addr
		} else if p, perr := strconv.ParseUint(portStr, 10, 64); perr == nil {
			port = p
		}
		scs = append(scs, constant.ServerConfig{IpAddr: host, Port: port})
	}
	cc := constant.ClientConfig{NamespaceId: namespace, TimeoutMs: 5000, NotLoadCacheAtStart: true, LogLevel: "warn"}
	client, err := clients.NewConfigClient(vo.NacosClientParam{ClientConfig: &cc, ServerConfigs: scs})
	if err == nil {
		s.client = client
	}
	return s
}

func (s *nacosSource) Read() (*source.ChangeSet, error) {
	if s.client == nil {
		return nil, errors.New("nacos config client not initialised")
	}
	if s.dataId == "" {
		return nil, errors.New("nacos source requires WithDataId")
	}
	data, err := s.client.GetConfig(vo.ConfigParam{DataId: s.dataId, Group: s.group})
	if err != nil {
		return nil, err
	}
	cs := &source.ChangeSet{Data: []byte(data), Format: s.format, Source: s.String(), Timestamp: time.Now()}
	cs.Checksum = cs.Sum()
	return cs, nil
}

func (s *nacosSource) Write(*source.ChangeSet) error { return nil }

func (s *nacosSource) Watch() (source.Watcher, error) {
	if s.client == nil {
		return nil, errors.New("nacos config client not initialised")
	}
	w := &watcher{s: s, ch: make(chan *source.ChangeSet, 4), exit: make(chan struct{})}
	err := s.client.ListenConfig(vo.ConfigParam{
		DataId: s.dataId, Group: s.group,
		OnChange: func(_, _, _, data string) {
			cs := &source.ChangeSet{Data: []byte(data), Format: s.format, Source: s.String(), Timestamp: time.Now()}
			cs.Checksum = cs.Sum()
			select {
			case w.ch <- cs:
			case <-w.exit:
			default:
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (s *nacosSource) String() string { return "nacos" }

type watcher struct {
	s    *nacosSource
	ch   chan *source.ChangeSet
	exit chan struct{}
}

func (w *watcher) Next() (*source.ChangeSet, error) {
	select {
	case cs := <-w.ch:
		return cs, nil
	case <-w.exit:
		return nil, errors.New("watcher stopped")
	}
}

func (w *watcher) Stop() error {
	select {
	case <-w.exit:
	default:
		close(w.exit)
		_ = w.s.client.CancelListenConfig(vo.ConfigParam{DataId: w.s.dataId, Group: w.s.group})
	}
	return nil
}

type addressKey struct{}
type dataIdKey struct{}
type groupKey struct{}
type namespaceKey struct{}
type formatKey struct{}

func set(k, v interface{}) source.Option {
	return func(o *source.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, k, v)
	}
}

// WithAddress sets nacos server addresses (host:port).
func WithAddress(addrs ...string) source.Option { return set(addressKey{}, addrs) }

// WithDataId sets the config dataId to read/watch (required).
func WithDataId(id string) source.Option { return set(dataIdKey{}, id) }

// WithGroup sets the config group (default DEFAULT_GROUP).
func WithGroup(g string) source.Option { return set(groupKey{}, g) }

// WithNamespaceId sets the nacos namespace.
func WithNamespaceId(ns string) source.Option { return set(namespaceKey{}, ns) }

// WithFormat sets the config format hint (default yaml).
func WithFormat(f string) source.Option { return set(formatKey{}, f) }
