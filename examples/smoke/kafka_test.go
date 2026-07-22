package smoke

import (
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	kgo "github.com/segmentio/kafka-go"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/broker/kafka"
)

// kafkaAddr returns the kafka bootstrap address for integration tests.
// Override with KAFKA_ADDR. Any local kafka on 9092 works, e.g.:
//
//	podman run -d --name kafka-smoke -p 9092:9092 apache/kafka:3.9.0
func kafkaAddr() string {
	if v := os.Getenv("KAFKA_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:9092"
}

// TestSmokeKafkaBroker exercises the kafka broker against a real server:
// connect → subscribe (consumer group) → publish → receive → unsubscribe.
// Skips when no kafka is reachable.
func TestSmokeKafkaBroker(t *testing.T) {
	addr := kafkaAddr()
	if _, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		t.Skipf("kafka not reachable at %s: %v", addr, err)
	}

	b := kafka.NewBroker(broker.Addrs(addr))
	if err := b.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer b.Disconnect()

	topic := "smoke.kafka.topic"
	// pre-create the topic: a consumer group that joins before the topic
	// exists only discovers it on the next rebalance, which makes a fresh
	// environment flaky
	ensureTopic(t, addr, topic)

	var got atomic.Int32
	var lastHeader atomic.Value
	sub, err := b.Subscribe(topic, func(ev broker.Event) error {
		if string(ev.Message().Body) == "smoke-payload" {
			lastHeader.Store(ev.Message().Header["trace-id"])
			got.Add(1)
		}
		return nil
	}, broker.Queue("smoke-group"))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// consumer group rebalance takes a few seconds on first join; retry the
	// publish until the subscriber sees it
	msg := &broker.Message{
		Header: map[string]string{"trace-id": "t-123"},
		Body:   []byte("smoke-payload"),
	}
	deadline := time.Now().Add(60 * time.Second)
	for got.Load() == 0 && time.Now().Before(deadline) {
		if err := b.Publish(topic, msg); err != nil {
			t.Logf("publish (topic may still be creating): %v", err)
		}
		for i := 0; i < 20 && got.Load() == 0; i++ {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if got.Load() == 0 {
		t.Fatal("subscriber never received the message")
	}
	if h, _ := lastHeader.Load().(string); h != "t-123" {
		t.Fatalf("header lost in transit: %q", h)
	}
	t.Logf("received %d message(s), header intact", got.Load())
}

// ensureTopic creates the topic on the controller if it does not exist.
func ensureTopic(t *testing.T, addr, topic string) {
	t.Helper()
	conn, err := kgo.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	controller, err := conn.Controller()
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	cc, err := kgo.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		t.Fatalf("dial controller: %v", err)
	}
	defer cc.Close()
	// TopicAlreadyExists is fine
	_ = cc.CreateTopics(kgo.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1})
}
