.PHONY: build debug dev test check

build:
	go build -o bin/bt ./cmd/bt

debug: build
	./scripts/mkdebug.sh

dev: build
	./bin/bt --dev dev/Volumes --config dev/config.toml

test:
	go test ./...

# What CI runs: formatting, vet, and the full suite under the race detector.
check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; echo "run gofmt -w"; exit 1)
	go vet ./...
	go test -race -count=1 ./...
