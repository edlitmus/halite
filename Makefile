BIN     := bin/halite
TARGETS := freebsd/amd64 freebsd/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test vet cross clean

all: build

build:
	@mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/halite

test:
	go test ./...

vet:
	go vet ./...

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
