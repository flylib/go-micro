package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	microsync "github.com/flylib/go-micro/sync"
)

// server returns the address of a Redis-compatible server: REDIS_ADDRESS
// (a real Redis, Dragonfly or Valkey) or an in-process miniredis whose
// clock follows real time, so TTLs expire as on a real server.
func server(t *testing.T) string {
	t.Helper()
	if addr := os.Getenv("REDIS_ADDRESS"); addr != "" {
		return addr
	}
	m := miniredis.RunT(t)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		last := time.Now()
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				m.FastForward(now.Sub(last))
				last = now
			}
		}
	}()
	return m.Addr()
}

// newSync returns a Sync with its own client and a unique prefix.
func newSync(t *testing.T, addr string) microsync.Sync {
	t.Helper()
	s := NewSync(microsync.Nodes(addr), microsync.Prefix("/test-"+t.Name()))
	t.Cleanup(func() { _ = s.(*redisSync).Close() })
	return s
}

func rawClient(t *testing.T, addr string) redis.UniversalClient {
	t.Helper()
	c, err := NewClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestLockExcludes(t *testing.T) {
	addr := server(t)
	a, b := newSync(t, addr), newSync(t, addr)

	if err := a.Lock("job"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := b.Lock("job", microsync.LockWait(200*time.Millisecond)); !errors.Is(err, microsync.ErrLockTimeout) {
		t.Fatalf("second holder: want ErrLockTimeout, got %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("gave up after %v, before the wait", d)
	}

	got := make(chan error, 1)
	go func() { got <- b.Lock("job", microsync.LockWait(5*time.Second)) }()
	time.Sleep(50 * time.Millisecond)
	if err := a.Unlock("job"); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	if err := b.Unlock("job"); err != nil {
		t.Fatal(err)
	}
}

func TestLockOutlivesTTLWhileHeld(t *testing.T) {
	addr := server(t)
	a, b := newSync(t, addr), newSync(t, addr)
	if err := a.Lock("job", microsync.LockTTL(300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// three TTLs later the watchdog has kept it
	if err := b.Lock("job", microsync.LockWait(time.Second)); !errors.Is(err, microsync.ErrLockTimeout) {
		t.Fatalf("lock taken over while held: %v", err)
	}
	_ = a.Unlock("job")
}

func TestCrashedHolderReleasesAfterTTL(t *testing.T) {
	addr := server(t)
	a, b := newSync(t, addr), newSync(t, addr)
	if err := a.Lock("job", microsync.LockTTL(300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// a crash: renewals stop, the key is not deleted
	ra := a.(*redisSync)
	ra.mu.Lock()
	ra.locks["job"].stop()
	ra.mu.Unlock()

	start := time.Now()
	if err := b.Lock("job", microsync.LockWait(3*time.Second)); err != nil {
		t.Fatalf("lock of a crashed holder: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v, want about the TTL", d)
	}
	_ = b.Unlock("job")
}

func TestUnlock(t *testing.T) {
	addr := server(t)
	a := newSync(t, addr)
	if err := a.Unlock("never"); err == nil {
		t.Fatal("unlock of a lock not held succeeded")
	}

	// a holder whose key now belongs to someone else must not delete it
	if err := a.Lock("job", microsync.LockTTL(time.Minute)); err != nil {
		t.Fatal(err)
	}
	c := rawClient(t, addr)
	key := a.(*redisSync).key("lock", "job")
	if err := c.Set(context.Background(), key, "someone-else", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := a.Unlock("job"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get(context.Background(), key).Result(); v != "someone-else" {
		t.Fatalf("unlock deleted another owner's lock, key = %q", v)
	}
}

func TestPrefixSeparatesLocks(t *testing.T) {
	addr := server(t)
	a := NewSync(microsync.Nodes(addr), microsync.Prefix("/app-a"))
	b := NewSync(microsync.Nodes(addr), microsync.Prefix("/app-b"))
	t.Cleanup(func() { _ = a.(*redisSync).Close(); _ = b.(*redisSync).Close() })
	if err := a.Lock("job"); err != nil {
		t.Fatal(err)
	}
	if err := b.Lock("job", microsync.LockWait(time.Second)); err != nil {
		t.Fatalf("prefixes share a lock: %v", err)
	}
}

func TestLeader(t *testing.T) {
	old := leaderTTL
	leaderTTL = 300 * time.Millisecond
	t.Cleanup(func() { leaderTTL = old })

	addr := server(t)
	a, b := newSync(t, addr), newSync(t, addr)
	la, err := a.Leader("scheduler")
	if err != nil {
		t.Fatal(err)
	}

	elected := make(chan microsync.Leader, 1)
	go func() {
		l, err := b.Leader("scheduler")
		if err != nil {
			t.Error(err)
			return
		}
		elected <- l
	}()
	select {
	case <-elected:
		t.Fatal("two leaders")
	case <-time.After(time.Second): // past three leases
	}
	select {
	case <-la.Status():
		t.Fatal("leader lost leadership while alive")
	default:
	}

	if err := la.Resign(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-la.Status(); ok {
		t.Fatal("status not closed after resign")
	}
	var lb microsync.Leader
	select {
	case lb = <-elected:
	case <-time.After(3 * time.Second):
		t.Fatal("follower not elected after resign")
	}

	// leadership taken away (key deleted, e.g. by a failover): Status closes
	c := rawClient(t, addr)
	if err := c.Del(context.Background(), b.(*redisSync).key("leader", "scheduler")).Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lb.Status():
	case <-time.After(2 * time.Second):
		t.Fatal("status not closed after the key was lost")
	}
}

func TestNewClient(t *testing.T) {
	for _, tc := range []struct {
		nodes []string
		want  string
	}{
		{nil, "*redis.Client"},
		{[]string{"10.0.0.1:6379"}, "*redis.Client"},
		{[]string{"redis://:pw@10.0.0.1:6380/2"}, "*redis.Client"},
		{[]string{"10.0.0.1:7000", "10.0.0.2:7000"}, "*redis.ClusterClient"},
	} {
		c, err := NewClient(tc.nodes...)
		if err != nil {
			t.Fatal(err)
		}
		switch c := c.(type) {
		case *redis.Client:
			if tc.want != "*redis.Client" {
				t.Fatalf("%v: got a single client", tc.nodes)
			}
			if len(tc.nodes) == 1 && tc.nodes[0][0] == 'r' {
				o := c.Options()
				if o.Addr != "10.0.0.1:6380" || o.Password != "pw" || o.DB != 2 {
					t.Fatalf("URL not applied: %+v", o)
				}
			}
		case *redis.ClusterClient:
			if tc.want != "*redis.ClusterClient" {
				t.Fatalf("%v: got a cluster client", tc.nodes)
			}
		}
		_ = c.Close()
	}
	if _, err := NewClient("redis://[bad"); err == nil {
		t.Fatal("bad URL accepted")
	}
}
