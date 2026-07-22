// Package kafka provides a kafka broker for go-micro using segmentio/kafka-go.
package kafka

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"

	"github.com/flylib/go-micro/broker"
	log "github.com/flylib/go-micro/logger"
)

type kafkaBroker struct {
	addrs []string

	// one writer per topic, created lazily
	writers map[string]*kgo.Writer

	opts      broker.Options
	mu        sync.RWMutex
	connected bool
}

// NewBroker returns a kafka broker.
func NewBroker(opts ...broker.Option) broker.Broker {
	options := broker.Options{
		Context: context.Background(),
		Logger:  log.DefaultLogger,
	}
	for _, o := range opts {
		o(&options)
	}

	addrs := options.Addrs
	if len(addrs) == 0 {
		addrs = []string{"127.0.0.1:9092"}
	}

	return &kafkaBroker{
		addrs:   addrs,
		opts:    options,
		writers: make(map[string]*kgo.Writer),
	}
}

func (b *kafkaBroker) Init(opts ...broker.Option) error {
	for _, o := range opts {
		o(&b.opts)
	}
	if len(b.opts.Addrs) > 0 {
		b.addrs = b.opts.Addrs
	}
	return nil
}

func (b *kafkaBroker) Options() broker.Options {
	return b.opts
}

func (b *kafkaBroker) Address() string {
	if len(b.addrs) > 0 {
		return b.addrs[0]
	}
	return ""
}

// Connect verifies at least one bootstrap broker is reachable.
func (b *kafkaBroker) Connect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.connected {
		return nil
	}
	var gerr error
	for _, addr := range b.addrs {
		conn, err := kgo.Dial("tcp", addr)
		if err != nil {
			gerr = err
			continue
		}
		conn.Close()
		b.connected = true
		return nil
	}
	if gerr == nil {
		gerr = errors.New("no kafka addresses configured")
	}
	return gerr
}

func (b *kafkaBroker) Disconnect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var gerr error
	for topic, w := range b.writers {
		if err := w.Close(); err != nil {
			gerr = err
		}
		delete(b.writers, topic)
	}
	b.connected = false
	return gerr
}

func (b *kafkaBroker) writer(topic string) *kgo.Writer {
	b.mu.RLock()
	w, ok := b.writers[topic]
	b.mu.RUnlock()
	if ok {
		return w
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if w, ok = b.writers[topic]; ok {
		return w
	}
	w = &kgo.Writer{
		Addr:                   kgo.TCP(b.addrs...),
		Topic:                  topic,
		Balancer:               &kgo.LeastBytes{},
		AllowAutoTopicCreation: true,
		RequiredAcks:           kgo.RequireOne,
	}
	b.writers[topic] = w
	return w
}

func (b *kafkaBroker) Publish(topic string, m *broker.Message, _ ...broker.PublishOption) error {
	headers := make([]kgo.Header, 0, len(m.Header))
	for k, v := range m.Header {
		headers = append(headers, kgo.Header{Key: k, Value: []byte(v)})
	}
	return b.writer(topic).WriteMessages(b.opts.Context, kgo.Message{
		Value:   m.Body,
		Headers: headers,
	})
}

func (b *kafkaBroker) Subscribe(topic string, h broker.Handler, opts ...broker.SubscribeOption) (broker.Subscriber, error) {
	opt := broker.NewSubscribeOptions(opts...)

	// a shared queue maps to a kafka consumer group; without one each
	// subscriber gets its own group and receives every message
	group := opt.Queue
	if group == "" {
		group = uuid.New().String()
	}

	reader := kgo.NewReader(kgo.ReaderConfig{
		Brokers: b.addrs,
		Topic:   topic,
		GroupID: group,
	})

	ctx, cancel := context.WithCancel(b.opts.Context)
	sub := &subscriber{
		reader: reader,
		topic:  topic,
		opts:   opt,
		cancel: cancel,
	}

	go sub.run(ctx, b, h)
	return sub, nil
}

func (b *kafkaBroker) String() string {
	return "kafka"
}

type subscriber struct {
	reader *kgo.Reader
	cancel context.CancelFunc
	topic  string
	opts   broker.SubscribeOptions
}

func (s *subscriber) run(ctx context.Context, b *kafkaBroker, h broker.Handler) {
	for {
		km, err := s.reader.FetchMessage(ctx)
		if err != nil {
			// context canceled or reader closed → subscriber stopped
			return
		}

		msg := &broker.Message{
			Header: make(map[string]string, len(km.Headers)),
			Body:   km.Value,
		}
		for _, hd := range km.Headers {
			msg.Header[hd.Key] = string(hd.Value)
		}

		ev := &event{topic: s.topic, message: msg, reader: s.reader, km: km, ctx: ctx}
		if err := h(ev); err != nil {
			ev.err = err
			if eh := b.opts.ErrorHandler; eh != nil {
				_ = eh(ev)
			} else {
				b.opts.Logger.Logf(log.ErrorLevel, "[kafka] subscriber error: %v", err)
			}
			continue // leave uncommitted so the message is redelivered
		}
		if s.opts.AutoAck {
			if err := ev.Ack(); err != nil {
				b.opts.Logger.Logf(log.ErrorLevel, "[kafka] commit error: %v", err)
			}
		}
	}
}

func (s *subscriber) Options() broker.SubscribeOptions {
	return s.opts
}

func (s *subscriber) Topic() string {
	return s.topic
}

func (s *subscriber) Unsubscribe() error {
	s.cancel()
	return s.reader.Close()
}

type event struct {
	ctx     context.Context
	reader  *kgo.Reader
	message *broker.Message
	err     error
	topic   string
	km      kgo.Message
}

func (e *event) Topic() string {
	return e.topic
}

func (e *event) Message() *broker.Message {
	return e.message
}

// Ack commits the message offset to the consumer group.
func (e *event) Ack() error {
	return e.reader.CommitMessages(e.ctx, e.km)
}

func (e *event) Error() error {
	return e.err
}
