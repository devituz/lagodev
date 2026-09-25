module github.com/devituz/lagodev/drivers/redis

go 1.26.0

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/devituz/lagodev v0.27.0
	github.com/redis/go-redis/v9 v9.22.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace github.com/devituz/lagodev => ../..
