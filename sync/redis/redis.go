// Package redis provides Redis-backed distributed locks and leader
// election. It works with Redis (standalone, Sentinel or Cluster) and with
// Redis-compatible servers such as Dragonfly and Valkey.
//
// A lock is one key set with SET NX PX to a random token. While it is
// held, a watchdog extends its lease every third of the TTL, so like the
// etcd implementation a lock lasts until Unlock while its holder lives,
// and a crashed holder's lock frees itself when the TTL runs out. Unlock
// and renewal check the token in a Lua script that touches only its
// declared key, which Dragonfly's default script mode requires.
//
// Locks live on one Redis primary: a failover that loses the key can let
// a second holder in. Guard work that must never overlap with a fencing
// token (for example a version check on write).
package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	gosync "sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/flylib/go-micro/logger"
	microsync "github.com/flylib/go-micro/sync"
)

const (
	// DefaultTTL is the lease of locks and leaders without LockTTL.
	DefaultTTL = 30 * time.Second

	minRetry  = 20 * time.Millisecond
	maxRetry  = 500 * time.Millisecond
	opTimeout = 5 * time.Second
)

// leaderTTL is the lease of leaders; a variable so tests can shorten it.
var leaderTTL = DefaultTTL

var (
	// unlock deletes the key only if it still holds our token.
	unlock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

	// renew extends the lease only if the key still holds our token.
	renew = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)
)

// NewSync returns a Redis-backed Sync. Nodes are "host:port" addresses or
// redis:// / rediss:// URLs (with password and database); several nodes
// make a Cluster client.
//
//	s := redis.NewSync(microsync.Nodes("redis://:secret@127.0.0.1:6379/0"), microsync.Prefix("/myapp"))
//	if err := s.Lock("orders-migration", microsync.LockWait(10*time.Second)); err != nil { ... }
//	defer s.Unlock("orders-migration")
func NewSync(opts ...microsync.Option) microsync.Sync {
	s := &redisSync{locks: make(map[string]*lease), own: true}
	_ = s.Init(opts...)
	return s
}

// NewSyncFromClient returns a Sync that uses an existing client, for
// options NewSync does not expose (Sentinel, TLS config, pool sizes). The
// client is not closed by the Sync.
func NewSyncFromClient(c redis.UniversalClient, opts ...microsync.Option) microsync.Sync {
	s := &redisSync{locks: make(map[string]*lease), client: c}
	_ = s.Init(opts...)
	return s
}

type redisSync struct {
	client redis.UniversalClient
	own    bool // client created by NewSync
	opts   microsync.Options

	mu    gosync.Mutex
	locks map[string]*lease
}

func (s *redisSync) Init(opts ...microsync.Option) error {
	for _, o := range opts {
		o(&s.opts)
	}
	if s.client != nil {
		return nil
	}
	c, err := NewClient(s.opts.Nodes...)
	if err != nil {
		return err
	}
	s.client = c
	return nil
}

// NewClient builds a client from node addresses: none means
// 127.0.0.1:6379, one is an address or a redis:// URL, several make a
// Cluster client.
func NewClient(nodes ...string) (redis.UniversalClient, error) {
	switch len(nodes) {
	case 0:
		return redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"}), nil
	case 1:
		return newSingle(nodes[0])
	}
	return redis.NewClusterClient(&redis.ClusterOptions{Addrs: nodes}), nil
}

func newSingle(node string) (redis.UniversalClient, error) {
	if strings.HasPrefix(node, "redis://") || strings.HasPrefix(node, "rediss://") {
		o, err := redis.ParseURL(node)
		if err != nil {
			return nil, err
		}
		return redis.NewClient(o), nil
	}
	return redis.NewClient(&redis.Options{Addr: node}), nil
}

func (s *redisSync) Options() microsync.Options { return s.opts }

func (s *redisSync) key(kind, id string) string {
	return s.opts.Prefix + "/micro-sync/" + kind + "/" + id
}

