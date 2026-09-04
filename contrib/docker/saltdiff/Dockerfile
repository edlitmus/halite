# The image SPEC section 31's differential gate runs in.
#
# The gate compares the same state tree compiled by halite and by Salt,
# and it is the only check that can say an existing tree means the same
# thing under a reimplementation. It has always skipped where Salt is
# absent, which is every machine this project is developed on, so the
# primary correctness gate has been green by not running. DIVERGENCE
# records the container as never built; this is it.
#
# Asking a developer to install Salt on the machine they work on is how a
# gate stays skipped: it is a Python application with a long dependency
# list and a habit of conflicting with a system Python.
FROM golang:1.25-bookworm

# The Salt major to compare against. SPEC 31's deviation table records
# which version each difference was observed under — 3006 and 3008
# disagree with each other about what show_lowstate projects — so the
# version is part of the result rather than whatever a resolver picked
# today.
ARG SALT_VERSION=3007.1
ARG SALT_ARCH=x86_64

RUN apt-get update \
    && apt-get install --no-install-recommends -y curl ca-certificates xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Salt's own onedir bundle rather than pip.
#
# `pip install salt` succeeds and produces a salt-call that cannot start:
# Salt declares its runtime dependencies in requirements files rather
# than in its wheel metadata, so pip installs none of them and the first
# import fails with ModuleNotFoundError — tornado, then yaml, then
# whatever is next. Chasing that list would be guessing at a set that
# changes between releases.
#
# The onedir bundle is the artifact Salt ships for production and the one
# an estate actually installs. Comparing against it rather than against a
# hand-assembled environment is also the more honest test: the reference
# implementation is what the reference implementation ships as.
RUN curl -fsSL -o /tmp/salt.tar.xz \
      "https://packages.broadcom.com/artifactory/saltproject-generic/onedir/${SALT_VERSION}/salt-${SALT_VERSION}-onedir-linux-${SALT_ARCH}.tar.xz" \
    && mkdir -p /opt \
    && tar -C /opt -xJf /tmp/salt.tar.xz \
    && rm /tmp/salt.tar.xz \
    && /opt/salt/salt-call --version

# Salt shells out for a few of its own operations and reads SHELL to
# decide how. Unset, it picks something that is not there in a minimal
# image and fails in a way that looks like a difference.
ENV SHELL=/bin/sh
ENV HALITE_SALT_CALL=/opt/salt/salt-call

# The third comparison — every state evaluated in test mode against this
# host — is off by default because it reads the machine it runs on. In a
# container that machine is disposable, which is the whole reason to have
# one, so it is on here.
ENV HALITE_SALTDIFF_RESULTS=1

# The image's Go is whatever the base image ships; go.mod pins a newer
# toolchain, and the base image sets GOTOOLCHAIN=local, which refuses to
# fetch it. `auto` lets the pinned toolchain be downloaded once into the
# module cache volume, so the gate runs the toolchain the project pins
# rather than the one the image happened to have.
ENV GOTOOLCHAIN=auto
ENV GOFLAGS=-mod=mod
ENV GOCACHE=/gocache
ENV GOMODCACHE=/gomodcache

WORKDIR /src
ENTRYPOINT ["go", "test", "-count=1", "./internal/saltdiff/"]
