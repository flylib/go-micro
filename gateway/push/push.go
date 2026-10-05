// Package push sends messages to WebSocket clients connected to a gateway
// (gateway/SPEC.md section 2.3). Services publish on the broker; every
// gateway instance subscribed for the target delivers the message.
//
//	p := push.New(svc.Options().Broker)      // the broker the gateway uses, e.g. NATS
//	p.ToUser(ctx, "user-42", map[string]any{"type": "match", "with": "user-7"})
//	p.ToTopic(ctx, "room.42", msg)
//
// Delivery is at most once: clients that are not connected when a message
// is published do not get it. Keep anything that must not be lost in a
// service and let clients fetch it on reconnect.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/flylib/go-micro/broker"
)

const (
	// UserPrefix is the broker topic prefix of pushes to an account.
	UserPrefix = "micro.push.user."
	// TopicPrefix is the broker topic prefix of messages to a topic.
	TopicPrefix = "micro.push.topic."
	// MaxTopic is the longest topic name.
	MaxTopic = 256
)

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// ValidTopic reports whether t is a topic name: dot-separated segments of
// letters, digits, '_' and '-', at most MaxTopic bytes.
func ValidTopic(t string) bool {
	return len(t) <= MaxTopic && topicRe.MatchString(t)
}

// UserTopic is the broker topic of pushes to account. Bytes outside
// [A-Za-z0-9_-] are written as %XX, so any account is one topic segment.
func UserTopic(account string) string {
	return UserPrefix + escape(account)
}

// TopicOf is the broker topic of messages to a client topic.
func TopicOf(topic string) string {
	return TopicPrefix + topic
}

func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// Pusher publishes pushes on a broker.
type Pusher struct {
	broker broker.Broker

	mu        sync.Mutex
	connected bool
}

// New returns a Pusher on b, which must be the broker the gateways
// subscribe to. The Pusher connects the broker before its first push if
// nothing has yet (every broker's Connect is a no-op when connected).
func New(b broker.Broker) *Pusher {
	return &Pusher{broker: b}
}

// connect connects the broker once; a failure is retried on the next push.
func (p *Pusher) connect() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connected {
		return nil
	}
	if err := p.broker.Connect(); err != nil {
		return fmt.Errorf("push: connect broker %s: %w", p.broker, err)
	}
	p.connected = true
	return nil
}

// ToUser sends msg to every WebSocket connection of account.
func (p *Pusher) ToUser(ctx context.Context, account string, msg any) error {
	if account == "" {
		return errors.New("push: empty account")
	}
	return p.publish(ctx, UserTopic(account), msg)
}

// ToTopic sends msg to every WebSocket connection subscribed to topic.
func (p *Pusher) ToTopic(ctx context.Context, topic string, msg any) error {
	if !ValidTopic(topic) {
		return fmt.Errorf("push: invalid topic %q", topic)
	}
	return p.publish(ctx, TopicOf(topic), msg)
}

func (p *Pusher) publish(ctx context.Context, topic string, msg any) error {
	body, err := encode(msg)
	if err != nil {
		return err
	}
	if err := p.connect(); err != nil {
		return err
	}
	m := &broker.Message{Header: map[string]string{"Content-Type": "application/json"}, Body: body}
	return p.broker.Publish(topic, m, broker.PublishContext(ctx))
}

// encode returns msg as JSON: []byte and json.RawMessage must already be
// JSON, anything else is marshalled.
func encode(msg any) ([]byte, error) {
	var b []byte
	switch m := msg.(type) {
	case json.RawMessage:
		b = m
	case []byte:
		b = m
	default:
		var err error
		if b, err = json.Marshal(msg); err != nil {
			return nil, fmt.Errorf("push: encode: %w", err)
		}
		return b, nil
	}
	if !json.Valid(b) {
		return nil, errors.New("push: message is not JSON")
	}
	return b, nil
}

// ToUser sends msg to account's connections through broker.DefaultBroker.
// Services usually configure their own broker (NATS): prefer
// New(svc.Options().Broker).
func ToUser(ctx context.Context, account string, msg any) error {
	return New(broker.DefaultBroker).ToUser(ctx, account, msg)
}

// ToTopic sends msg to topic's subscribers through broker.DefaultBroker.
func ToTopic(ctx context.Context, topic string, msg any) error {
	return New(broker.DefaultBroker).ToTopic(ctx, topic, msg)
}
