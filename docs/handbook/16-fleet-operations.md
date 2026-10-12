# Configuration management at scale

<!-- abstract: Running a tree against a whole fleet: how changes flow from git to machines, scheduling, drift reports, rollouts, promotion and failure handling. -->
<!-- kind: guide -->
<!-- index: configuration management; gitops; change workflow; continuous convergence; scheduled highstate; schedule; cron; splay; startup_states; drift report; compliance; canary; rollout; promotion; staging; failure triage; check-tree; halite.node -->

The earlier chapters taught the parts: a tree, the hub that serves it,
the command that applies it. This chapter is how they combine into an
operation you can run every day with a team: how a change travels from a
text edit to a thousand machines, what keeps them converged between
changes, and what you do when it goes wrong.

## Two ways a state reaches a machine

| | Pull | Push |
|---|---|---|
| Who starts it | The **node**, on its own schedule | An **operator**, or a reactor, from the hub |
| Command | `schedule:` in `node.yaml`, or a timer calling `halite-node state apply` | `halite-hub run <target> state.apply` |
| Good for | Continuous convergence; drift repair; machines that must keep working with the hub down | Deliberate changes; urgent fixes; rollouts with batches and safe limits |
| Cost | Every node runs the same schedule; spread it with `splay` | You decide when; you must remember to |

Use **both**. A pull schedule is the safety net that repairs drift within
the hour; push is how you roll a change out now, carefully, rather than
waiting for the schedule to find it.

## The change workflow

The tree is the source of truth, and the process of changing it is the
same as for any software:

```
 edit ─▶ review ─▶ check ─▶ merge ─▶ hub serves ─▶ rehearse ─▶ canary ─▶ roll out ─▶ verify
  git     PR       CI        main     gitfs         --test      --subset   --batch     doctor / drift
```

1. **Edit** the tree on a branch. A state change, a template change or a
   pillar change is a text diff.
2. **Review** it. Because the tree is declarative, a reviewer can read
   what a change *means* without running it.
3. **Check** it in CI, mechanically (below).
4. **Merge** to the branch the hub serves. If the hub has gitfs with
   `gitfs_verify_signatures: true`, only a signed commit is served
   ([chapter 10](10-top-and-files.md)).
5. **Rehearse** against the fleet: `state.apply --test`. Read the report.
6. **Canary**: apply to one or two machines at random.
7. **Roll out** in batches with a safe limit.
8. **Verify** that the fleet is converged, with a second rehearsal.

### Check in CI

Two checks catch most mistakes before anything is applied. The first
compiles the tree, for every platform you run and every kind of node you
have, without changing anything. The script ships with the handbook and
was run against the example tree:

<!-- example: handbook/ci/check-tree.sh -->
```sh
#!/bin/sh
# check-tree.sh STATES PILLAR
#
# Compile a tree for every platform you run and every kind of node you have,
# without changing anything. Run it in CI on every change to the tree.
#
# The platform is chosen by overriding grains in a throwaway configuration
# file, which is how one Linux CI machine can compile the FreeBSD half of the
# tree. The node ID is chosen with --id, so the top file assigns the states
# that node would really get.
set -eu

states=$1
pillar=$2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0

for platform in Debian:Linux:Debian RedHat:Linux:RedHat FreeBSD:FreeBSD:FreeBSD; do
    os_family=${platform%%:*}
    rest=${platform#*:}
    kernel=${rest%%:*}
    os=${rest#*:}
    cfg="$tmp/$os_family.yaml"
    printf 'grains:\n  kernel: %s\n  os_family: %s\n  os: %s\n' \
        "$kernel" "$os_family" "$os" > "$cfg"

    for id in web1.example.com db1.example.com; do
        if halite-node state show_lowstate --local \
            --file-root "$states" --pillar-root "$pillar" \
            --root "$tmp" --config "$cfg" --id "$id" --out quiet 2> "$tmp/err"; then
            echo "ok    $os_family $id"
        else
            echo "FAIL  $os_family $id"
            sed 's/^/      /' "$tmp/err"
            failed=1
        fi
    done
done

exit "$failed"
```

Run on the web-server tree it prints

```
ok    Debian web1.example.com
ok    Debian db1.example.com
ok    RedHat web1.example.com
ok    RedHat db1.example.com
ok    FreeBSD web1.example.com
ok    FreeBSD db1.example.com
```

