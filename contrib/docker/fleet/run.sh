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

# The run itself gets no network. `unshare -n` is the assertion rather
# than a promise in a comment: a test that quietly started reaching a
# mirror would fail here rather than on the day the mirror is down.
#
# Where the kernel will not allow it -- an unprivileged container, some
# CI sandboxes -- the run still happens and says that the isolation was
# not applied, because losing the whole check to gain the assertion is
# the wrong trade.
echo "--- driving the modules"
export HALITE_FLEET_LIVE=1
export HALITE_FLEET_PKGDIR=/srv/halite-fleet/pkgs
export HALITE_FLEET_KEYRING=/etc/apt/keyrings/halite-fleet.gpg

if unshare -n true 2>/dev/null; then
	exec unshare -n sh -c 'ip link set lo up 2>/dev/null || true; exec "$@"' sh "$@"
fi
echo "note: this kernel would not give the run its own network namespace,"
echo "      so 'offline' is unasserted here. The tests still reach nothing"
echo "      by design; see the Dockerfile."
exec "$@"
