# MongoDB store

`store.Store` on MongoDB 4.4 or later. It is tested on MongoDB 7.

```go
import (
	"github.com/flylib/go-micro/store"
	"github.com/flylib/go-micro/store/mongo"
)

s := mongo.NewStore(
	store.Nodes("mongodb://user:pass@127.0.0.1:27017"), // or mongodb+srv://...
	store.Database("orders"),
)
```

- **Nodes.** No nodes means `mongodb://127.0.0.1:27017`. To share a `*mongo.Client`, pass `mongo.WithClient(c)`. The store does not disconnect a client it was given.

## Layout

A store database is a MongoDB database, and a table is a collection. MongoDB forbids some characters in those names (`/ \ . " $` in database names, `$` in collection names). The store percent-encodes them, so names like `service/orders` work and stay distinct. One record is one document:

```
{_id: <key>, value: <binary>, metadata: <JSON text>, expiresAt: <date, only with a TTL>}
```

- **Scans.** `List` and prefix/suffix reads are anchored regular expressions on `_id`, sorted by `_id`. Keys come back in byte order, like the file and SQL stores. A prefix scan uses the `_id` index. The server applies limit and offset.
- **Expiry.** The server clock (`$$NOW`) computes the expiry time at write and checks it at read, so client clock skew does not matter.
  - Precedence: `WriteTTL` wins over `WriteExpiry`, and both win over `Record.Expiry`. A write whose expiry has already passed deletes the record.
  - Cleanup: a TTL index on `expiresAt` removes expired documents in the background. MongoDB runs it about once a minute, but reads already skip expired documents.
- **Metadata.** Metadata is stored as JSON text. Numbers come back as `float64`, as in the Redis and Postgres stores.