and with a function name misspelt (`pkg.instaled`) it fails for the nodes
that receive that state and names the file and line:

```
FAIL  Debian web1.example.com
      halite: state compilation failed with 1 error(s):
        …/webserver/nginx.sls:4:3 [nginx]: the pkg state module has no function
        "instaled"; it provides installed, latest, purged, removed
ok    Debian db1.example.com
```

The second check is the **idempotency test** of
[chapter 4](04-idempotency.md): apply twice in a disposable machine and
require the second run to exit `2`. Together they are the minimum bar for
merging.

## Keeping the fleet converged

### Scheduling a highstate on the node

The node has its own scheduler ([chapter 23](23-scheduler-and-returners.md)).
In `node.yaml`:

```yaml
schedule:
  nightly_highstate:
    function: state.apply
    cron: '17 3 * * *'
    splay: 900
    maxrunning: 1
    return_job: True
```

Verified on a lab node, this is what the node reports:

```
nightly_highstate:
    function: state.apply
    cron: 17 3 * * *
    splay: 15m0s
    return_job: True
    enabled: True
    timezone: Local
    next_fire_time: 2026-10-12T03:17:00-07:00
```

- **`splay: 900`** adds a random delay of up to 15 minutes, so a thousand
  machines do not hit the hub in the same second. Always use it.
- **`maxrunning: 1`** prevents a second run starting while one is still
  going.
- **`catchup: true`** runs a missed job once when the node starts, for a
  machine that was off at 03:17.

For a convergence loop you may prefer every 30 minutes:
`every: 30m` with `splay`.

The machine's own scheduler does the job equally well, and there is an
argument for leaving it there: one less thing in the agent, and every
operator can read a crontab. `contrib/systemd/halite-highstate.timer`
runs `halite-node state apply --local` with `RandomizedDelaySec`, and the
service carries `SuccessExitStatus=0 2` so a converged run is not a failed
unit; on FreeBSD, `contrib/rc.d/halite_highstate` and a cron line do the
same. Use the built-in scheduler when you want the schedule to travel with
the node, to change without a restart, or to catch up after an outage.

### Converging at boot

`startup_states` in `node.yaml` makes a node run the highstate (or named
SLS files) when the agent starts. Turning it on means a reboot converges
the machine, which is usually what you want and is occasionally a surprise
at 3 a.m.

### Seeing the whole fleet

The hub's view of "is everything as described?" is one command:

```sh
halite-hub run '*' state.apply --test --out json | drift-report.sh
```

Here `drift-report.sh` is a ten-line filter, also in the examples:

<!-- example: handbook/ci/drift-report.sh -->
```sh
#!/bin/sh
# drift-report.sh
#
# Read the JSON that a fleet-wide rehearsal prints on standard input and list
# every state that would change something, one per line:
#
#     node <TAB> state ID <TAB> what it would do
#
# Usage:
#     halite-hub run '*' state.apply --test --out json | drift-report.sh
#
# In test mode a state's result is null exactly when a real run would change
# it, so the report is empty on a converged fleet. A node that failed to
# compile or did not answer has no per-state entries and is not listed here:
# check halite-hub's exit status as well (1 is a failure, 3 is no answer).
exec jq -r '
    to_entries[]
    | .key as $node
    | .value
    | to_entries[]
    | select(.value.result == null)
    | [$node, .value.__id__, .value.comment]
    | @tsv'
```

Run on the lab after one machine's file was edited by hand and another's
directory was deleted:

```
db1.lab   playground_dir        The directory /tmp/halite-lab/db1.lab would be created.
db1.lab   playground_motd       /tmp/halite-lab/db1.lab/motd would be created: its contents, its mode.
db1.lab   playground_changelog  The command "echo …" would be run.
web1.lab  playground_motd       /tmp/halite-lab/web1.lab/motd would be updated: its contents.
web1.lab  playground_changelog  The command "echo …" would be run.
```

Five lines naming two machines and exactly what differs. An empty report
is a converged fleet. Run it on a timer, and alert when it is not empty for
longer than your convergence interval; that is a *compliance* check that
needs no agent beyond the one you already have.

The exit status of the `halite-hub run` in the pipeline still tells you
about nodes that did not answer (`3`) or failed (`1`). Check it too; an
empty report from a node that never answered is not a clean bill of
health.

