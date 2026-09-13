#!/bin/sh
# Drive the lab instances: ship this tree to each and run the suite.
#
# Called by the `lab-*` targets in the Makefile rather than directly, and
# kept as a script rather than as Makefile recipes because the loop below
# has to keep going after a host fails and then report which ones did --
# a `make` recipe either stops at the first failure or hides all of them.
#
# # What "the suite" means on a lab host
#
# Two runs per machine, because they answer different questions:
#
#   1. `go build ./...` and the unit suite. This says the tree compiles
#      and behaves on this distribution's libc and kernel -- the "build"
#      half of what the lab is for, and the only half that means anything
#      on a row whose provider is not written yet.
#   2. The live tests, as root, with HALITE_SYSTEM_LIVE=1. This is the
#      half no runner and no container gives: a real dnf, a real apk, a
#      real zypper, a real systemd or a real OpenRC, driven by the
#      modules that claim to drive them.
#
# The reboot gate is **not** set. HALITE_REBOOT_LIVE is the one that
# schedules a real reboot, and a machine that reboots mid-run takes its
# own results with it. A lab instance is the right place to run it
# eventually -- it is disposable in a way beastie is not -- but it is an
# explicit choice, not something a sweep does on the way past.

set -eu

TOFU_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH='' cd -- "$TOFU_DIR/../.." && pwd)"

SSH_OPTS="-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$TOFU_DIR/.known_hosts -o ConnectTimeout=10 -o BatchMode=yes"
REMOTE_DIR=/root/halite

usage() {
    cat >&2 <<'EOF'
usage: lab.sh <command> [distro ...]

  hosts     print the instances this state knows about
  wait      block until every instance has finished its bootstrap
  facts     print each instance's recorded facts (what it really is)
  test      ship the tree to each instance and run build, unit and live suites
  ssh       open a shell on one instance (exactly one distro required)
  untaint   clear taint left by a failed post-create read (see lab-repair)

With no distro names, every instance in the state is used.
EOF
    exit 2
}

[ $# -ge 1 ] || usage
command="$1"
shift

# The addresses come from the state rather than from the API, so this
# script and `tofu` cannot disagree about what exists.
#
# An empty state has to be detected before `output -raw` is trusted.
#
# `tofu output -raw` against a state with no outputs prints a *warning*
# -- to **stdout**, in ANSI colour -- and **exits 0**. So neither a `||`
# nor a redirect of stderr catches it, and an emptiness check on the
# result does not either, because the result is not empty: it is nine
# lines of box-drawing characters. Read as a host list, that produced a
# table of machines named "|" which were all "not ready".
#
# `output -json` is the form with a machine-readable empty answer: it
# prints exactly `{}` and nothing else. That is what is checked first.
#
# A *partly* applied lab is the other case, and it is the likely one: an
# apply that failed on one instance writes the outputs it could evaluate
# and not the ones it could not. `monthly_cost_if_left_running` only
# counts instances and survives; `ssh_targets` reads every instance's
# address, so one unfinished instance leaves it unwritten entirely. The
# state then has seven running machines and no way to address them, which
# is exactly when this script is wanted. `output -raw` on a missing
# output exits 1 and explains itself in terraform's terms; the message
# below is the same fact in this lab's terms.
targets() {
    _json="$(tofu -chdir="$TOFU_DIR" output -json -no-color 2>/dev/null || echo '{}')"
    case "$(printf '%s' "$_json" | tr -d '[:space:]')" in
    '' | '{}')
        echo "no lab instances in the state at $TOFU_DIR." >&2
        echo "  'make lab-up' raises them; 'make lab-distros' lists the rows." >&2
        exit 1
        ;;
    esac
    if ! _t="$(tofu -chdir="$TOFU_DIR" output -raw -no-color ssh_targets 2>/dev/null)"; then
        echo "the lab state has instances but no ssh_targets output." >&2
        echo "  That is what a part-finished 'make lab-up' leaves behind: one instance" >&2
        echo "  that did not complete takes the whole output with it, even though the" >&2
        echo "  others are running and billing." >&2
        echo "  'make lab-repair' converges it; 'make lab-down' destroys the lot." >&2
        exit 1
    fi
    [ -n "$_t" ] || {
        echo "ssh_targets is empty; the state has no instances to address." >&2
        exit 1
    }
    echo "$_t"
}

# Write the selected targets to a file and echo its path.
#
# Every command reads its host list through this rather than piping
# `selected_targets` into a `while`. The right-hand side of a pipe runs
# in a subshell, so an `exit 1` from `targets` -- the "no state" case --
# would end that subshell and leave the script exiting 0, reporting
# success for a lab that does not exist. `hosts` and `facts` both did
# exactly that.
load_targets() {
    _f="$(mktemp)"
    selected_targets "$@" >"$_f"
    echo "$_f"
}

