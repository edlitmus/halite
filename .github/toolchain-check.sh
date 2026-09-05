#!/bin/sh
# Assert that the Go on PATH is the toolchain go.mod pins.
#
# `release`, `fips-test` and `repro` build with GOPROXY=off, which is the
# point of them: the build must not be able to fetch anything, so that
# what it produces depends on the tree alone. Fetching a *toolchain* also
# goes through the proxy, so a runner whose Go is older than the pinned
# one fails inside those targets with a message about a module proxy,
# which is not what went wrong.
#
# This says what went wrong. It is a check on the runner, not on the
# tree: the fix is the Go version the workflow installs, never the
# toolchain line.
set -eu

want=$(awk '/^toolchain /{print $2; exit}' go.mod)
if [ -z "$want" ]; then
	# No toolchain line is legitimate -- the `go` directive is then the
	# floor and any newer Go satisfies it. Nothing to assert.
	echo "go.mod pins no toolchain; skipping"
	exit 0
fi

have=$(go env GOVERSION)
if [ "$have" = "$want" ]; then
	echo "toolchain $have, as go.mod pins"
	exit 0
fi

cat >&2 <<EOF
go.mod pins the toolchain $want and this runner has $have.

The offline targets (release, fips-test, repro) build with GOPROXY=off,
so Go cannot fetch $want and will fail there with an error about the
module proxy rather than about its own version.

Fix the Go the workflow installs. Do not relax the toolchain line: it is
what makes two builders produce the same bytes.
EOF
exit 1
