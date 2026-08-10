BIN     := bin/halite
TARGETS := freebsd/amd64 freebsd/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test race vet check cross clean

all: build

build:
	@mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/halite

test:
	go test ./...

# The control plane, agent, event bus, reactor, beacons, mine, and
# orchestration all run goroutines against shared state. Race-detector
# runs are separate from `test` because they are slower and the detector
# is not available on every platform Go builds for.
race:
	go test -race ./...

vet:
	go vet ./...

# What to run before calling a change done.
check: vet test race

cross:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		echo "building dist/halite-$$os-$$arch$$ext"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" \
			-o dist/halite-$$os-$$arch$$ext ./cmd/halite || exit 1; \
	done

clean:
	rm -rf bin dist
