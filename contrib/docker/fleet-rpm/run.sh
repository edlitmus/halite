#!/bin/sh
# Drive the RHEL and SUSE rows' modules against the real tools, and prove
# they ran.
#
# One script for all three images, because the part worth keeping in one
# place is the proof at the bottom, and the part that differs -- which
# tests this distribution can run -- is a few lines keyed on
# /etc/os-release. The filter and the list of tests that must have passed
# sit next to each other on purpose: a test added to one and not the other
# is visible in the same screenful.
#
# # Why the proof is here and not in the workflow
#
# `make fleetcheck-rocky9` and CI must do the same thing, and the only way
# to guarantee that is for there to be one thing. A person who runs the
# target by hand gets the same "this test skipped, so this run proved
# nothing about it" failure the leg does.
set -e

cd /src

. /etc/os-release
echo "--- what this image is"
echo "distribution: $PRETTY_NAME ($ID $VERSION_ID)"
echo "kernel:       $(uname -r) (the runner's; a container has no other)"
echo "rpm:          $(rpm --version)"
command -v dnf >/dev/null && echo "dnf:          $(dnf --version 2>/dev/null | head -1)"
command -v zypper >/dev/null && echo "zypper:       $(zypper --version 2>&1)"
command -v authselect >/dev/null && echo "authselect:   $(rpm -q authselect)"
echo "e2fsprogs:    $(rpm -q e2fsprogs)"
echo "/tmp is:      $(stat -f -c %T /tmp) -- where the chattr test makes its files"
echo "go:           $(go version)"

# Which tests run here, and which must have passed.
#
# The -run filter names families (TestLiveRpm, not each test), so a
# sibling added later is selected without anybody remembering to -- the
# lesson internal/buildpolicy's fleetfilter_test.go records for the linux
# leg. `expect` names every test individually, because it is the list of
# things this leg claims, and a test that vanished (renamed, deleted,
# filtered out) must fail it rather than quietly shorten it.
#
# Leap is filtered to two named rpm tests rather than the family: the
# third, TestLiveRpmVerifySeesARealModification, edits two files Red Hat
# ships (/etc/DIR_COLORS.lightbgcolor, which's NEWS) and SUSE does not,
# so it could only skip there. That is a limit of the test, recorded in
# DIVERGENCE 5.178, not a skip this leg accepts.
# `allowed_skips` names the one kind of skip a leg accepts: a subtest
# gated on a tool version this distribution does not ship. It is empty
# except on EL8, whose rpm 4.14.3 has no EVR comparison for the `evr`
# subtest (DIVERGENCE 5.177) to be checked against. Naming it here keeps
# every other skip a failure, and requiring it to happen means an EL8
# image that someday ships rpm >= 4.16 says so instead of quietly running
# a check this list does not claim.
allowed_skips=''
case "$ID" in
rocky | almalinux)
	[ "$ID" = almalinux ] && allowed_skips='TestLiveRpmVersionCmpAgreesWithRpm/evr'
	modules='TestLiveRpm|TestLiveChattr|TestLiveAuthselect|TestLiveDnfModule|TestLiveKernelpkgReadsAgreeWithRPM|TestLiveKernelpkgRPM'
	expect='
		TestLiveRpmReadsTheRealDatabase
		TestLiveRpmVersionCmpAgreesWithRpm
		TestLiveRpmVerifySeesARealModification
		TestLiveChattrSetsAndClearsImmutableAndAppend
		TestLiveAuthselectReads
		TestLiveAuthselectSelectAndFeatures
		TestLiveDnfModuleReadersAgree
		TestLiveDnfModuleEnableSwitchDisableReset
		TestLiveDnfModuleInstallAndRemove
		TestLiveKernelpkgReadsAgreeWithRPM
		TestLiveKernelpkgRPMInstallsAndRemovesKernels'
	;;
opensuse-leap)
	modules='TestLiveZypper|TestLiveRpmReadsTheRealDatabase|TestLiveRpmVersionCmp|TestLiveChattr'
	expect='
		TestLiveZypperInstallsHoldsAndRemovesARealPackage
		TestLiveZypperReadsAgreeWithTheMachine
		TestLiveZypperLatestUpgradesAndAPinDowngrades
		TestLiveZypperInstallsPastAnUnreachableRepository
		TestLiveRpmReadsTheRealDatabase
		TestLiveRpmVersionCmpAgreesWithRpm
		TestLiveChattrSetsAndClearsImmutableAndAppend'
	;;
*)
	echo "this script knows the tests for Rocky, AlmaLinux and openSUSE Leap, and this is $ID"
	exit 1
	;;
esac

