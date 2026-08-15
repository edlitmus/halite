BIN     := bin/halite
TARGETS := freebsd/amd64 freebsd/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test race vet check docs cross clean

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

# The documentation checks: every state in the module reference, every
# command and flag documented, every link resolving, every example
# compiling, and the counts matching the registry. They are part of `test`
# already — this target is for running them alone while editing docs.
docs:
	go test ./internal/docs/

# What to run before calling a change done. Documentation is not a
# separate step: `test` includes the checks in internal/docs, so a change
# that leaves a doc behind fails here rather than shipping.
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
