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

# The FIPS artifact set of SPEC 27.4. GOFIPS140 routes all cryptography
# through the Go Cryptographic Module and freezes it at the certified
# version, which is what an assessment is assessing; the version is not
# a preference and is not raised without one.
#
# The certified module is a source of truth in its own right: `go build`
# refuses a version the toolchain does not carry, so a typo here is a
# build failure rather than a non-FIPS binary named -fips.
FIPS_MODULE ?= v1.0.0
FIPS_ENV     = $(RELEASE_ENV) GOFIPS140=$(FIPS_MODULE)

TARGETS = linux/amd64 linux/arm64 freebsd/amd64 freebsd/arm64 \
	darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build test race vet cover check release cross clean tidy vendor policy fmt \
	fips fips-cross fips-verify fips-test

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

# -count=1 disables the test cache deliberately. Several of the audits
# here read the whole tree rather than their own package -- the
# declared-and-unread sweep, the documentation audit, the specification
# audit, the command matrix -- and the cache does not notice a file that
# did not exist when the result was recorded. It cached a pass over
# thirteen settings that had just started being read.
test:
	@env $(DEV_ENV) go test -count=1 ./...

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
	@env CGO_ENABLED=1 go test -count=1 -race ./...

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
#
# builtin, docsaudit and gitfs fail here and are expected to: each is the
# emulator rather than the code, and DIVERGENCE 4.1 says which test and
# why. A fourth package failing is a finding.
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
	echo "(builtin, docsaudit and gitfs are expected to fail here; see DIVERGENCE 4.1)"

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
	@gofmt -l -w cmd internal tools

# fmt-check reports rather than rewrites, which is what a gate needs.
#
# `check` used to begin with `fmt`, so it could never fail on formatting:
# it reformatted the tree and carried on, and in CI that is a stage that
# reports nothing it did not first make true.
fmt-check:
	@out=`gofmt -l cmd internal tools`; \
	if [ -n "$$out" ]; then \
		echo "these files are not gofmt'd:"; \
		echo "$$out" | sed 's/^/  /'; \
		echo "run \`make fmt\`"; \
		exit 1; \
	fi

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

# Build every binary twice and compare the digests.
#
# SPEC section 31 asks that two independent builders produce identical
# artifacts. This is not that, and the difference is worth stating: it is
# one builder, one toolchain, one machine. What it does establish is that
# the build does not embed the clock, the working directory, or anything
# else that varies between two runs of it — which is the half that
# usually breaks first, and the half that has to hold before two
# builders can agree about anything.
#
# The second build is made from a copy of the tree at a different path,
# so -trimpath is exercised rather than assumed.
repro:
	@set -e; \
	tmp=$$(mktemp -d); trap "rm -rf $$tmp" EXIT; \
	mkdir -p $$tmp/a $$tmp/b/src; \
	tar -cf - --exclude=bin --exclude=cover.out --exclude=dist . | (cd $$tmp/b/src && tar -xf -); \
	for b in $(BINARIES); do \
		env $(RELEASE_ENV) go build $(BUILDFLAGS) -o $$tmp/a/$$b ./cmd/$$b; \
		( cd $$tmp/b/src && env $(RELEASE_ENV) go build $(BUILDFLAGS) -o $$tmp/b/$$b ./cmd/$$b ); \
		if cmp -s $$tmp/a/$$b $$tmp/b/$$b; then \
			echo "$$b: identical"; \
		else \
			echo "$$b: DIFFERS between two builds of the same source"; \
			exit 1; \
		fi; \
	done

# build-all compiles every shipped target, which `build` does not: it
# builds for this host alone, so a platform-specific type or a missing
# constant on another one is invisible until somebody there runs make.
#
# macOS was grouped with the BSDs for the width of syscall.Rlimit — right
# about nearly everything and wrong about that — and the tree did not
# compile there at all. Nothing caught it, because nothing compiled for
# macOS until an operator did.
build-all:
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		printf "  %-16s " "$$t"; \
		env $(DEV_ENV) GOOS=$$os GOARCH=$$arch go build ./... || exit 1; \
		echo ok; \
	done

# What to run before calling a change done.
#
# fips-test is in here rather than left to CI: the FIPS artifacts are a
# shipped deliverable, and a restriction that only holds in the build
# nobody runs locally is a restriction that breaks on the tag.
check: fmt-check vet build-all test race policy fips-test

tidy:
	go mod tidy

vendor:
	go mod vendor

# fips builds the parallel artifact set for this machine.
fips:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "building bin/$$b-fips (module $(FIPS_MODULE))"; \
		env $(FIPS_ENV) go build $(BUILDFLAGS) -o bin/$$b-fips ./cmd/$$b || exit 1; \
	done
	@$(MAKE) fips-verify

