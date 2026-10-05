# Store

Key-value persistence — the framework's simple durable state abstraction.

## Concept

- `Read / Write / Delete / List` over `Record{Key, Value, Metadata, Expiry}`; options add prefix/suffix scans, limits and TTL.
- Namespacing: a store is scoped by database/table; each service's store is auto-scoped to `service/<name>` (`store.Scope`) so co-hosted services stay isolated.
- It is deliberately not a database — for typed queries use [model](../model), which builds on the store.

## Implementations

file/bbolt (default, durable) · memory (built-in) · Postgres · MySQL · NATS JetStream KV · Redis / Dragonfly / Valkey (`store/<name>`, own go.mod).
