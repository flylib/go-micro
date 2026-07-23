package sync

import (
	gosync "sync"
	"time"
)

// NewMemorySync returns an in-process Sync — real mutual exclusion within
// one binary, for tests and single-node development.
func NewMemorySync(opts ...Option) Sync {
	m := &memorySync{locks: make(map[string]chan struct{})}
	_ = m.Init(opts...)
	return m
}

type memorySync struct {
	mu    gosync.Mutex
	locks map[string]chan struct{} // held lock -> release channel
	opts  Options
}

func (m *memorySync) Init(opts ...Option) error {
	for _, o := range opts {
		o(&m.opts)
	}
	return nil
}

func (m *memorySync) Options() Options { return m.opts }

func (m *memorySync) chanFor(id string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.locks[id]
	if !ok {
		ch = make(chan struct{}, 1)
		m.locks[id] = ch
	}
	return ch
}

func (m *memorySync) Lock(id string, opts ...LockOption) error {
	var lo LockOptions
	for _, o := range opts {
		o(&lo)
	}
	ch := m.chanFor(m.opts.Prefix + id)
	if lo.Wait > 0 {
		select {
		case ch <- struct{}{}:
			return nil
		case <-time.After(lo.Wait):
			return ErrLockTimeout
		}
	}
	ch <- struct{}{}
	return nil
}

func (m *memorySync) Unlock(id string) error {
	ch := m.chanFor(m.opts.Prefix + id)
	select {
	case <-ch:
	default:
	}
	return nil
}

func (m *memorySync) Leader(id string, _ ...LeaderOption) (Leader, error) {
	// leadership in-process == holding the lock
	if err := m.Lock("leader/" + id); err != nil {
		return nil, err
	}
	return &memoryLeader{m: m, id: id, status: make(chan bool, 1)}, nil
}

func (m *memorySync) String() string { return "memory" }

type memoryLeader struct {
	m      *memorySync
	id     string
	status chan bool
	once   gosync.Once
}

func (l *memoryLeader) Resign() error {
	l.once.Do(func() {
		_ = l.m.Unlock("leader/" + l.id)
		close(l.status)
	})
	return nil
}

func (l *memoryLeader) Status() chan bool { return l.status }
