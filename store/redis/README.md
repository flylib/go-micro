# Redis store

`store.Store` on Redis and Redis-compatible servers. It is tested on Redis 7, Dragonfly 2.0 (default script mode, and emulated cluster mode) and miniredis.

```go
import (
	"github.com/flylib/go-micro/store"
	"github.com/flylib/go-micro/store/redis"
)

s := redis.NewStore(
	store.Nodes("redis://:secret@127.0.0.1:6379/0"), // or "host:port"; several nodes = Cluster
	store.Table("orders"),
)
```

- **Nodes.** No nodes means `127.0.0.1:6379`. One node is an address or a `redis://` / `rediss://` URL, which can carry a password and a database number. Several nodes make a Cluster client.
- **Existing client.** For Sentinel, TLS settings or pool sizes, build the client yourself and pass `redis.WithClient(c)`. The store does not close a client it was given.

## Layout

Each database/table pair gets three kinds of keys. They share one hash tag, so a table always lives in one Cluster slot:

```
micro:store:{<db>/<table>}:r:<key>   hash: v = value, m = metadata JSON
micro:store:{<db>/<table>}:idx       zset (score 0): the table's keys in byte order
micro:store:{<db>/<table>}:exp       zset: key -> expiry time (server clock, ms)
```

- **Writes.** A Lua script replaces the record, its index entry and its expiry in one step. Expiry uses `PEXPIRE`. `WriteTTL` wins over `WriteExpiry`, and both win over `Record.Expiry`, as in the memory store. A write whose expiry has already passed deletes the record.
- **Reads.** A plain `Read(key)` reads the hash. `List` and prefix/suffix reads first drop index entries whose expiry has passed. They then walk the index with `ZRANGEBYLEX`, so keys come back in byte order, like the file and SQL stores. The server applies prefix, limit and offset. A suffix filter pages through the matching range.
- **Dragonfly.** Scripts only touch the keys they declare, which Dragonfly's default script mode requires. They read the time with `TIME`, so client clock skew does not matter. No server flags are needed.
- **Scaling.** One table is one zset, so a very large table puts its whole index on one node. Spread data over several tables (`store.Table`, `store.Scope`) when that matters.
