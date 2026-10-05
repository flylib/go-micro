module github.com/flylib/go-micro/cache/redis

go 1.26.0

require (
	github.com/flylib/go-micro v0.0.0-20261005032823-771d2b07a7e2
	github.com/go-redis/redis/v8 v8.11.5
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/uuid v1.6.0 // indirect
)

replace github.com/flylib/go-micro => ../..
