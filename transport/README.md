# Transport

Point-to-point synchronous communication — the pipe RPC rides on. Client and server exchange `Message{Header, Body}` frames over a `Socket`.

## Concept

- Interface: `Dial` (client side) and `Listen/Accept` (server side) produce Sockets with `Send/Recv`.
- The wire protocol is swappable (HTTP default, gRPC, NATS) without touching handlers — codec and headers stay identical across transports.
- Header keys are standardized in [`transport/headers`](headers) (`Micro-Service`, `Micro-Endpoint`, `Micro-Trace-ID`, …) — that package is shared vocabulary, not a backend.

## Implementations

HTTP/1.1+h2 (default) · memory (tests) · gRPC (`transport/grpc`) · NATS (`transport/nats`) — the latter two are standalone modules.
