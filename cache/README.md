# Cache

In-process/remote caching with TTL — `Get/Put/Delete` with expiry, for hot data in front of stores or downstream calls.

## Concept

- `Cache` interface with `Context` options per backend; each service gets its own instance (`service.Cache(...)`).
- Distinct from [store](../store): cache is lossy and TTL-driven, store is durable.

## Implementations

memory (default, an in-process map guarded by a `sync.RWMutex`) · Redis (`cache/redis`, own module).