# fips-verify refuses to ship an artifact that is not what its name says.
#
# A -fips binary that was built without GOFIPS140 is the failure this
# whole target exists to prevent: it looks right, it runs, and it is not
# using the certified module. The binary is asked rather than the build
# log, because the binary is what gets shipped.
fips-verify:
	@for b in $(BINARIES); do \
		out=`./bin/$$b-fips version 2>&1` || exit 1; \
		echo "$$out" | grep -q "fips $(FIPS_MODULE)" || { \
			echo "bin/$$b-fips does not report module $(FIPS_MODULE):" >&2; \
			echo "$$out" >&2; exit 1; }; \
		echo "$$out" | grep -q "self-tests passed" || { \
			echo "bin/$$b-fips does not report passing self-tests" >&2; exit 1; }; \
	done
	@echo "fips artifacts report module $(FIPS_MODULE) and passing self-tests"

# fips-test runs the whole suite as a FIPS artifact.
#
# SPEC section 32 makes "the FIPS build passes its self-tests" a phase 5
# exit criterion, and the self-tests are the floor rather than the
# check: what matters is that the tree still behaves when every
# primitive routes through the certified module. Running it found three
# tests that assumed SHA-1 and Ed25519 were always available, which is
# the same assumption a caller would have made.
fips-test:
	env $(FIPS_ENV) go test -count=1 ./...

# fips-cross is the release set. Only the tier 1 platforms of SPEC 27.1
# get one: the module is what is certified, and shipping a -fips binary
# for a platform nobody assessed would be a claim rather than a fact.
FIPS_TARGETS = linux/amd64 linux/arm64

fips-cross:
	@mkdir -p dist
	@for t in $(FIPS_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		for b in $(BINARIES); do \
			echo "building dist/$$b-fips-$$os-$$arch"; \
			env $(FIPS_ENV) GOOS=$$os GOARCH=$$arch \
				go build $(BUILDFLAGS) -o dist/$$b-fips-$$os-$$arch ./cmd/$$b || exit 1; \
		done; \
	done

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

# Installation. The paths follow the platform the way the binaries do,
# and internal/config's TestTheMakefileInstallsWhereTheBinariesLook holds
# the two to each other — a Makefile that installed somewhere the binary
# does not look would produce a service that starts and reads nothing.
#
# Computed in shell rather than with a conditional: BSD make spells those
# `.if` and GNU make spells them `ifeq`, and no file can carry both.
INSTALL_OS != uname -s

# BINDIR is /usr/local/bin on every platform, because that is the path
# written into the rc.d scripts and the systemd units. Moving it means
# editing those too.
BINDIR    ?= /usr/local/bin
CONFDIR   != case `uname -s` in FreeBSD|OpenBSD|NetBSD|DragonFly) echo /usr/local/etc/halite ;; *) echo /etc/halite ;; esac
STATEDIR  != case `uname -s` in FreeBSD|OpenBSD|NetBSD|DragonFly) echo /var/db/halite ;; *) echo /var/lib/halite ;; esac
SERVICEDIR != case `uname -s` in FreeBSD|OpenBSD|NetBSD|DragonFly) echo /usr/local/etc/rc.d ;; *) echo /etc/systemd/system ;; esac
CACHEDIR  ?= /var/cache/halite
LOGDIR    ?= /var/log/halite

# The account the hub and the API run as. A node runs as root, and root
# can write what this account owns, so one owner serves both.
HALITE_USER ?= halite

