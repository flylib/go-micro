# Codec

Message encoding — how request/response bodies and frames are (de)serialized on the wire.

## Concept

- Chosen per-request by `Content-Type`; the server keeps a codec map and picks the peer's declared format, so mixed-format clients can talk to one server.
- Default content type is `application/json`; protobuf is preferred for generated types.
- `codec/bytes.Frame` passes raw bytes through untouched — used by gateways/proxies that must not re-encode.

## Implementations

json · proto · jsonrpc · protorpc · grpc (frame format) · text · bytes — all built-in, selected via `client.Codec`/`server.Codec` options or content type.
