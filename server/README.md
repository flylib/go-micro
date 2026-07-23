# Server

The inbound half of RPC — listens on a transport, routes requests to registered handlers, dispatches broker events to subscribers.

## Concept

- `Handle` registers a struct; each exported `(ctx, *Req, *Rsp) error` method becomes an endpoint (`Struct.Method`). Endpoint metadata is published to the registry.
- Owns registration lifecycle: on `Start` it registers the node (TTL-refreshed), on `Stop` it deregisters and drains — graceful shutdown is built in.
- `Subscribe` binds queue/topic consumers through the same server so they share codecs and wrappers.
- Note the distinction: **service** is the whole assembled unit; **server** is only its request-receiving component (see the main README's Core Concepts).

Implementations: RPC (default) · gRPC (`server/grpc`, own module) · mock (tests).
