# Scenarios: putting it together

<!-- abstract: Whole procedures built from the parts: a file that repairs itself, a node that onboards itself, a certificate that raises its own alarm, a rollout that waits for a human. -->
<!-- kind: tutorial -->
<!-- index: self-healing; onboarding; certificate expiry alert; approval gate; scenario; end to end; beacon reactor orchestration; loop prevention; debounce; audit event -->

The parts of the last six chapters are small and each is easy to
understand. Their value is in how they combine. This chapter builds four
whole procedures from them. Each was **run on a small lab fleet** (a hub and
three nodes) and what you see is what happened; where something was not run
the text says so.

| Scenario | Parts used |
|---|---|
| [1. A file that repairs itself](#1-a-file-that-repairs-itself) | beacon → reactor → state → audit event |
| [2. A node that onboards itself](#2-a-node-that-onboards-itself) | start event → reactor → state |
| [3. A certificate that raises its own alarm](#3-a-certificate-that-raises-its-own-alarm) | beacon → bus → event returner → alerting |
| [4. A rollout that waits for a person](#4-a-rollout-that-waits-for-a-person) | orchestration, batches, rollback, wait for event |
| [5. A fleet that reports its own drift](#5-a-fleet-that-reports-its-own-drift) | scheduled rehearsal, drift report |

## 1. A file that repairs itself

**Goal.** The motd of every web server is managed by a state. If anyone
edits it by hand, put it back within seconds, and leave a record.

**Parts.** A `filechanges` **beacon** on the node watches the file. A
**reactor** on the hub turns that event into a `state.apply` of the one SLS
that owns the file. The result is an audit event.

**1. The beacon**, in the node's `node.yaml`:

<!-- example: handbook/automation/node-self-heal-beacon.yaml -->
```yaml
# node.yaml: watch the file a state owns, and report only when it changes.
#
# onchangeonly turns the beacon's readings into "this file changed";
# without it the beacon reports the file every interval.
# disable_during_state_run keeps the repair itself, which rewrites the file,
# from being reported as a fresh change while it is happening.
beacons:
  filechanges:
    - files:
        - /tmp/halite-lab/web1.lab/motd
    - interval: 2
    - onchangeonly: True
    - disable_during_state_run: True
```

**2. The reaction.** It applies exactly one SLS to the node that spoke:

<!-- example: handbook/automation/reactor/self-heal.sls -->
```yaml
# Put a managed file back when somebody edits it by hand.
#
# The beacon on the node saw the file change; this applies the one SLS that
# owns the file, to that node only. Naming the SLS (rather than running the
# whole highstate) keeps the repair small and lets the policy restrict this
# principal to exactly that SLS.
{% set node = data['id'] %}

heal_{{ node }}:
  local.state.apply:
    - tgt: {{ node }}
    - arg:
      - playground
```

**3. The reactor entry**, in `hub.yaml`:

<!-- example: handbook/automation/hub-self-heal.yaml -->
```yaml
# hub.yaml: the reactor entry for self-healing. The beacon on the node that
# feeds it (filechanges, with onchangeonly and disable_during_state_run) is
# in chapter 24.
#
# `debounce` collapses the burst of events one edit can cause into a single
# repair, run once the burst has gone quiet. It is the right control here:
# `dedupe_window` would let the FIRST event through and drop the rest, so an
# edit made a few seconds after another could be dropped with nothing
# arriving later to repair it.
reactor:
  - tag: 'halite/beacon/*/filechanges/tmp/halite-lab/*/motd'
    sls:
      - /etc/halite/reactor/self-heal.sls
    principal: 'reactor:heal'
    debounce: 3s
```

**4. The permission.** The principal may apply that SLS and nothing else.
It cannot run a command, apply a different file or pass pillar:

<!-- example: handbook/automation/hub-self-heal-policy.yaml -->
```yaml
# The self-healing reaction may apply one named SLS, and nothing else, to
# any node. It cannot run a command, apply a different file, or pass pillar.
roles:
  heal:
    - target: '*'
      functions:
        - 'state.apply'
      args:
        'state.apply':
          allow_sls: ['playground']
          deny_kwargs: ['pillar']

bindings:
  - principal: 'reactor:heal'
    roles: ['heal']
```

Check the permission before you rely on it. `policy test` is the same
evaluator the hub uses:

```
$ halite-hub policy test reactor:heal web1.lab state.apply playground
allowed by role "heal" rule 0
$ halite-hub policy test reactor:heal web1.lab state.apply other
denied: other is not in the SLS names this role may apply (playground)
$ halite-hub policy test reactor:heal web1.lab state.apply playground --kwarg pillar=x
denied: state.apply may not be called with "pillar" by this role
```

**What happened.** After starting the hub and node with this configuration,
the file was overwritten by hand and then polled once a second:

```
12:14:27   (edit)
12:14:29: edited by hand
12:14:30: edited by hand
12:14:32: edited by hand
12:14:33: This directory is managed by halite.
12:14:35: This directory is managed by halite.
```

The repair arrived about six seconds after the edit: two seconds for the
beacon's poll, three of debounce, then a state run. The job cache shows what
the system did:

```
20261011T191436993795  state.apply   web1.lab   complete
20261011T191432489717  state.apply   web1.lab   complete
```

**Two** applies for one edit. The first repaired the file. The repair
changed the file, so the beacon noticed *that* change too (it was silent
only *during* the run) and the reaction fired again; the second apply found
nothing to do and did not change the file, so the cycle ended. That is a
loop of length two that terminates, not a runaway: a self-correcting reaction
settles after one echo, because the second apply changes nothing and so the
file does not change again. A reaction that does *not* settle would not be
stopped by the hub: the causality limit (`max_causality_depth`) did not stop a
deliberately self-triggering reaction in testing ([chapter 20](20-reactors.md)),
so a self-healing design has to settle by construction.

**What to take from it.**

- The choices that mattered: `onchangeonly` on the beacon (without it,
  the same reading arrives every two seconds), `debounce` rather than
  `dedupe_window` on the reactor (a dedupe window opened by an earlier event
  held this very repair back for fourteen seconds when the scenario was first
  tried), and a principal that can apply one named SLS.
- Do this for the few files that really must never differ. For everything
  else, the scheduled highstate repairs drift within the hour at no cost.

## 2. A node that onboards itself

**Goal.** When a node connects, check that its configuration matches the
tree, and say so, without an operator remembering to.

**Parts.** The `halite/node/<id>/start` event, which every connecting node
causes; a reaction that rehearses the highstate.

<!-- example: handbook/automation/reactor/node-started.sls -->
```yaml
# When a node connects, rehearse its highstate. Swap `--test` for the real
# thing only after you have watched this run for a while.
{% set node = data['id'] %}

check_{{ node }}:
  local.state.apply:
    - tgt: {{ node }}
    - kwarg:
        test: True
```

with the entry, as the principal `reactor:start` (a role that may call
`state.apply`):

```yaml
reactor:
  - tag: 'halite/node/*/start'
    sls:
      - /etc/halite/reactor/node-started.sls
    principal: 'reactor:start'
```

`reactor.test` first, with a made-up start event:

```
$ halite-hub runner reactor.test tag=halite/node/web1.lab/start 'data={"id":"web1.lab"}'
tag: halite/node/web1.lab/start
matched:
    - halite/node/*/start
reactions:
    -
        id: check_web1.lab
        principal: reactor:start
        kind: local
        fun: state.apply
        tgt: web1.lab
        kwarg:
            test: True
        authorized: allowed by role "starter" rule 0
```

**What happened.** On restarting the hub and the three nodes, each node
reconnected, a start event reached the bus, and one rehearsal per node
appeared in the job cache a moment later:

```
20261011T190346017642  state.apply   web1.lab   complete
20261011T190346009091  state.apply   web2.lab   complete
20261011T190346008454  state.apply   db1.lab    complete
```

**Take-aways.** This runs on *every* connection, including a hub restart
that makes the whole fleet reconnect at once: that is a rehearsal per node,
which is cheap, but would be a real highstate storm if you had taken
`test: True` off. Use `splay`-like control by rate-limiting the reactor
(`rate_limit`), or prefer `startup_states` in `node.yaml`, which has each node
converge itself locally when it starts and needs no reactor. Use the reactor
when you want the **hub** to decide, or to record that the node was seen.

## 3. A certificate that raises its own alarm

**Goal.** Know a month before a certificate on any node expires, in the
alerting system you already have.

**Parts.** The `cert_info` **beacon**; the event bus; **`event_return`** to a
signed webhook.

**1. On the node**:

```yaml
beacons:
  cert_info:
    - files:
        - /etc/ssl/certs/site.pem
    - notify_days: 30
    - interval: 3600
```

**2. On the hub**, ship the beacon's events to the alert receiver:

```yaml
event_return: webhook
event_return_tags: 'halite/audit/**,halite/beacon/*/cert_info/**'
returner_webhook_url: https://alerts.example/halite/events
returner_webhook_secret_file: /etc/halite/returner.secret
returner_webhook_ca_file: /etc/halite/internal-ca.pem
```

**What happened.** With a test certificate of ten days' life and a 30-day
window, the node's beacon fired within seconds of starting and the receiver
(the verifying script from [chapter 23](23-scheduler-and-returners.md))
logged a validly signed event:

```json
{"path": "/events", "record": {
   "_tag": "halite/beacon/db1.lab/cert_info/…/test-cert.pem",
   "_node": "db1.lab",
   "data": {"expires_within_days": 30, "path": "…/test-cert.pem"},
   "_offset": "00000001:…"}}
```

No reactor was involved: the alert is the event, and the receiver turns it
into a ticket or a page.

> The first time this scenario was tried, the beacon put an *error* event on
> the bus instead, because it was calling the certificate function with the
> wrong argument name. That is fixed and has a test, and is a good argument
> for running a scenario end to end before relying on it.

**Take-aways.** `interval: 3600` is plenty for expiry. Remember that
`cert_info` reports every interval while the certificate is inside the
window, so the receiver should deduplicate by path. If you want *halite* to
renew the certificate, add a reactor on the same tag that applies the SLS
that manages it (the `x509` state module, as the `halite.certs` example tree
does).

## 4. A rollout that waits for a person

**Goal.** Roll a change to the web tier one node at a time, stop at the
first failure, roll back if verification fails, and don't begin until a human
has said go.

**Parts.** An **orchestration** with `salt.wait_for_event`, `batch`,
`batch_safe_limit` and `onfail`. The two files are
`approval-gate.sls` and `rolling-update.sls` from
[chapter 21](21-orchestration.md); this combines them.

The sequence a rollout follows:

1. A person or CI job starts it, with the version as pillar:

   ```sh
   halite-hub orch run rolling-update --pillar '{"version": "1.4"}'
   ```

2. It announces on the bus (`halite/audit/rollout_started`), so other
   systems and people see it begin.
3. It rehearses the highstate on `web*`.
4. It applies one node at a time, stopping at the first failure.
5. It verifies. If a step failed, the `roll_back` step runs on the nodes and
   the run is `failed`, with the cause on its timeline.
6. If everything passed, it announces completion.

and the gate, when you want a human in the middle, is a `wait_for_event`
step between 3 and 4 exactly as in `approval-gate.sls`: the approver sends
`halite/audit/approved`, the run proceeds, and with nobody it fails at the
timeout and says so.

A failed run is not the end: fix the cause, then resume from the step that
failed without repeating the earlier ones
(`halite-hub orch resume <jid> --from verify_web`). Every piece of this was
run, including the failure, the rollback and the resume, in
[chapter 21](21-orchestration.md).

## 5. A fleet that reports its own drift

**Goal.** Every morning, know which machines differ from the tree and how.

**Parts.** A scheduled rehearsal on the hub host, and the drift report.

```sh
# crontab on the host that runs operator commands
17 6 * * *  halite-hub run '*' state.apply --test --out json --as reporter \
              | /usr/local/bin/drift-report.sh | mail -E -s "halite drift" ops@example.com
```

(`mail -E` sends nothing on an empty body: a converged fleet is silent.) The
script is `drift-report.sh` from [chapter 16](16-fleet-operations.md); give
the `reporter` certificate a role that may run `state.apply` only with
`--test`-style safety by pairing the schedule with a policy that denies
anything else. An empty report is the goal; a non-empty one lists the node,
the state and what it would change.

> **What has and has not been run.** The crontab line was run on Ubuntu 26.04
> with Postfix (local delivery only), `bsd-mailx`, `jq` and cron, every minute
> in place of 06:17, against two nodes. With a state that would be created,
> cron sent one mail to root whose body was the report, one line per node. After
> the fleet was converged, two further cron ticks sent nothing: `mail -E` was
> silent on the empty report. **Not run:** `--as reporter` (the line was run as
> the first operator), delivery to an address off the machine, and FreeBSD's
> `mail`.

## Designing your own

A recipe that has worked every time:

1. **Say the goal as an event and a response.** "When *X* is seen on a node,
   do *Y* to that node."
2. **Start with the observation.** Configure the beacon alone and read the
   events with `halite-hub event listen --tag 'halite/beacon/**'`. Decide
   whether the events are right before anything reacts to them.
3. **Write the reaction as a rehearsal.** `test: True`, or an `event.send`
   that merely announces. Check it with `reactor.test`.
4. **Write the permission last, narrowly,** from what `reactor.test` says
   it needs, and test it with `policy test`.
5. **Add the controls.** `onchangeonly`, `disable_during_state_run`,
   `debounce`, `rate_limit`.
6. **Make it real, watch it, then forget it.** Run it a week with the
   announcement on. Then take the safety off.
7. **Alert on the reactor's own errors:** `halite/reactor/**` to a returner.

## Key ideas

- The primitives compose: beacon (sense) → bus (carry) → reactor (decide)
  → job or orchestration (act) → audit event (record).
- Every reaction runs as a **named principal** with the **narrowest**
  permission that works, tested with `policy test`.
- **Settle, don't loop**: `onchangeonly`, `disable_during_state_run`,
  `debounce`, `dedupe_window`. Do not count on the causality limit.
- The system's own notifications leave through **returners**, not
  runners.
- **Run it end to end** before relying on it. Two beacons in this build
  had a defect that only an end-to-end run showed.

Next, Part V: the security and identity model behind all of this.
[Trust and identity](25-trust-and-identity.md).
