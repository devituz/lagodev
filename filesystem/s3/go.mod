module github.com/devituz/lagodev/filesystem/s3

go 1.25.0

require (
	github.com/devituz/lagodev v0.14.0
	github.com/minio/minio-go/v7 v7.0.76
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/rs/xid v1.6.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)

// Local development: workspace overrides this anyway, but the replace
// directive lets `go build ./filesystem/s3/...` succeed without the
// workspace.
replace github.com/devituz/lagodev => ../..
