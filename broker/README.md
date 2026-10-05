# Broker

Asynchronous pub/sub messaging between services — fire-and-forget events, decoupled from the synchronous RPC path.

## Concept

- `Publish(topic, *Message)` / `Subscribe(topic, handler)` over a `Message{Header, Body}`.
- `broker.Queue("name")` makes subscribers **share** a queue (each message handled once — consumer-group semantics); without it every subscriber receives every message (broadcast).

```
broadcast (no Queue)                shared queue (Queue("workers"))
Publish ──▶ topic ──▶ sub A  ✉      Publish ──▶ topic ──▶ sub A  ✉
                  └─▶ sub B  ✉                        └─▶ sub B      (one of them,
                  └─▶ sub C  ✉                        └─▶ sub C       not all)
```
- Ack semantics: `AutoAck` (default) acks after the handler returns nil; return an error and the message is eligible for redelivery; manual mode calls `event.Ack()`.
- Services usually consume it indirectly: `micro.RegisterSubscriber` binds a typed handler, `micro.NewEvent(topic, client).Publish` sends typed events.

## Implementations

HTTP (default, registry-based) · memory (built-in) · NATS · RabbitMQ · Kafka (`broker/<name>`, own go.mod; Kafka maps Queue → consumer group and offers [async publishing](kafka)).