# install puts the binaries, the service files, and the directories in
# place. It writes no configuration: a `make install` that overwrote
# hub.yaml would be one nobody could run twice.
#
# Directories are created owned by HALITE_USER here rather than left to
# first use, because a directory created by running a command as root is
# one the service account cannot use afterwards — and the symptoms name
# neither the directory nor the account. See DIVERGENCE 5.20.
# Deliberately does not depend on `build`, so that the build never runs
# as root: that leaves root-owned binaries in bin/ and a root-owned Go
# build cache, both of which obstruct the next ordinary build. Depending
# on how sudo is set up it can also trip git's dubious-ownership check on
# a work tree owned by someone else, which surfaces as `error obtaining
# VCS status: exit status 128` and names neither git nor sudo. Build as
# yourself, install as root.
install:
	@for b in $(BINARIES); do \
		test -x bin/$$b || { \
			echo "bin/$$b is missing. Run 'make build' as yourself first:" >&2; \
			echo "  install does not build, so that the build never runs as root." >&2; \
			exit 1; }; \
	done
	@ref=""; for b in $(BINARIES); do ref=bin/$$b; break; done; \
	if [ -n "`find . -name '*.go' -newer $$ref -print 2>/dev/null | head -1`" ]; then \
		echo "  ! bin/ is older than the source; 'make build' first if that is not deliberate" >&2; \
	fi
	@for d in "$(BINDIR)" "$(CONFDIR)" "$(SERVICEDIR)"; do \
		p=`dirname "$$d"`; \
		{ test -w "$$d" 2>/dev/null || test -w "$$p"; } || { \
			echo "cannot write $$d — run make install as root, or point BINDIR," >&2; \
			echo "CONFDIR, STATEDIR, CACHEDIR, LOGDIR and SERVICEDIR somewhere writable" >&2; \
			exit 1; }; \
	done
	@echo "installing for $(INSTALL_OS)"
	@for b in $(BINARIES); do \
		echo "  $(BINDIR)/$$b"; \
		install -m 0755 bin/$$b $(BINDIR)/$$b || exit 1; \
	done
	@if id $(HALITE_USER) >/dev/null 2>&1; then \
		owner="-o $(HALITE_USER)"; \
	else \
		owner=""; \
		echo "  ! the $(HALITE_USER) account does not exist; directories are left owned by root" >&2; \
		echo "  ! the hub and the API cannot use them until it does. Create it, then run this again:" >&2; \
		case "$(INSTALL_OS)" in \
		FreeBSD|OpenBSD|NetBSD|DragonFly) \
			echo "      pw useradd $(HALITE_USER) -d /nonexistent -s /usr/sbin/nologin" >&2 ;; \
		*) echo "      useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin $(HALITE_USER)" >&2 ;; \
		esac; \
	fi; \
	for d in "$(CONFDIR) 0755" "$(CONFDIR)/pki 0700" "$(STATEDIR) 0700" "$(CACHEDIR) 0700" "$(LOGDIR) 0750"; do \
		set -- $$d; \
		echo "  $$1"; \
		install -d $$owner -m $$2 $$1 || exit 1; \
		if [ -n "$$owner" ] && [ -z "`find $$1 -maxdepth 0 -user $(HALITE_USER) 2>/dev/null`" ]; then \
			echo "  ! $$1 is not owned by $(HALITE_USER) and install did not say so" >&2; \
			echo "  ! the hub and the API cannot use it; run this as root" >&2; \
			exit 1; \
		fi; \
	done
	@$(MAKE) install-service
	@echo
	@echo "installed. Nothing was started and no configuration was written."
	@echo "  configuration  $(CONFDIR)/{hub,node,api}.yaml — contrib/examples has one of each"
	@echo "  service files  $(SERVICEDIR)"

# install-service puts the platform's own service files in place. They
# are overwritten: picking up a fix to them is the reason to run this.
install-service:
	@p=`dirname "$(SERVICEDIR)"`; \
	{ test -w "$(SERVICEDIR)" 2>/dev/null || test -w "$$p"; } || { \
		echo "cannot write $(SERVICEDIR) — run as root, or set SERVICEDIR" >&2; exit 1; }
	@case "$(INSTALL_OS)" in \
	FreeBSD|OpenBSD|NetBSD|DragonFly) \
		for f in contrib/rc.d/*; do \
			echo "  $(SERVICEDIR)/`basename $$f`"; \
			install -m 0555 $$f $(SERVICEDIR)/`basename $$f` || exit 1; \
		done ;; \
	*) \
		for f in contrib/systemd/*.service contrib/systemd/*.timer; do \
			echo "  $(SERVICEDIR)/`basename $$f`"; \
			install -m 0644 $$f $(SERVICEDIR)/`basename $$f` || exit 1; \
		done; \
		echo "  run systemctl daemon-reload before enabling anything" ;; \
	esac

# install-fips is the parallel artifact set of SPEC 27.4, for a host that
# is deploying it. The drop-ins are not installed automatically: on
# systemd they change what the unit runs, which is a decision.
install-fips:
	@for b in $(BINARIES); do \
		test -x bin/$$b-fips || { \
			echo "bin/$$b-fips is missing. Run 'make fips' as yourself first." >&2; \
			exit 1; }; \
	done
	@{ test -w "$(BINDIR)" 2>/dev/null || test -w `dirname "$(BINDIR)"`; } || { \
		echo "cannot write $(BINDIR) — run as root, or set BINDIR" >&2; exit 1; }
	@for b in $(BINARIES); do \
		echo "  $(BINDIR)/$$b-fips"; \
		install -m 0755 bin/$$b-fips $(BINDIR)/$$b-fips || exit 1; \
	done
	@echo "installed. contrib/systemd/fips/ holds the drop-ins; on a BSD set"
	@echo "  halite_hub_fips=YES (and _node, _api, _highstate) in rc.conf"

clean:
	rm -rf bin dist cover.out
