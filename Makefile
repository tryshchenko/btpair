.PHONY: build debug dev test check site

build:
	go build -o bin/btpair ./cmd/btpair

debug: build
	./scripts/mkdebug.sh

dev: build
	./bin/btpair --dev dev/Volumes --config dev/config.toml

test:
	go test ./...

# What CI runs: formatting, vet, and the full suite under the race detector.
check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; echo "run gofmt -w"; exit 1)
	go vet ./...
	go test -race -count=1 ./...
	./scripts/steps/apple-photos_test.sh >/dev/null

# Rebuild the website with fresh screenshots of the TUI.
site: build
	./scripts/site/build.sh
