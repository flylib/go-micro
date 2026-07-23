package sync

import (
	"testing"
	"time"
)

func TestMemoryLockMutualExclusion(t *testing.T) {
	s := NewMemorySync()
	if err := s.Lock("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock("a", LockWait(100*time.Millisecond)); err != ErrLockTimeout {
		t.Fatalf("second lock should time out, got %v", err)
	}
	if err := s.Unlock("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock("a", LockWait(100*time.Millisecond)); err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
}

func TestMemoryLeader(t *testing.T) {
	s := NewMemorySync()
	l, err := s.Leader("job")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		l2, err := s.Leader("job")
		if err == nil {
			close(acquired)
			_ = l2.Resign()
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second leader before resign")
	case <-time.After(100 * time.Millisecond):
	}
	_ = l.Resign()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second leader not elected after resign")
	}
}
