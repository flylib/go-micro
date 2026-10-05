module github.com/flylib/go-micro/sync/redis

go 1.26.0

require (
	github.com/alicebob/miniredis/v2 v2.39.0
	github.com/flylib/go-micro v0.0.0-20261005063438-00e6294a60a5
	github.com/redis/go-redis/v9 v9.21.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
)

replace github.com/flylib/go-micro => ../..
