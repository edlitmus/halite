# The image `make check`'s race leg runs in when the host cannot run it.
#
# `make race` sets CGO_ENABLED=1 deliberately: the detector is not
# available with cgo off, and a race target that quietly ran without the
# detector would be a stage that reports nothing it did not first make
# true. The cost of that decision is a C toolchain, and Windows does not
# have one by default:
#
#   cgo: C compiler "gcc" not found
#
# So on the platform that has found four cross-platform defects so far,
# the concurrency surface went unchecked, and `make check` could not
# complete at all. This is how that leg runs there.
#
# It is not only a Windows workaround. The race detector had never been
# run against this tree on Linux either — DIVERGENCE 4.1 records the
# Linux runs as unit tests under emulation — so this is also the first
# time the scheduler doing the interleaving is the one the estate runs
# on.
FROM golang:1.25-bookworm

# gcc comes with the base image; assert it rather than assume it. A base
# image that stopped shipping a compiler would otherwise surface as the
# same "not found" this container exists to fix, several layers deeper.
RUN gcc --version >/dev/null

# The suite runs as an ordinary account, not as root. run.sh says why:
# root has CAP_DAC_OVERRIDE, so the tests that assert a refusal on an
# unwritable directory cannot create the condition they test for, and
# one of them failed on the first run of this image for exactly that.
RUN useradd --create-home --shell /bin/sh halite

# The image's Go is whatever the base image ships; go.mod pins a newer
# toolchain, and the base image sets GOTOOLCHAIN=local, which refuses to
# fetch it. `auto` lets the pinned toolchain be downloaded once into the
# module cache volume, so the leg runs the toolchain the project pins
# rather than the one the image happened to have.
ENV GOTOOLCHAIN=auto
ENV GOCACHE=/gocache
ENV GOMODCACHE=/gomodcache

# GOFLAGS is deliberately unset. vendor/ is present, so the default
# resolution is -mod=vendor, which is what `make race` gets on a host —
# and matching the host is the point of this container. Setting it to
# -mod=mod, as the saltdiff image does for its own reasons, would test a
# module graph the release never builds from.

# The detector needs the whole suite, so the command is the `race`
# recipe verbatim rather than a subset. -count=1 for the same reason it
# is there: a cached pass is not a run.
#
# run.sh drops to the unprivileged account and then execs this, so the
# recipe stays readable as the recipe.
ENV CGO_ENABLED=1
COPY run.sh /usr/local/bin/run.sh
RUN chmod +x /usr/local/bin/run.sh
ENTRYPOINT ["/usr/local/bin/run.sh"]
CMD ["go", "test", "-count=1", "-race", "./..."]
