// Package sync provides distributed coordination primitives: locks and
// leader election. The memory implementation is for single-process use;
// distributed backends (etcd) live in submodules.
package sync

import (
	"errors"
	"time"
)

var (
	// ErrLockTimeout is returned when a lock is not acquired within Wait.
	ErrLockTimeout = errors.New("lock timeout")
)

// Sync is a distributed coordination interface.
type Sync interface {
	Init(...Option) error
	Options() Options
	// Lock acquires a named distributed lock, blocking until acquired
	// (or until LockWait elapses).
	Lock(id string, opts ...LockOption) error
	// Unlock releases a named lock.
	Unlock(id string) error
	// Leader campaigns for leadership on the named election, blocking
	// until elected. The returned Leader reports lost leadership on
	// Status and steps down with Resign.
	Leader(id string, opts ...LeaderOption) (Leader, error)
	String() string
}

// Leader is a leadership handle.
type Leader interface {
	// Resign steps down.
	Resign() error
	// Status is closed / receives when leadership is lost.
	Status() chan bool
}

type Options struct {
	Nodes  []string
	Prefix string
}

type Option func(*Options)

// Nodes sets the backend addresses.
func Nodes(addrs ...string) Option {
	return func(o *Options) { o.Nodes = addrs }
}

// Prefix namespaces all lock/election keys.
func Prefix(p string) Option {
	return func(o *Options) { o.Prefix = p }
}

type LockOptions struct {
	// TTL is the lock lease: if the holder dies the lock auto-releases
	// after TTL (backend-dependent).
	TTL time.Duration
	// Wait bounds how long Lock blocks; zero means block indefinitely.
	Wait time.Duration
}

type LockOption func(*LockOptions)

// LockTTL sets the lock lease duration.
func LockTTL(d time.Duration) LockOption {
	return func(o *LockOptions) { o.TTL = d }
}

// LockWait bounds lock acquisition; ErrLockTimeout after d.
func LockWait(d time.Duration) LockOption {
	return func(o *LockOptions) { o.Wait = d }
}

type LeaderOptions struct{}

type LeaderOption func(*LeaderOptions)
