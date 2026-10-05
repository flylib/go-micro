package kafka

import (
	"context"
	"time"

	kgo "github.com/segmentio/kafka-go"

	"github.com/flylib/go-micro/broker"
)

// DefaultBatchTimeout is how long a writer waits to fill a batch before
// sending it. kafka-go's own default is one second, which a synchronous
// Publish of a single message waits out in full; 10ms keeps one publish
// fast and still batches concurrent ones.
const DefaultBatchTimeout = 10 * time.Millisecond

type writerOptions struct {
	async        bool
	batchTimeout time.Duration
	batchSize    int
	acks         kgo.RequiredAcks
}

type writerOptionsKey struct{}

func writerOpts(o *broker.Options) writerOptions {
	if o.Context != nil {
		if w, ok := o.Context.Value(writerOptionsKey{}).(writerOptions); ok {
			return w
		}
	}
	return writerOptions{batchTimeout: DefaultBatchTimeout, acks: kgo.RequireOne}
}

func setWriter(fn func(*writerOptions)) broker.Option {
	return func(o *broker.Options) {
		w := writerOpts(o)
		fn(&w)
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, writerOptionsKey{}, w)
	}
}

// Async makes Publish return without waiting for Kafka: messages are
// queued and sent in batches (BatchTimeout, BatchSize). Delivery errors
// go to the broker's ErrorHandler, or are logged. Use it for fire-and-
// forget traffic such as analytics events.
func Async() broker.Option {
	return setWriter(func(w *writerOptions) { w.async = true })
}

// BatchTimeout sets how long a batch may wait to fill before it is sent
// (default DefaultBatchTimeout).
func BatchTimeout(d time.Duration) broker.Option {
	return setWriter(func(w *writerOptions) { w.batchTimeout = d })
}

// BatchSize sets how many messages a batch holds at most (kafka-go
// default 100).
func BatchSize(n int) broker.Option {
	return setWriter(func(w *writerOptions) { w.batchSize = n })
}

// RequiredAcks sets the acknowledgements a write waits for: kgo.RequireNone,
// kgo.RequireOne (default) or kgo.RequireAll.
func RequiredAcks(a kgo.RequiredAcks) broker.Option {
	return setWriter(func(w *writerOptions) { w.acks = a })
}
