# halite build recipe.
#
# The integrity controls of SPEC section 4.3 are not optional flags a
# release engineer remembers: they are here, and internal/buildpolicy
# fails the test suite if any of them goes missing.
#
#   CGO_ENABLED=0        no cgo, so no shared object is linked
#   -trimpath            reproducible paths
#   -buildvcs=true       the artifact records the source commit
#   GOFLAGS=-mod=vendor  builds read the vendored allowlist
#   GOPROXY=off          the build network is disabled

BINARIES = halite-node halite-hub halite-api

# `!=` rather than `$(shell ...)`: BSD make has no $(shell), and this
# project is developed on FreeBSD. GNU make has supported `!=` since 4.0,
# so one spelling serves both.
GIT_VERSION != git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev
GIT_COMMIT  != git rev-parse HEAD 2>/dev/null || echo unknown
GIT_EPOCH   != git log -1 --format=%ct 2>/dev/null || echo 0

VERSION ?= $(GIT_VERSION)
COMMIT  ?= $(GIT_COMMIT)

# SOURCE_DATE_EPOCH is honoured so two builders on two machines produce
# identical digests.
SOURCE_DATE_EPOCH ?= $(GIT_EPOCH)

MODULE  = github.com/edlitmus/halite
LDFLAGS = -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT)

BUILDFLAGS = -trimpath -buildvcs=true -ldflags="$(LDFLAGS)"

# The vendored build environment. `dev` targets leave it off so that a
# working tree without vendor/ still builds during development; `release`
# turns it on and is what CI runs.
RELEASE_ENV = CGO_ENABLED=0 GOFLAGS=-mod=vendor GOPROXY=off SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH)
DEV_ENV     = CGO_ENABLED=0

TARGETS = linux/amd64 linux/arm64 freebsd/amd64 freebsd/arm64 \
	darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build test race vet cover check release cross clean tidy vendor policy fmt

all: build

build:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "building bin/$$b"; \
		env $(DEV_ENV) go build $(BUILDFLAGS) -o bin/$$b ./cmd/$$b || exit 1; \
	done

# release builds the way CI does: vendored, offline, cgo off.
release:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "building bin/$$b (release)"; \
		env $(RELEASE_ENV) go build $(BUILDFLAGS) -o bin/$$b ./cmd/$$b || exit 1; \
	done

test:
	@env $(DEV_ENV) go test ./...

# The correctness core is held to a higher bar than the rest of the tree,
# because the YAML parser, the template engine, the state compiler, and the
# targeting matcher are where a defect becomes a wrong change on a host.
# SPEC section 31.
cover:
	@env $(DEV_ENV) go test -coverprofile=cover.out ./... >/dev/null
	@go tool cover -func=cover.out | tail -1
	@echo "--- correctness core ---"
	@env $(DEV_ENV) go test -cover \
		./internal/yaml/ ./internal/template/ ./internal/state/ ./internal/target/ 2>/dev/null || true

# The race detector is the one place cgo is wanted: DEV_ENV pins
# CGO_ENABLED=0 for every other target, and -race with it off fails
# outright rather than quietly running without the detector.
race:
	@env CGO_ENABLED=1 go test -race ./...

