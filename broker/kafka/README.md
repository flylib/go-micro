# Kafka broker

`broker.Broker` on Kafka, built on segmentio/kafka-go.

```go
import (
	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/broker/kafka"
)

b := kafka.NewBroker(broker.Addrs("10.0.0.1:9092"))                // synchronous: Publish waits for the ack
events := kafka.NewBroker(broker.Addrs("10.0.0.1:9092"), kafka.Async()) // fire and forget, e.g. analytics
```

## Publishing

- **Synchronous (default).** `Publish` returns once Kafka acknowledges the message (`RequiredAcks`, default `kgo.RequireOne`). Concurrent publishes to a topic share batches, which are flushed at least every `BatchTimeout` (default 10ms). One publish therefore costs about a round trip, not the 1 s kafka-go waits by default.
- **`Async()`.** `Publish` queues the message and returns in microseconds. The writer sends batches every `BatchTimeout` or when `BatchSize` messages are waiting. Messages that fail are handed to the broker's `ErrorHandler` (`broker.ErrorHandler`), or logged. Use it where losing a message on a crash is acceptable.
- **Tuning.** `kafka.BatchTimeout(d)`, `kafka.BatchSize(n)` (kafka-go default 100), `kafka.RequiredAcks(kgo.RequireAll)`.
- **Context.** `broker.PublishContext(ctx)` bounds a synchronous publish.
- **Topics.** Topics are created on first write (`AllowAutoTopicCreation`). The first publish to a new topic can fail while Kafka creates it.

## Subscribing

- Each subscription is a kafka-go `Reader`.
- `broker.Queue("name")` makes it a consumer group, so each message is handled once. Without a queue, every subscriber gets its own group and receives every message.
- With `AutoAck` (the default), an offset is committed after the handler returns nil. A handler error leaves it uncommitted for redelivery.
