package smoke

import (
	"testing"
	"time"

	microsync "github.com/flylib/go-micro/sync"
	setcd "github.com/flylib/go-micro/sync/etcd"
)

// TestSmokeEtcdSync exercises distributed lock + leader election against
// a real etcd (podman etcd-smoke). Skips when unreachable.
func TestSmokeEtcdSync(t *testing.T) {
	reachable(t, "127.0.0.1:2379")

	s1 := setcd.NewSync(microsync.Nodes("127.0.0.1:2379"), microsync.Prefix("/smoke"))
	s2 := setcd.NewSync(microsync.Nodes("127.0.0.1:2379"), microsync.Prefix("/smoke"))

	// --- lock: mutual exclusion across two clients ---
	if err := s1.Lock("mig", microsync.LockTTL(10*time.Second)); err != nil {
		t.Fatalf("s1 lock: %v", err)
	}
	if err := s2.Lock("mig", microsync.LockWait(2*time.Second)); err != microsync.ErrLockTimeout {
		t.Fatalf("s2 should time out while s1 holds, got %v", err)
	}
	if err := s1.Unlock("mig"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := s2.Lock("mig", microsync.LockWait(5*time.Second)); err != nil {
		t.Fatalf("s2 lock after release: %v", err)
	}
	_ = s2.Unlock("mig")

	// --- leader election: only one leader; resign hands over ---
	l1, err := s1.Leader("scheduler")
	if err != nil {
		t.Fatalf("l1 campaign: %v", err)
	}
	elected := make(chan struct{})
	go func() {
		l2, err := s2.Leader("scheduler")
		if err == nil {
			close(elected)
			_ = l2.Resign()
		}
	}()
	select {
	case <-elected:
		t.Fatal("second leader elected while first holds")
	case <-time.After(2 * time.Second):
	}
	if err := l1.Resign(); err != nil {
		t.Fatalf("resign: %v", err)
	}
	select {
	case <-elected:
	case <-time.After(10 * time.Second):
		t.Fatal("no takeover after resign")
	}
	// l1's status channel must be closed after resign
	select {
	case <-l1.Status():
	default:
		t.Fatal("l1 status should signal lost leadership")
	}
}
