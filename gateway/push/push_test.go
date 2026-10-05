package push

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flylib/go-micro/broker"
)

func TestTopics(t *testing.T) {
	for in, want := range map[string]string{
		"user-42":       "micro.push.user.user-42",
		"a.b@example":   "micro.push.user.a%2Eb%40example",
		"spaces in it ": "micro.push.user.spaces%20in%20it%20",
	} {
		if got := UserTopic(in); got != want {
			t.Errorf("UserTopic(%q) = %q, want %q", in, got, want)
		}
	}
	for topic, ok := range map[string]bool{"room.42": true, "a": true, "a_b-c.d": true, "": false, "a..b": false, "a.": false, "a b": false, "a.*": false} {
		if ValidTopic(topic) != ok {
			t.Errorf("ValidTopic(%q) != %v", topic, ok)
		}
	}
}

func TestPublish(t *testing.T) {
	b := broker.NewMemoryBroker()
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	defer b.Disconnect()

	got := make(chan *broker.Message, 4)
	for _, topic := range []string{UserTopic("u1"), TopicOf("room.1")} {
		if _, err := b.Subscribe(topic, func(e broker.Event) error { got <- e.Message(); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	p := New(b)
	ctx := context.Background()
	if err := p.ToUser(ctx, "u1", map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := p.ToTopic(ctx, "room.1", json.RawMessage(`"hi"`)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{"n":1}`, `"hi"`} {
		select {
		case m := <-got:
			if string(m.Body) != want {
				t.Fatalf("body %s, want %s", m.Body, want)
			}
		case <-time.After(time.Second):
			t.Fatal("not delivered")
		}
	}

	if err := p.ToTopic(ctx, "bad topic", "x"); err == nil {
		t.Fatal("invalid topic accepted")
	}
	if err := p.ToUser(ctx, "", "x"); err == nil {
		t.Fatal("empty account accepted")
	}
	if err := p.ToUser(ctx, "u1", []byte("not json")); err == nil {
		t.Fatal("non-JSON bytes accepted")
	}
}

// A Pusher connects a broker nothing has connected yet (a service with
// no subscribers on the gRPC server used to leave it unconnected).
func TestPublishConnectsBroker(t *testing.T) {
	b := broker.NewMemoryBroker() // not connected
	if err := New(b).ToUser(context.Background(), "u1", "hi"); err != nil {
		t.Fatalf("push on an unconnected broker: %v", err)
	}
}
