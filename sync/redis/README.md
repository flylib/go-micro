# Redis sync

Distributed locks and leader election (`sync.Sync`) on Redis and Redis-compatible servers. It is tested on Redis 7, Dragonfly 2.0 and miniredis.

```go
import (
	microsync "github.com/flylib/go-micro/sync"
	"github.com/flylib/go-micro/sync/redis"
)

s := redis.NewSync(microsync.Nodes("redis://:secret@127.0.0.1:6379/0"), microsync.Prefix("/myapp"))

if err := s.Lock("orders-migration", microsync.LockWait(10*time.Second)); err != nil {
	return err // sync.ErrLockTimeout after 10s
}
defer s.Unlock("orders-migration")

l, err := s.Leader("scheduler") // blocks until elected
if err != nil {
	return err
}
go runCron()
<-l.Status() // closed on Resign or when leadership is lost
```

`NewSyncFromClient(c, ...)` uses an existing client, for example a Sentinel client (`redis.NewFailoverClient`). Nodes work as in [store/redis](../../store/redis).

## Semantics

These match the etcd implementation:

- **Lock lifetime.** A lock is held until `Unlock` while its holder lives. A watchdog extends the lease every TTL/3. `LockTTL` (default 30s) only sets how long a crashed holder blocks others.
- **Unlock.** `Unlock` deletes the key only if it still holds this holder's random token. A holder whose lock expired and was taken over cannot free the new owner's lock.
- **Leadership.** `Leader` is a lock on `<prefix>/micro-sync/leader/<id>` with a 30s lease. `Status()` closes in three cases: on `Resign`, when the key is found to belong to someone else, or when renewals have failed for a whole lease.
- **Waiting.** Waiters poll with jittered backoff, from 20ms up to 500ms.
- **Dragonfly.** The Lua scripts touch only their declared key, so they run in Dragonfly's default script mode.

## Limits

A lock lives on one primary. If a failover loses the key before it replicates, a second holder can get in. For work that must never overlap, also check a fencing value where the work is applied, such as a version column.
