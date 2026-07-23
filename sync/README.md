# Sync

Distributed coordination — locks and leader election across service instances.

## Concept

- `Lock/Unlock(id)`: named distributed mutex. `LockTTL` leases the lock so a crashed holder auto-releases; `LockWait` bounds acquisition (`ErrLockTimeout`).
- `Leader(id)`: campaign for leadership, blocking until elected. The returned `Leader` steps down with `Resign()`; `Status()` closes when leadership is lost (lease expiry, resign) — watch it to stop leader-only work (cron, migrations, singleton consumers).

```go
s := etcd.NewSync(sync.Nodes("127.0.0.1:2379"), sync.Prefix("/myapp"))
if l, err := s.Leader("scheduler"); err == nil {
    go runCron()            // leader-only work
    <-l.Status()            // lost leadership → stop
}
```

## Implementations

memory (built-in, in-process — tests/dev) · etcd (`sync/etcd`, own module; clientv3/concurrency sessions).