# Filter to the distros named on the command line, or pass everything
# through when none were.
selected_targets() {
    # `targets` once, not once per name: it shells out to tofu, and
    # calling it inside the loop also meant its "no state" message was
    # printed again for every distro asked for.
    # The `|| exit` is not redundant with `set -e`. An assignment from a
    # failing command substitution does trip `set -e` today, which is
    # what makes the no-state case exit 1 -- but that is a subtlety one
    # edit away from being lost, and what it would be lost to is the
    # script carrying on with an empty host list.
    _all="$(targets)" || exit 1
    [ -n "$_all" ] || exit 1
    if [ $# -eq 0 ]; then
        echo "$_all"
        return
    fi
    for want in "$@"; do
        line="$(echo "$_all" | awk -F'\t' -v w="$want" '$1 == w')"
        if [ -z "$line" ]; then
            echo "no instance named '$want' in the current state" >&2
            exit 1
        fi
        echo "$line"
    done
}

# A host is usable only once its bootstrap wrote the ready file. Anything
# earlier is a machine with a partial package set and no Go, and a test
# result from one is worse than no result.
wait_for_ready() {
    name="$1"
    addr="$2"
    tries=60
    while [ "$tries" -gt 0 ]; do
        # shellcheck disable=SC2086
        if ssh $SSH_OPTS "root@$addr" 'test -f /var/lib/halite-lab/ready' 2>/dev/null; then
            return 0
        fi
        tries=$((tries - 1))
        sleep 10
    done
    echo "$name ($addr) never finished its bootstrap." >&2
    echo "  /var/log/halite-lab-bootstrap.log on the host says why; a checksum" >&2
    echo "  mismatch or a failed Go download stops it deliberately." >&2
    return 1
}

case "$command" in
hosts)
    tmp="$(load_targets "$@")"
    trap 'rm -f "$tmp"' EXIT
    printf '%-12s %-16s %s\n' DISTRO ADDRESS STATE
    while IFS="$(printf '\t')" read -r name addr; do
        [ -n "$name" ] || continue
        # shellcheck disable=SC2086
        if ssh $SSH_OPTS "root@$addr" 'test -f /var/lib/halite-lab/ready' 2>/dev/null; then
            state=ready
        else
            state="not ready"
        fi
        printf '%-12s %-16s %s\n' "$name" "$addr" "$state"
    done <"$tmp"
    ;;

wait)
    rc=0
    # Into a file rather than a pipe: a `while` on the right of a pipe
    # runs in a subshell, so rc would be discarded and this would report
    # success for a host that never came up.
    tmp_wait="$(load_targets "$@")"
    trap 'rm -f "$tmp_wait"' EXIT
    while IFS="$(printf '\t')" read -r name addr; do
        [ -n "$name" ] || continue
        echo "waiting for $name ($addr)"
        wait_for_ready "$name" "$addr" || rc=1
    done <"$tmp_wait"
    exit $rc
    ;;

facts)
    tmp="$(load_targets "$@")"
    trap 'rm -f "$tmp"' EXIT
    while IFS="$(printf '\t')" read -r name addr; do
        [ -n "$name" ] || continue
        echo "=== $name ($addr)"
        # shellcheck disable=SC2086
        ssh $SSH_OPTS "root@$addr" 'cat /var/lib/halite-lab/facts 2>/dev/null || echo "no facts: the bootstrap did not get far enough"'
        echo
    done <"$tmp"
    ;;

