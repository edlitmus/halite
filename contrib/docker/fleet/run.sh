#!/bin/sh
# Drive the Debian row's modules against the real tools.
#
# HALITE_FLEET_LIVE is what the tests look for. Without it they skip, on
# every machine including this one, because a test that installs packages
# and relinks /etc/localtime is not one to run by accident on a laptop.
#
# The script drives the suite itself and then checks that the tests it
# claims actually passed; see "the run, and the proof that it ran" below.
set -e

# The recipe also passes -w /src. Named here as well, because the proof
# at the bottom is only worth something if `docker run` without it still
# lands in the tree rather than testing nothing from /.
cd /src

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

# The run, and the proof that it ran.
#
# # Why the test command is here rather than in the recipe
#
# It used to be the recipe's: the Makefile passed `go test ... -run
# TestLive` and this script ended in `exec "$@"`, having set the
# environment. That worked and proved nothing. Every test this leg
# selects skips politely on a machine that cannot run it -- which is most
# of them, since `-run TestLive` selects the FreeBSD, macOS, RHEL and
# systemd tests too -- so a change that made the Debian ones skip as well
# would have left a green leg with nothing in it. The rpm legs have had
# the check for this since 5.178; this leg did not, and 5.633 recorded the
# asymmetry rather than fixing it.
#
# So the command and the list of tests that must have passed live next to
# each other, in the one place both CI and a person reach: `make
# fleetcheck` and the `debian` job now run the same script with no
# arguments, and an argument is refused rather than quietly obeyed, since
# a recipe that still passed a command would be a second path doing the
# same job slightly differently.
if [ "$#" -gt 0 ]; then
	echo "run.sh drives the suite itself and takes no command; it was given: $*" >&2
	echo "To poke around the image instead, override the entrypoint:" >&2
	echo "  docker run --rm -it --entrypoint sh -v \"\$PWD\":/src -w /src halite-fleet:bookworm" >&2
	exit 2
fi

export HALITE_FLEET_LIVE=1
export HALITE_FLEET_PKGDIR=/srv/halite-fleet/pkgs
export HALITE_FLEET_KEYRING=/etc/apt/keyrings/halite-fleet.gpg

# What this leg claims, test by test.
#
# These are the tests that can only run here: the twelve gated on
# HALITE_FLEET_LIVE, which this script alone sets, and the dpkg
# differential, which needs no root but needs a dpkg -- and the `linux`
# leg's filter does not select the Dpkg family, so this container is the
# only place in CI it executes (5.633).
#
# Named individually rather than counted, because this is the list of
# things the leg asserts: a test renamed, deleted or newly skipping must
# fail it rather than quietly shorten it. `internal/buildpolicy`'s
# fleetproof_test.go holds this list to the tree in the other direction,
# so a thirteenth test gated on HALITE_FLEET_LIVE fails `make check`
# until it is named here.
#
# The subtest is named too. It is the one that asserts a Debian machine's
# grains pick the Debian ordering, so without it `version_cmp` could fall
# back to rpm's rules on a real Debian and every vector above would still
# agree.
expect='
	TestLiveDpkgListsWhatIsActuallyInstalled
	TestLiveDpkgReadsRealControlFields
	TestLiveDpkgSearchAndFileListAgreeWithEachOther
	TestLiveDpkgReadsARealPackageFile
	TestLiveDpkgHoldsAPackageAndDpkgAgrees
	TestLiveDpkgHoldsAnArchQualifiedPackageByItsPlainName
	TestLiveDpkgVersionCmpAgreesWithDpkg
	TestLiveDpkgVersionCmpAgreesWithDpkg/auto_picks_debian_on_a_machine_with_dpkg
	TestLiveDebconfSetsAnAnswerAndDebconfAgrees
	TestLiveDebconfShowOnAnUnknownPackageIsEmptyRatherThanAnError
	TestLiveRepoIsWrittenAndAptReadsIt
	TestLiveTimezoneSetsTheZoneAndReadsItBack
	TestLiveTimezoneRefusesAZoneThisMachineDoesNotHave
	TestLivePkgAutoremoveReclaimsAnUnneededPackage'

out=/tmp/live.txt
: >"$out"

# Teed, so a hang is readable before the job's timeout; the status
# carried through a file, since a pipeline's status is tee's, and `|| rc=$?`
# rather than a bare `echo $?` after it, because under `set -e` a failing
# go test would end the group before the echo.
#
# 15m, deliberately under the `debian` job's own 20-minute budget (the
# rpm legs have 45 and ask for 30). The run takes about a minute, so the
# number only matters when something hangs -- and then the useful outcome
# is Go timing out and dumping every goroutine, not GitHub killing the
# job first and leaving a log that stops mid-test.
echo "--- driving the modules: -run TestLive"
{
	rc=0
	go test -count=1 -v -timeout 15m -run TestLive ./internal/builtin/ 2>&1 || rc=$?
	echo "$rc" >/tmp/live.status
} | tee -a "$out"

echo "--- what ran"
printf 'tests that passed:  %s\n' "$(grep -c -- '--- PASS' "$out" || true)"
printf 'tests that skipped: %s\n' "$(grep -c -- '--- SKIP' "$out" || true)"
printf 'tests that failed:  %s\n' "$(grep -c -- '--- FAIL' "$out" || true)"

failed=0
if [ "$(cat /tmp/live.status)" != 0 ]; then
	echo "::error::go test failed in the fleet image; the output above says which test"
	failed=1
fi

# A skip is normal here and is not listed, with one exception: a test this
# leg claims. In a run where a hundred and seventy-odd tests skip because
# this is not a FreeBSD, a macOS or a RHEL, the reason for the one that
# matters is a line nobody would find.
awk '/--- SKIP: / { print $3": "prev } { prev=$0 }' "$out" >/tmp/skips.txt

missing=0
for name in $expect; do
	if grep -qF -- "--- PASS: $name (" "$out"; then
		continue
	fi
	echo "::error::$name did not pass in the fleet image: it failed, skipped, or no longer exists"
	grep -F -- "$name: " /tmp/skips.txt || true
	missing=$((missing + 1))
done
echo "expected to pass: $(echo $expect | wc -w); did not: $missing"
[ "$missing" -eq 0 ] || failed=1

exit "$failed"