# Run the test suite as Linux binaries.
#
# Everything outside the FreeBSD-specific modules is platform-neutral Go,
# but "it cross-compiles" is not the same as "it runs", and the Linux
# grain code reads /proc and /sys rather than sysctl — an entirely
# separate implementation that was never executed. On a FreeBSD host with
# the Linux compat layer and linprocfs mounted, the cross-compiled test
# binaries run here, so it can be.
#
# Two failures are expected and are the emulator rather than the code:
#
#   builtin/TestFileAccess    the compat layer resolves a symlink's
#                             absolute target against the FreeBSD root, so
#                             a stat through the link fails while a stat of
#                             the same path string succeeds.
#   docsaudit                 shells out to the Go toolchain, which a
#                             cross-executed binary cannot reach.
#
# The cmd packages are included: their tests re-execute the test binary
# as the command, so they need no toolchain and run here unchanged.
#
# What this does NOT exercise: the apt, dnf, and apk package providers and
# the systemd service provider. The compat layer has no Linux package
# manager and no init, so provider selection correctly reaches for the
# FreeBSD binaries that are there. Those wait on a real Linux host.
test-linux:
	@set -e; \
	tmp=$$(mktemp -d); trap "rm -rf $$tmp" EXIT; \
	pass=0; fail=0; failed=""; \
	for p in $$(go list ./internal/... ./cmd/...); do \
		n=$${p##*/}; d=$${p#github.com/edlitmus/halite/}; \
		env $(DEV_ENV) GOOS=linux GOARCH=amd64 go test -c -o "$$tmp/$$n.test" "$$p" 2>/dev/null || continue; \
		[ -f "$$tmp/$$n.test" ] || continue; \
		if ( cd "$$d" && "$$tmp/$$n.test" -test.count=1 >"$$tmp/$$n.out" 2>&1 ); then \
			pass=$$((pass+1)); \
		else \
			fail=$$((fail+1)); failed="$$failed $$n"; \
			echo "FAIL $$n"; grep -E '^\s+--- FAIL|^--- FAIL' "$$tmp/$$n.out" | head -5; \
		fi; \
	done; \
	echo "linux: $$pass package(s) passed, $$fail failed:$$failed"; \
	echo "(builtin and docsaudit are expected to fail here; see the Makefile comment)"

# Fuzzing, SPEC section 31. Go runs one target per invocation, so each is
# named. FUZZTIME is per target; the default is a short smoke run, and a
# real campaign is `make fuzz FUZZTIME=30m`.
FUZZTIME ?= 60s

fuzz:
	@env $(DEV_ENV) go test ./internal/yaml/ -run=XXX -fuzz='^FuzzParse$$' -fuzztime=$(FUZZTIME)
	@env $(DEV_ENV) go test ./internal/yaml/ -run=XXX -fuzz='^FuzzParseStream$$' -fuzztime=$(FUZZTIME)
	@env $(DEV_ENV) go test ./internal/yaml/ -run=XXX -fuzz='^FuzzEncodeScalar$$' -fuzztime=$(FUZZTIME)
	@env $(DEV_ENV) go test ./internal/template/ -run=XXX -fuzz='^FuzzRender$$' -fuzztime=$(FUZZTIME)
	@env $(DEV_ENV) go test ./internal/template/ -run=XXX -fuzz='^FuzzRenderStrictUndefined$$' -fuzztime=$(FUZZTIME)
	@env $(DEV_ENV) go test ./internal/target/ -run=XXX -fuzz='^FuzzCompileAuto$$' -fuzztime=$(FUZZTIME)
	@env $(DEV_ENV) go test ./internal/target/ -run=XXX -fuzz='^FuzzCompileKind$$' -fuzztime=$(FUZZTIME)

vet:
	@env $(DEV_ENV) go vet ./...

fmt:
	@gofmt -l -w cmd internal

# policy runs the specification's own build rules: the lexicon of section
# 2.3, the dependency allowlist of section 4.2, and the import checks of
# section 25.3.
policy:
	@env $(DEV_ENV) go test ./internal/buildpolicy/

# The vulnerability scan of SPEC section 31's security layer.
#
# It is not part of `check`, and the reason is the point: it fetches the
# tool and the vulnerability database, and `check` has to work on a
# machine with no network, which is the same machine a release is built
# on with GOPROXY=off.
#
# With no third-party dependencies, this is a scan of the Go standard
# library and nothing else — `go list -m all` returns one module, and
# buildpolicy fails the build if that stops being true. That makes it a
# check on the toolchain rather than on a supply chain, which is a
# smaller claim than the name suggests and the one worth making.
vuln:
	@env $(DEV_ENV) go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# What to run before calling a change done.
check: fmt vet test race policy

tidy:
	go mod tidy

vendor:
	go mod vendor

cross:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		for b in $(BINARIES); do \
			echo "building dist/$$b-$$os-$$arch$$ext"; \
			env $(RELEASE_ENV) GOOS=$$os GOARCH=$$arch \
				go build $(BUILDFLAGS) -o dist/$$b-$$os-$$arch$$ext ./cmd/$$b || exit 1; \
		done; \
	done

clean:
	rm -rf bin dist cover.out