func (s *redisSync) Lock(id string, opts ...microsync.LockOption) error {
	if s.client == nil {
		return errors.New("redis sync not initialised")
	}
	var lo microsync.LockOptions
	for _, o := range opts {
		o(&lo)
	}
	ctx := context.Background()
	if lo.Wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lo.Wait)
		defer cancel()
	}
	l, err := s.acquire(ctx, s.key("lock", id), lo.TTL)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return microsync.ErrLockTimeout
		}
		return err
	}
	s.mu.Lock()
	if old, ok := s.locks[id]; ok {
		// same id locked twice in this process: keep the newest, as etcd does
		old.stop()
	}
	s.locks[id] = l
	s.mu.Unlock()
	return nil
}

func (s *redisSync) Unlock(id string) error {
	s.mu.Lock()
	l, ok := s.locks[id]
	delete(s.locks, id)
	s.mu.Unlock()
	if !ok {
		return errors.New("lock not held: " + id)
	}
	return l.release()
}

func (s *redisSync) Leader(id string, _ ...microsync.LeaderOption) (microsync.Leader, error) {
	if s.client == nil {
		return nil, errors.New("redis sync not initialised")
	}
	l, err := s.acquire(context.Background(), s.key("leader", id), leaderTTL)
	if err != nil {
		return nil, err
	}
	return &redisLeader{lease: l}, nil
}

func (s *redisSync) String() string { return "redis" }

// Close releases the locks this Sync holds and closes a client NewSync
// created. It is not part of microsync.Sync; call it through an
// interface assertion when shutting down.
func (s *redisSync) Close() error {
	s.mu.Lock()
	held := s.locks
	s.locks = make(map[string]*lease)
	s.mu.Unlock()
	for _, l := range held {
		_ = l.release()
	}
	if s.own && s.client != nil {
		return s.client.Close()
	}
	return nil
}

// acquire blocks until the key is set to a fresh token or ctx ends, then
// starts the lease watchdog.
func (s *redisSync) acquire(ctx context.Context, key string, ttl time.Duration) (*lease, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < 3*time.Millisecond {
		ttl = 3 * time.Millisecond
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	wait := minRetry
	for {
		ok, err := s.client.SetNX(ctx, key, token, ttl).Result()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if ok {
			break
		}
		t := time.NewTimer(jitter(wait))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		if wait *= 2; wait > maxRetry {
			wait = maxRetry
		}
	}
	l := &lease{
		client: s.client,
		key:    key,
		token:  token,
		ttl:    ttl,
		done:   make(chan struct{}),
		lost:   make(chan bool),
	}
	go l.keepAlive()
	return l, nil
}

// lease is one held key and its watchdog.
type lease struct {
	client redis.UniversalClient
	key    string
	token  string
	ttl    time.Duration

	stopOnce gosync.Once
	done     chan struct{} // closed by stop: the watchdog exits
	lostOnce gosync.Once
	lost     chan bool // closed when the key is no longer ours
}

// keepAlive extends the lease every ttl/3. The key is lost when it no
// longer holds our token, or when renewals keep failing until the last
// successful one would have expired.
func (l *lease) keepAlive() {
	every := l.ttl / 3
	t := time.NewTicker(every)
	defer t.Stop()
	deadline := time.Now().Add(l.ttl)
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), every)
		n, err := renew.Run(ctx, l.client, []string{l.key}, l.token, l.ttl.Milliseconds()).Int()
		cancel()
		switch {
		case err == nil && n == 1:
			deadline = time.Now().Add(l.ttl)
		case err == nil:
			logger.Logf(logger.WarnLevel, "sync/redis: lost %s: held by another owner or expired", l.key)
			l.markLost()
			return
		case time.Now().After(deadline):
			logger.Logf(logger.WarnLevel, "sync/redis: lost %s: renewal failing: %v", l.key, err)
			l.markLost()
			return
		}
	}
}

func (l *lease) markLost() { l.lostOnce.Do(func() { close(l.lost) }) }

func (l *lease) stop() { l.stopOnce.Do(func() { close(l.done) }) }

// release stops the watchdog and deletes the key if it is still ours.
func (l *lease) release() error {
	l.stop()
	defer l.markLost()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return unlock.Run(ctx, l.client, []string{l.key}, l.token).Err()
}

type redisLeader struct {
	*lease
}

func (l *redisLeader) Resign() error { return l.release() }

func (l *redisLeader) Status() chan bool { return l.lost }

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// jitter returns a duration in [d/2, d).
func jitter(d time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d/2)))
	if err != nil {
		return d
	}
	return d/2 + time.Duration(n.Int64())
}
