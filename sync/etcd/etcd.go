// Package etcd provides etcd-backed distributed locks and leader
// election via clientv3/concurrency. Locks carry a session lease, so a
// crashed holder releases automatically when its lease expires.
package etcd

import (
	"context"
	"errors"
	gosync "sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	microsync "github.com/flylib/go-micro/sync"
)

// NewSync returns an etcd-backed Sync.
//
//	s := etcd.NewSync(microsync.Nodes("127.0.0.1:2379"), microsync.Prefix("/myapp"))
//	s.Lock("orders-migration")
//	defer s.Unlock("orders-migration")
func NewSync(opts ...microsync.Option) microsync.Sync {
	s := &etcdSync{locks: make(map[string]*lockHandle)}
	_ = s.Init(opts...)
	return s
}

type lockHandle struct {
	session *concurrency.Session
	mutex   *concurrency.Mutex
}

type etcdSync struct {
	client *clientv3.Client
	opts   microsync.Options
	mu     gosync.Mutex
	locks  map[string]*lockHandle
}

func (s *etcdSync) Init(opts ...microsync.Option) error {
	for _, o := range opts {
		o(&s.opts)
	}
	if s.client != nil {
		return nil
	}
	addrs := s.opts.Nodes
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1:2379"}
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: addrs, DialTimeout: 5 * time.Second})
	if err != nil {
		return err
	}
	s.client = cli
	return nil
}

func (s *etcdSync) Options() microsync.Options { return s.opts }

func (s *etcdSync) prefix(kind, id string) string {
	return s.opts.Prefix + "/micro-sync/" + kind + "/" + id
}

func (s *etcdSync) Lock(id string, opts ...microsync.LockOption) error {
	if s.client == nil {
		return errors.New("etcd sync not initialised")
	}
	var lo microsync.LockOptions
	for _, o := range opts {
		o(&lo)
	}

	ttl := int(lo.TTL.Seconds())
	if ttl <= 0 {
		ttl = 30
	}
	session, err := concurrency.NewSession(s.client, concurrency.WithTTL(ttl))
	if err != nil {
		return err
	}

	m := concurrency.NewMutex(session, s.prefix("lock", id))
	ctx := context.Background()
	if lo.Wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lo.Wait)
		defer cancel()
	}
	if err := m.Lock(ctx); err != nil {
		_ = session.Close()
		if errors.Is(err, context.DeadlineExceeded) {
			return microsync.ErrLockTimeout
		}
		return err
	}

	s.mu.Lock()
	s.locks[id] = &lockHandle{session: session, mutex: m}
	s.mu.Unlock()
	return nil
}

func (s *etcdSync) Unlock(id string) error {
	s.mu.Lock()
	h, ok := s.locks[id]
	delete(s.locks, id)
	s.mu.Unlock()
	if !ok {
		return errors.New("lock not held: " + id)
	}
	err := h.mutex.Unlock(context.Background())
	_ = h.session.Close()
	return err
}

func (s *etcdSync) Leader(id string, _ ...microsync.LeaderOption) (microsync.Leader, error) {
	if s.client == nil {
		return nil, errors.New("etcd sync not initialised")
	}
	session, err := concurrency.NewSession(s.client, concurrency.WithTTL(30))
	if err != nil {
		return nil, err
	}
	e := concurrency.NewElection(session, s.prefix("leader", id))
	if err := e.Campaign(context.Background(), "leader"); err != nil {
		_ = session.Close()
		return nil, err
	}

	l := &etcdLeader{election: e, session: session, status: make(chan bool, 1)}
	go func() {
		// session lease lost (network partition, etcd down) → leadership lost
		<-session.Done()
		l.once.Do(func() { close(l.status) })
	}()
	return l, nil
}

func (s *etcdSync) String() string { return "etcd" }

type etcdLeader struct {
	election *concurrency.Election
	session  *concurrency.Session
	status   chan bool
	once     gosync.Once
}

func (l *etcdLeader) Resign() error {
	err := l.election.Resign(context.Background())
	_ = l.session.Close()
	l.once.Do(func() { close(l.status) })
	return err
}

func (l *etcdLeader) Status() chan bool { return l.status }