# SPEC 11.6's four package states, through whichever provider this
# distribution picks: dnf on EL, zypper on Leap. Only these four cases --
# the rest of the harness needs systemd, accounts, a kernel, which a
# container either lacks or shares with the runner. The summary subtest is
# selected too, because it is where the harness counts what ran.
conformance='TestLiveConformanceOnThisMachine/^(pkg\.[a-z]+|what_ran_here)$'
expect="$expect
	TestLiveConformanceOnThisMachine/pkg.installed
	TestLiveConformanceOnThisMachine/pkg.removed
	TestLiveConformanceOnThisMachine/pkg.latest
	TestLiveConformanceOnThisMachine/pkg.purged
	TestLiveConformanceOnThisMachine/what_ran_here"

# HALITE_AUTHSELECT_TAKEOVER: a fresh container has never been configured
# by authselect, and 1.2.6 has no way back, so the test will not select a
# profile over the image's own /etc/pam.d without being told the machine
# is disposable. This one is thrown away when the run ends.
# HALITE_KERNELPKG_LIVE: kernelpkg installs two real kernels and removes one.
# The container is thrown away, and its kernel is the runner's, so neither
# is one anything boots.
export HALITE_SYSTEM_LIVE=1 HALITE_CONFORMANCE_LIVE=1 HALITE_AUTHSELECT_TAKEOVER=1 HALITE_KERNELPKG_LIVE=1

out=/tmp/live.txt
: >"$out"

# Teed, so a hang is readable before the job's timeout; the status carried
# through a file, since a pipeline's status is tee's. `|| rc=$?` rather
# than a bare `echo $?` after the command, because under `set -e` a
# failing go test would end the group before the echo and leave no status
# to read.
echo "--- driving the modules: -run '$modules'"
{
	rc=0
	go test -count=1 -v -timeout 30m -run "$modules" ./internal/builtin/ 2>&1 || rc=$?
	echo "$rc" >/tmp/modules.status
} | tee -a "$out"

echo "--- driving the conformance harness: -run '$conformance'"
{
	rc=0
	go test -count=1 -v -timeout 30m -run "$conformance" ./internal/builtin/ 2>&1 || rc=$?
	echo "$rc" >/tmp/conformance.status
} | tee -a "$out"

echo "--- what ran, and what skipped and why"
printf 'tests that passed:  %s\n' "$(grep -c -- '--- PASS' "$out" || true)"
printf 'tests that skipped: %s\n' "$(grep -c -- '--- SKIP' "$out" || true)"
printf 'tests that failed:  %s\n' "$(grep -c -- '--- FAIL' "$out" || true)"
awk '/--- SKIP/ { print $3": "prev } { prev=$0 }' "$out"

failed=0
if [ "$(cat /tmp/modules.status)" != 0 ] || [ "$(cat /tmp/conformance.status)" != 0 ]; then
	echo "::error::go test failed on $PRETTY_NAME; the output above says which test"
	failed=1
fi

# Every test selected here is one this distribution can run, so a skip is
# the leg doing less than it says. That is the failure mode this whole
# script is for: a green run in which everything quietly skipped.
for name in $(awk '/--- SKIP:/ { print $3 }' "$out"); do
	case " $allowed_skips " in
	*" $name "*) ;;
	*)
		echo "::error::$name skipped on $PRETTY_NAME, and every test this leg selects is meant to run here. The reason is printed above."
		failed=1
		;;
	esac
done
for name in $allowed_skips; do
	if ! grep -qF -- "--- SKIP: $name (" "$out"; then
		echo "::error::$name was expected to skip on $PRETTY_NAME and did not: the tool it waits for may have arrived, so this list is out of date"
		failed=1
	fi
done

missing=0
for name in $expect; do
	if ! grep -qF -- "--- PASS: $name (" "$out"; then
		echo "::error::$name did not pass on $PRETTY_NAME: it failed, skipped, or no longer exists"
		missing=$((missing + 1))
	fi
done
echo "expected to pass: $(echo $expect | wc -w); did not: $missing"
[ "$missing" -eq 0 ] || failed=1

# The machine was put back. The container is thrown away, so this is not
# about the container: the tests are also meant for a real lab host, and
# one whose cleanup leaves `tree` installed or a repository configured is
# one nobody runs twice.
echo "--- the machine was put back"
if rpm -q tree >/dev/null 2>&1; then
	echo "::error::tree is still installed after the suite; a pkg case's cleanup did not remove it"
	failed=1
fi
if ls /etc/zypp/repos.d 2>/dev/null | grep -q halitecf; then
	echo "::error::the suite left a halitecf repository in /etc/zypp/repos.d"
	failed=1
fi
if command -v zypper >/dev/null; then zypper --non-interactive locks || true; fi
ls /etc/dnf/modules.d 2>/dev/null || true

exit "$failed"
