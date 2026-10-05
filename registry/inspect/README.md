# Registry inspection

`micro-inspect` finds registry instances that do not answer. These are nodes still registered in Nacos, etcd or Consul whose process is gone, hung or unreachable ("zombies"), and nodes that answer but report themselves unhealthy. It sends each change to a webhook: Feishu, DingTalk, WeCom or plain JSON.

It complements an APM such as Coroot, which sees traffic between processes but not what the registry claims is running.

## Run

```bash
cd registry/inspect && go build -o micro-inspect ./cmd/micro-inspect   # from a checkout; see below

MICRO_REGISTRY=nacos MICRO_REGISTRY_ADDRESS=nacos:8848 \
MICRO_REGISTRY_USERNAME=nacos MICRO_REGISTRY_PASSWORD=... \
MICRO_INSPECT_WEBHOOK='https://oapi.dingtalk.com/robot/send?access_token=...' \
MICRO_INSPECT_WEBHOOK_KIND=dingtalk MICRO_INSPECT_WEBHOOK_SECRET=SEC... \
./micro-inspect
```

| Variable | Default | Meaning |
|---|---|---|
| `MICRO_REGISTRY`, `MICRO_REGISTRY_ADDRESS` | — | `etcd`, `consul` or `nacos`, and its addresses |
| `MICRO_REGISTRY_NAMESPACE`, `MICRO_REGISTRY_GROUP` | public, `DEFAULT_GROUP` | Nacos namespace and group |
| `MICRO_REGISTRY_USERNAME`, `MICRO_REGISTRY_PASSWORD` | — | Nacos credentials (auth is on by default since Nacos 3.0) |
| `MICRO_INSPECT_INTERVAL` | `30s` | Time between rounds |
| `MICRO_INSPECT_TIMEOUT` | `3s` | Per probe |
| `MICRO_INSPECT_THRESHOLD` | `3` | Failed rounds in a row before a node is reported |
| `MICRO_INSPECT_SERVICES` | all | Comma-separated services to inspect |
| `MICRO_INSPECT_WEBHOOK` | — | URL that state changes are posted to |
| `MICRO_INSPECT_WEBHOOK_KIND` | `json` | `json`, `feishu`, `dingtalk` or `wecom` |
| `MICRO_INSPECT_WEBHOOK_SECRET` | — | Signing secret of a Feishu or DingTalk bot that has signature security on |
| `MICRO_INSPECT_ADDRESS` | `:8070` | HTTP address: `GET /status` (every node, as JSON) and `GET /health` |

`go install …@main` does not work for this command, like the gateway's: the module keeps `replace` directives for in-repo builds. Build it from a checkout.

## How it decides

Each round lists the registry and probes every node concurrently:

| Node | Probe | State |
|---|---|---|
| `protocol=grpc` | `grpc.health.v1` Check (the [gRPC server](../../server#health-checks-grpc-server) serves it) | `SERVING` → **ok**, `NOT_SERVING` → **unhealthy**, no connection within the timeout → **unreachable** |
| `protocol=grpc`, older server without the health service | the same call | an answer (`Unimplemented`, …) → **ok** |
| anything else | TCP connect | accepted → **ok**, else **unreachable** |

- **Reporting.** A node is reported after `MICRO_INSPECT_THRESHOLD` bad rounds in a row. It is reported **ok** again after one good round. A reported node that leaves the registry is reported **deregistered**. Each change is sent once, so a dead node does not spam the channel.
- **Report only.** It never deregisters a node, because a wrong guess would remove a healthy instance.
- **Where zombies come from.**
  - **Hung or frozen processes** stay registered until the registry notices: in Nacos 2.x and later, until its connection check expels the client (tens of seconds); in etcd and Consul, until the registration TTL runs out.
  - **The dangerous case lasts indefinitely.** The process is alive and keeps its registry session, but callers cannot reach its service port. Causes include an exhausted worker pool, a blocked or misconfigured port, or a network partition between the node and its callers but not Nacos.
  - **Persistent Nacos instances** stay until someone removes them.

A message (DingTalk / WeCom text, Feishu text):

```
🔴 [go-micro] users v1: ok → unreachable
node users-f5423065… 10.0.3.7:9001
error: rpc error: code = DeadlineExceeded desc = context deadline exceeded while waiting for connections to become ready
2 failed checks in a row
```

## As a library

```go
in := inspect.New(reg, inspect.Options{
	Interval: 30 * time.Second,
	Notifier: &inspect.Webhook{URL: url, Kind: inspect.KindFeishu, Secret: secret},
})
go in.Run(ctx)
nodes := in.Nodes() // current state of every node
```
