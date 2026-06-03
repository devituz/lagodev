.PHONY: build check test test-race bench lint fmt vet tidy artisan clean

GOFLAGS  := -trimpath
PKG      := ./...
ARTISAN  := ./cmd/artisan

build:
	go build $(GOFLAGS) ./...

# Run all checks a CI run performs. Invoked by CONTRIBUTING.md.
check: vet test
	@gofmt -l . | (! grep .) || (echo "gofmt: files need formatting; run 'make fmt'" && exit 1)

test:
	go test $(GOFLAGS) -count=1 $(PKG)

test-race:
	go test $(GOFLAGS) -race -count=1 $(PKG)

cover:
	go test $(GOFLAGS) -cover -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out

bench:
	go test $(GOFLAGS) -bench=. -benchmem -run=^$$ ./benchmarks/...

fmt:
	gofmt -s -w .

vet:
	go vet $(PKG)

tidy:
	go mod tidy

artisan:
	go build -o bin/artisan $(ARTISAN)

clean:
	rm -rf bin coverage.out
