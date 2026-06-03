module github.com/devituz/lagodev/drivers/redis

go 1.25.0

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/devituz/lagodev v0.17.0
	github.com/redis/go-redis/v9 v9.7.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
)

replace github.com/devituz/lagodev => ../..
