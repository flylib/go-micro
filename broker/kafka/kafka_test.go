package kafka

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flylib/go-micro/broker"
)

// kafkaAddr returns KAFKA_ADDR (default 127.0.0.1:9092), skipping when no
// broker answers there.
func kafkaAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("KAFKA_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9092"
	}
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Skipf("kafka not reachable at %s: %v", addr, err)
	}
	c.Close()
	return addr
}

func newBroker(t *testing.T, opts ...broker.Option) broker.Broker {
	t.Helper()
	b := NewBroker(append([]broker.Option{broker.Addrs(kafkaAddr(t))}, opts...)...)
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Disconnect() })
	return b
}

// warm publishes until the auto-created topic accepts writes.
func warm(t *testing.T, b broker.Broker, topic string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := b.Publish(topic, &broker.Message{Body: []byte("warm")})
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("topic %s never accepted a write: %v", topic, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// publishTimes publishes n messages one after another and returns the
// slowest.
func publishTimes(t *testing.T, b broker.Broker, topic string, n int) time.Duration {
	t.Helper()
	var slowest time.Duration
	for i := 0; i < n; i++ {
		start := time.Now()
		if err := b.Publish(topic, &broker.Message{Body: []byte(fmt.Sprintf(`{"n":%d}`, i))}); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > slowest {
			slowest = d
		}
	}
	return slowest
}

func TestPublishDoesNotWaitOutABatch(t *testing.T) {
	topic := fmt.Sprintf("micro-kafka-sync-%d", time.Now().UnixNano())
	b := newBroker(t)
	warm(t, b, topic)
	slowest := publishTimes(t, b, topic, 5)
	t.Logf("default: slowest of 5 synchronous publishes %v", slowest)
	if slowest > 250*time.Millisecond {
		t.Fatalf("a synchronous publish took %v; it waits out a batch timeout", slowest)
	}

	// kafka-go's own default, which the plugin used to keep: ~1s each
	old := newBroker(t, BatchTimeout(time.Second))
	warm(t, old, topic)
	t.Logf("BatchTimeout(1s), the old behaviour: slowest of 2 %v", publishTimes(t, old, topic, 2))
}

func TestAsyncPublish(t *testing.T) {
	topic := fmt.Sprintf("micro-kafka-async-%d", time.Now().UnixNano())
	sync := newBroker(t)
	warm(t, sync, topic)

	var got atomic.Int64
	sub, err := sync.Subscribe(topic, func(e broker.Event) error {
		if string(e.Message().Body) != "warm" {
			got.Add(1)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	time.Sleep(3 * time.Second) // the consumer group joins

	async := newBroker(t, Async(), BatchTimeout(50*time.Millisecond))
	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := async.Publish(topic, &broker.Message{Body: []byte(fmt.Sprintf(`{"n":%d}`, i))}); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	t.Logf("async: %d publishes returned in %v", n, elapsed)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("async publishes took %v", elapsed)
	}
	deadline := time.Now().Add(30 * time.Second)
	for got.Load() < n && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got.Load() < n {
		t.Fatalf("consumer got %d of %d async messages", got.Load(), n)
	}
}

func TestPublishUsesCallerContext(t *testing.T) {
	topic := fmt.Sprintf("micro-kafka-ctx-%d", time.Now().UnixNano())
	b := newBroker(t)
	warm(t, b, topic)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Publish(topic, &broker.Message{Body: []byte("{}")}, broker.PublishContext(ctx)); err == nil {
		t.Fatal("publish with a canceled context succeeded")
	}
}
