// Package consul provides a consul KV config source: reads one key and
// hot-reloads via blocking queries.
package consul

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/flylib/go-micro/config/source"
)

type consulSource struct {
	client *api.Client
	key    string
	format string
}

type addressKey struct{}
type keyKey struct{}
type formatKey struct{}

func set(k, v interface{}) source.Option {
	return func(o *source.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, k, v)
	}
}

// WithAddress sets the consul address (host:port).
func WithAddress(addr string) source.Option { return set(addressKey{}, addr) }

// WithKey sets the KV key holding the config document (required).
func WithKey(k string) source.Option { return set(keyKey{}, k) }

// WithFormat sets the config format hint (default yaml).
func WithFormat(f string) source.Option { return set(formatKey{}, f) }

// NewSource returns a consul-backed config source.
func NewSource(opts ...source.Option) source.Source {
	options := source.NewOptions(opts...)
	s := &consulSource{format: "yaml"}
	cfg := api.DefaultConfig()
	if options.Context != nil {
		if v, ok := options.Context.Value(addressKey{}).(string); ok {
			cfg.Address = v
		}
		if v, ok := options.Context.Value(keyKey{}).(string); ok {
			s.key = v
		}
		if v, ok := options.Context.Value(formatKey{}).(string); ok {
			s.format = v
		}
	}
	cli, err := api.NewClient(cfg)
	if err == nil {
		s.client = cli
	}
	return s
}

func (s *consulSource) read(waitIndex uint64) (*source.ChangeSet, uint64, error) {
	kv, meta, err := s.client.KV().Get(s.key, &api.QueryOptions{WaitIndex: waitIndex, WaitTime: 55 * time.Second})
	if err != nil {
		return nil, 0, err
	}
	if kv == nil {
		return nil, meta.LastIndex, errors.New("key not found: " + s.key)
	}
	cs := &source.ChangeSet{Data: kv.Value, Format: s.format, Source: s.String(), Timestamp: time.Now()}
	cs.Checksum = cs.Sum()
	return cs, meta.LastIndex, nil
}

func (s *consulSource) Read() (*source.ChangeSet, error) {
	if s.client == nil {
		return nil, errors.New("consul client not initialised")
	}
	if s.key == "" {
		return nil, errors.New("consul source requires WithKey")
	}
	cs, _, err := s.read(0)
	return cs, err
}

func (s *consulSource) Write(*source.ChangeSet) error { return nil }

func (s *consulSource) Watch() (source.Watcher, error) {
	if s.client == nil {
		return nil, errors.New("consul client not initialised")
	}
	_, idx, _ := s.read(0)
	return &watcher{s: s, idx: idx, exit: make(chan struct{})}, nil
}

func (s *consulSource) String() string { return "consul" }

type watcher struct {
	s    *consulSource
	idx  uint64
	exit chan struct{}
}

// Next blocks on a consul blocking query until the key changes.
func (w *watcher) Next() (*source.ChangeSet, error) {
	for {
		select {
		case <-w.exit:
			return nil, errors.New("watcher stopped")
		default:
		}
		cs, idx, err := w.s.read(w.idx)
		if err != nil {
			return nil, err
		}
		if idx != w.idx {
			w.idx = idx
			return cs, nil
		}
	}
}

func (w *watcher) Stop() error {
	select {
	case <-w.exit:
	default:
		close(w.exit)
	}
	return nil
}
