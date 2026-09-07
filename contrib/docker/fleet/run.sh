#!/bin/sh
# Drive the Debian row's modules against the real tools.
#
# HALITE_FLEET_LIVE is what the tests look for. Without it they skip, on
# every machine including this one, because a test that installs packages
# and relinks /etc/localtime is not one to run by accident on a laptop.
set -e

echo "--- what this image is"
. /etc/os-release
echo "distribution: $PRETTY_NAME"
echo "dpkg:         $(dpkg --version | head -1)"
echo "debconf:      $(dpkg-query -W -f='${Version}' debconf)"
echo "tzdata:       $(dpkg-query -W -f='${Version}' tzdata)"

# The offline assertions, checked rather than assumed.
#
# A missing fixture would otherwise surface as a test failure that reads
# like a defect in the module, and the first person to see it would go
# looking in the wrong place.
echo "--- what the run needs, all of it local"
for f in /srv/halite-fleet/pkgs/Packages.gz /srv/halite-fleet/pkgs/InRelease /etc/apt/keyrings/halite-fleet.gpg; do
	test -s "$f" || { echo "missing: $f"; exit 1; }
done
ls /srv/halite-fleet/pkgs/*.deb >/dev/null 2>&1 || {
	echo "no .deb was baked into the image; dpkg.bin_pkg_info cannot run"
	exit 1
}
echo "packages:     $(ls /srv/halite-fleet/pkgs/*.deb | tr '\n' ' ')"

# The run has no network: `make fleetcheck` starts this container with
# `--network none`, so there is no interface but loopback and a test that
# quietly started reaching a mirror fails here rather than on the day the
# mirror is down.
#
# Asserted rather than assumed, because the container does not choose its
# own networking and a recipe that dropped the flag would otherwise go
# unnoticed for as long as nothing needed it.
echo "--- checking that this run has no network"
if ip -o link show 2>/dev/null | grep -qvE ": lo:"; then
	echo "this container has an interface other than loopback:"
	ip -o link show
	echo "run it with --network none; \`make fleetcheck\` does"
	exit 1
fi
echo "interfaces:   loopback only"

echo "--- driving the modules"
export HALITE_FLEET_LIVE=1
export HALITE_FLEET_PKGDIR=/srv/halite-fleet/pkgs
export HALITE_FLEET_KEYRING=/etc/apt/keyrings/halite-fleet.gpg

exec "$@"
