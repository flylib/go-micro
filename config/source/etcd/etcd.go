// Package etcd provides an etcd config source: reads one key and
// hot-reloads via etcd watch.
package etcd

import (
	"context"
	"errors"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/flylib/go-micro/config/source"
)

type etcdSource struct {
	client *clientv3.Client
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

// WithAddress sets etcd endpoints.
func WithAddress(addrs ...string) source.Option { return set(addressKey{}, addrs) }

// WithKey sets the key holding the config document (required).
func WithKey(k string) source.Option { return set(keyKey{}, k) }

// WithFormat sets the config format hint (default yaml).
func WithFormat(f string) source.Option { return set(formatKey{}, f) }

// NewSource returns an etcd-backed config source.
func NewSource(opts ...source.Option) source.Source {
	options := source.NewOptions(opts...)
	s := &etcdSource{format: "yaml"}
	addrs := []string{"127.0.0.1:2379"}
	if options.Context != nil {
		if v, ok := options.Context.Value(addressKey{}).([]string); ok {
			addrs = v
		}
		if v, ok := options.Context.Value(keyKey{}).(string); ok {
			s.key = v
		}
		if v, ok := options.Context.Value(formatKey{}).(string); ok {
			s.format = v
		}
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: addrs, DialTimeout: 5 * time.Second})
	if err == nil {
		s.client = cli
	}
	return s
}

func (s *etcdSource) Read() (*source.ChangeSet, error) {
	if s.client == nil {
		return nil, errors.New("etcd client not initialised")
	}
	if s.key == "" {
		return nil, errors.New("etcd source requires WithKey")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rsp, err := s.client.Get(ctx, s.key)
	if err != nil {
		return nil, err
	}
	if len(rsp.Kvs) == 0 {
		return nil, errors.New("key not found: " + s.key)
	}
	cs := &source.ChangeSet{Data: rsp.Kvs[0].Value, Format: s.format, Source: s.String(), Timestamp: time.Now()}
	cs.Checksum = cs.Sum()
	return cs, nil
}

func (s *etcdSource) Write(*source.ChangeSet) error { return nil }

func (s *etcdSource) Watch() (source.Watcher, error) {
	if s.client == nil {
		return nil, errors.New("etcd client not initialised")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &watcher{s: s, ch: s.client.Watch(ctx, s.key), cancel: cancel}, nil
}

func (s *etcdSource) String() string { return "etcd" }

type watcher struct {
	s      *etcdSource
	ch     clientv3.WatchChan
	cancel context.CancelFunc
}

func (w *watcher) Next() (*source.ChangeSet, error) {
	for rsp := range w.ch {
		if err := rsp.Err(); err != nil {
			return nil, err
		}
		for _, ev := range rsp.Events {
			if ev.Type == clientv3.EventTypePut {
				cs := &source.ChangeSet{Data: ev.Kv.Value, Format: w.s.format, Source: w.s.String(), Timestamp: time.Now()}
				cs.Checksum = cs.Sum()
				return cs, nil
			}
		}
	}
	return nil, errors.New("watcher stopped")
}

func (w *watcher) Stop() error { w.cancel(); return nil }
