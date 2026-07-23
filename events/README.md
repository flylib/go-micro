# Events

Persistent event streaming — at-least-once, replayable streams, as opposed to the fire-and-forget [broker](../broker).

## Concept

- `Publish(topic, msg)` appends to a durable stream; `Consume(topic, ...)` reads with **named groups**, offsets, acks and retry limits — consumers can rejoin and resume.
- Also exposes a small event `Store` for reading history.
- Choose broker for lightweight notifications, events for audit trails / event sourcing / work queues that must survive restarts.

## Implementations

memory (default, in-process) · NATS JetStream (`events/natsjs`, own module).