## Rolling a change out

This is [chapter 14](14-remote-execution.md)'s batches, applied to a
tree change:

```sh
# 1. What would change, everywhere?
halite-hub run '*' state.apply --test --out json | drift-report.sh

# 2. One random web server.
halite-hub run 'web*' state.apply --subset 1

# 3. Check it, the way you would check it by hand.
halite-hub run 'web*' cmd.run 'curl -fsS localhost/healthz' --subset 1

# 4. The rest, a quarter at a time, stopping at two failures.
halite-hub run 'web*' state.apply --batch 25% --batch-wait 30s --batch-safe-limit 2

# 5. Confirm convergence.
halite-hub run '*' state.apply --test --out json | drift-report.sh
```

When one step needs to wait for a health check before the next batch
starts, or needs to take machines out of a load balancer, that is the point
at which to move from this shell script to an **orchestration**
([chapter 21](21-orchestration.md)), which can do all of it with rollback
and records every step.

## Staging and promotion

Two common ways to separate "being tried" from "production":

**Environments.** Give `staging` its own roots (or its own git branch) and
select it for a run: `halite-hub run 'web*' state.apply --env staging
--test`. Staging roots can fall through to the production ones, holding only
what differs ([chapter 10](10-top-and-files.md)). Promotion is merging the
branch.

**Separate hubs.** A staging hub with its own nodes and CA, serving a
different branch. It costs a second hub and buys a complete separation of
identity: nothing on the staging network can reach production, and a
compromised staging node has no path to production secrets.

Either way, the rule is the same: *the fleet follows what the hub serves*,
so the thing to control is what the hub serves, and the process by which
it changes.

## When something goes wrong

A run across many nodes fails in a small number of ways. Work from the
outside in.

**1. Did every node answer?** Exit `3`, or `dispatched, N outstanding` in
`halite-hub jobs list`, means some did not. `halite-hub jobs missing <jid>`
names them. They are off, disconnected or wedged; `halite-hub runner
manage.status` says which nodes are connected.

**2. Did every node that answered, succeed?** Exit `1`. The output marks
failures; for a quick list:

```sh
halite-hub run 'web*' state.apply --out json |
  jq -r 'to_entries[] | .key as $n | .value | to_entries[]
         | select(.value.result == false) | [$n, .value.__id__, .value.comment] | @tsv'
```

**3. What does the failing node say?** Compare with a rehearsal on the
node itself, which shows the same thing with full diagnostics:

```sh
halite-node doctor
halite-node state apply --test --log-level debug --log-fmt console
```

**4. Is it the change or the machine?** If the same state fails on every
node, it is the change: stop (`--batch-safe-limit` should have stopped it),
revert the commit and reapply. If it fails on one, it is that machine:
`doctor`, then look at the first failing state's comment.

**5. Revert by reverting.** The tree is in version control, so the undo
button is `git revert`. Because every state is idempotent and declarative,
applying the previous version returns the fleet to the previous
configuration. What a revert cannot undo are actions with side effects in
the world, such as a database migration run by a `cmd.run`. Guard those
and think about them before the change, not after.

## Managing halite itself with halite

The agent's own configuration is a file like any other. The example tree
(`contrib/examples/tree`) manages `node.yaml` (including its schedule),
`hub.yaml`, `policy.yaml`, `api.yaml`, certificate renewal, and signed
extension publishing from pillar, restarting the agent a little after a
change so it does not stop the very job that is changing it. It compiles
for FreeBSD and Linux on every change to the project. Its README says
what has and has not been run: the restart mechanism, the hub restarting on
a configuration change and the signer's push are compiled, not yet
exercised. Copy it as a starting point and read that README first.

## Key ideas

- **Pull** (the node's own schedule) is the safety net; **push** (an
  operator or reactor running `state.apply`) is the deliberate change. Use
  both.
- Treat the tree as software: edit, review, **check in CI**, merge, serve.
- CI = **compile for every platform and role** + **the idempotency test**.
- `state.apply --test --out json | drift-report.sh` is a **fleet-wide
  compliance report** with no extra agent.
- Roll out with **rehearse → canary → batches with a safe limit →
  confirm**; move to orchestration when you need steps between batches.
- Triage failures outside-in: did all answer; did all succeed; is it the
  change or the machine; **revert by reverting**.

Next: [machines with no agent](17-agentless.md).
