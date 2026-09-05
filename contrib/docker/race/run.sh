#!/bin/sh
# Run the race leg as an ordinary account.
#
# The first run of this container ran as root and failed one test:
#
#   --- FAIL: TestOpeningAnUnusableNodeCacheFailsAtOnce
#       a node cache this process cannot write was opened without complaint
#
# That is not a defect in the code under test. `permtest.DenyWrite` makes
# a directory 0500 and the test asserts that opening a cache there is
# refused; root has CAP_DAC_OVERRIDE and writes to it anyway, so the
# condition the test exists to create never existed. It is the same
# failure the permtest package doc describes for `os.Chmod` on Windows,
# arriving from the other direction: a test that cannot make the
# condition it is testing for is not testing for it.
#
# Running as root would also be the wrong environment on its own terms.
# A hub runs as a service account, and the cache-ownership bug this very
# test was written for is one that only appears when the account that
# created a directory and the account that opens it differ.
set -e

# The caches are named volumes, created root-owned by Docker before this
# runs. Chowned only when they are not already right, because walking a
# populated module cache on every run is most of a minute for nothing.
for d in /gocache /gomodcache; do
	mkdir -p "$d"
	if [ "$(stat -c %u "$d")" != "$(id -u halite)" ]; then
		chown -R halite:halite "$d"
	fi
done

# /src is a bind mount, so its files carry the host's ownership and not
# this account's. git calls that "dubious ownership" and refuses, which
# `go build` reports as:
#
#   error obtaining VCS status: exit status 128
#
# That is not cosmetic here. Several tests build a helper binary — the
# bridge's test extensions, the render package's stand-in gpg — and
# every one of them failed on it. The alternative, -buildvcs=false,
# would have made them build something the release does not.
#
# Set through the environment rather than a config file: it is stateless,
# and it names one directory rather than turning the check off.
GIT_CONFIG_COUNT=1
GIT_CONFIG_KEY_0=safe.directory
GIT_CONFIG_VALUE_0=/src
export GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0

# HOME so the toolchain has somewhere to put its own state; /src stays
# read-only in practice because `go test` writes only to the caches and
# to GOTMPDIR.
exec setpriv --reuid=halite --regid=halite --init-groups \
	env HOME=/home/halite \
	GIT_CONFIG_COUNT="$GIT_CONFIG_COUNT" \
	GIT_CONFIG_KEY_0="$GIT_CONFIG_KEY_0" \
	GIT_CONFIG_VALUE_0="$GIT_CONFIG_VALUE_0" \
	"$@"