ssh)
    [ $# -eq 1 ] || { echo "ssh takes exactly one distro name" >&2; exit 2; }
    # Through the file rather than `$(selected_targets ... | cut)`: a
    # command substitution is a subshell too, so the "no state" exit was
    # lost here as well and this went on to run `ssh root@` with an empty
    # address, which fails with a name resolution error naming nothing.
    tmp="$(load_targets "$1")"
    trap 'rm -f "$tmp"' EXIT
    addr="$(cut -f2 <"$tmp")"
    if [ -z "$addr" ]; then
        echo "no address for '$1' in the current state" >&2
        exit 1
    fi
    # shellcheck disable=SC2086
    exec ssh $SSH_OPTS "root@$addr"
    ;;

test)
    failed=""
    passed=""
    # The loop body runs in this shell rather than a pipeline subshell,
    # so that `failed` survives it -- a pipe into `while` would lose
    # every result and report success.
    tmp_targets="$(load_targets "$@")"
    out="$(mktemp)"
    trap 'rm -f "$tmp_targets" "$out"' EXIT

    while IFS="$(printf '\t')" read -r name addr; do
        [ -n "$name" ] || continue
        echo
        echo "################ $name ($addr)"

        if ! wait_for_ready "$name" "$addr"; then
            failed="$failed $name(bootstrap)"
            continue
        fi

        echo "---- what this machine is"
        # shellcheck disable=SC2086
        ssh $SSH_OPTS "root@$addr" 'cat /var/lib/halite-lab/facts'

        echo "---- shipping the tree"
        # `git archive HEAD` sends committed files only. A dirty tree is
        # the common way to test something other than what is in front
        # of you, so it is named rather than silently skipped.
        if [ -n "$(git -C "$REPO_ROOT" status --porcelain 2>/dev/null)" ]; then
            echo "NOTE: the working tree has uncommitted changes, and HEAD is what ships."
        fi
        # The tree goes over as a tar stream rather than with rsync,
        # because rsync is a package that has to be installed on the far
        # side and tar is on every one of these images. git archive sends
        # only tracked files, which keeps bin/, vendor caches and any
        # local state out of it.
        # REMOTE_DIR is expanded here on purpose; the far side has no such
        # variable. SC2086 is the deliberately unquoted SSH_OPTS.
        # shellcheck disable=SC2086,SC2029
        if ! git -C "$REPO_ROOT" archive --format=tar HEAD |
            ssh $SSH_OPTS "root@$addr" "rm -rf $REMOTE_DIR && mkdir -p $REMOTE_DIR && tar -C $REMOTE_DIR -xf -"; then
            echo "FAIL $name: the tree could not be shipped"
            failed="$failed $name(ship)"
            continue
        fi

        host_failed=""

        echo "---- build"
        # shellcheck disable=SC2086,SC2029
        ssh $SSH_OPTS "root@$addr" \
            "cd $REMOTE_DIR && PATH=\$PATH:/usr/local/go/bin CGO_ENABLED=0 go build ./..." ||
            host_failed="$host_failed build"

        echo "---- unit suite"
        # Output to a file, then filter, rather than piping ssh into
        # grep. `go test`'s status is the one that matters, and in a
        # pipeline the shell reports the *last* command's -- so a red
        # suite piped into `grep` would be recorded as a pass. The far
        # side cannot do the filtering either: PIPESTATUS is bash, and
        # Alpine's shell is ash.
        # shellcheck disable=SC2086,SC2029
        if ! ssh $SSH_OPTS "root@$addr" \
            "cd $REMOTE_DIR && PATH=\$PATH:/usr/local/go/bin CGO_ENABLED=0 go test -count=1 ./..." \
            >"$out" 2>&1; then
            host_failed="$host_failed unit"
        fi
        grep -vE '^ok|no test files' "$out" | head -40

        echo "---- live suite (root, HALITE_SYSTEM_LIVE=1)"
        # shellcheck disable=SC2086,SC2029
        if ! ssh $SSH_OPTS "root@$addr" \
            "cd $REMOTE_DIR && PATH=\$PATH:/usr/local/go/bin CGO_ENABLED=0 HALITE_SYSTEM_LIVE=1 go test -count=1 -v -run TestLive ./internal/builtin/" \
            >"$out" 2>&1; then
            host_failed="$host_failed live"
        fi
        # SKIP lines are kept deliberately. A live test that skipped is
        # not a live test that ran, and on these rows the skips are the
        # report: they name what this distribution could not be asked.
        grep -E '^(--- (PASS|FAIL|SKIP)|FAIL|ok)' "$out" | head -60

        if [ -n "$host_failed" ]; then
            echo "FAIL $name:$host_failed"
            failed="$failed $name($(echo "$host_failed" | sed 's/^ //;s/ /,/g'))"
        else
            echo "PASS $name"
            passed="$passed $name"
        fi
    done <"$tmp_targets"

    echo
    echo "================ lab summary"
    echo "passed:${passed:- none}"
    echo "failed:${failed:- none}"
    [ -z "$failed" ]
    ;;

untaint)
    # Vultr's API 404s on `GET /instances/<id>/backup-schedule` for an
    # instance it has created but not finished registering, and the
    # provider calls it unconditionally in Read, straight after Create:
    #
    #   Error: error getting backup schedule: {"error":"Invalid
    #   instance-id.","status":404}
    #
    # The instance is fine -- ours was `active`, answering SSH and
    # running its bootstrap minutes later -- but terraform cannot tell a
    # failed create from a failed read after a create, so it marks the
    # resource tainted and the next apply would destroy a healthy machine
    # and build another.
    #
    # Untainting every instance is safe *here* because taint is not what
    # this lab trusts for health: `/var/lib/halite-lab/ready` is, it is
    # written only on a complete bootstrap, and `wait` and `test` both
    # refuse a host that lacks it. A genuinely broken instance therefore
    # still fails, loudly, at the point where it would have been tested.
    #
    # `untaint` is a local state operation and needs no API key.
    found=0
    for addr in $(tofu -chdir="$TOFU_DIR" state list 2>/dev/null | grep '^vultr_instance\.node\['); do
        if tofu -chdir="$TOFU_DIR" untaint "$addr" 2>/dev/null; then
            echo "untainted $addr"
            found=$((found + 1))
        fi
    done
    if [ "$found" -eq 0 ]; then
        echo "nothing was tainted."
    fi
    ;;

*)
    usage
    ;;
esac
