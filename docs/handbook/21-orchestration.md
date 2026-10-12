# Orchestration

<!-- abstract: Multi-step, multi-machine procedures that run on the hub: step types, ordering and rollback with requisites, batches, waiting for events, rehearsal, and resuming a failed run. -->
<!-- kind: guide -->
<!-- index: orchestration; orch run; orch lint; orch show; orch list; orch resume; salt.state; salt.function; salt.runner; salt.wheel; salt.wait_for_event; salt.parallel; onfail; rollback; rolling update; batch; tolerate_failures; fail_minions; approval gate; state.orchestrate -->

Configuration management brings each machine to a state *independently*.
Some jobs are not independent. Taking a web server out of rotation, then
upgrading it, then checking it, then putting it back, in that order, a few
at a time, stopping if two fail, with a way back, is a **procedure**. It
spans machines and has an order and an end.

**Orchestration** is how halite runs procedures. It runs on the **hub**,
and it is written in the same declarative language as everything else:
the same SLS files, the same IDs, the same requisites, with a few special
states that act on the fleet rather than on a machine.

```sh
halite-hub orch run rolling-update --pillar '{"version": "1.2"}'
```

## A first orchestration

This is the example orchestration that ships with the handbook,
`contrib/examples/handbook/automation/orchestration/rolling-update.sls`. It
was linted, rehearsed and run against a three-node lab:

<!-- example: handbook/automation/orchestration/rolling-update.sls -->
```yaml
# Roll a new version of the web tier, a node at a time, and record a
# rollback if a step fails.
#
# Rehearse:  halite-hub orch run rolling-update --test --pillar '{"version": "1.2"}'
# Run:       halite-hub orch run rolling-update --pillar '{"version": "1.2"}'
#
# An orchestration sees the `pillar` the caller passed and nothing else:
# the hub is not a node and has no pillar of its own.
{% set version = pillar['version'] %}

announce_start:
  salt.runner:
    - name: event.send
    - kwarg:
        tag: halite/audit/rollout_started
        data:
          version: "{{ version }}"

rehearse_web:
  salt.state:
    - tgt: 'web*'
    - highstate: True
    - test: True
    - require:
      - salt: announce_start

apply_web:
  salt.state:
    - tgt: 'web*'
    - highstate: True
    - batch: 1
    - batch_safe_limit: 1
    - require:
      - salt: rehearse_web

verify_web:
  salt.function:
    - name: test.ping
    - tgt: 'web*'
    - require:
      - salt: apply_web

roll_back:
  salt.function:
    - name: cmd.run
    - tgt: 'web*'
    - arg:
      - echo "rolling back {{ version }}" >> /tmp/halite-rollback.log
    - onfail:
      - salt: apply_web
      - salt: verify_web

announce_done:
  salt.runner:
    - name: event.send
    - kwarg:
        tag: halite/audit/rollout_finished
        data:
          version: "{{ version }}"
    - require:
      - salt: verify_web
```

Read it as a script of six **steps**. Each is a state whose module is
`salt` and whose function says what kind of step it is. They run in order,
and `require` and `onfail` work exactly as in a highstate
([chapter 6](06-requisites.md)): `verify_web` runs only if `apply_web`
succeeded; `roll_back` runs **only if** a step it names failed; and
`announce_done` runs only at the end of a good run.

```
$ halite-hub orch run rolling-update --pillar '{"version":"1.2"}'
20261011T190632153119  complete
  ok   announce_start     salt.runner     job 20261011T190632163629
       event.send ran on the hub.
  ok   rehearse_web       salt.state      job 20261011T190632194620 on 2 node(s)
       state.apply ran on 2 node(s).
  ok   apply_web          salt.state      job 20261011T190632465237 on 2 node(s)
       state.apply ran on 2 node(s).
  ok   verify_web         salt.function   job 20261011T190632742796 on 2 node(s)
       test.ping ran on 2 node(s).
  skip roll_back          salt.function
       This state was skipped because none of its onfail requisites failed.
  ok   announce_done      salt.runner     job 20261011T190633012130
       event.send ran on the hub.
```

The orchestration is itself a **run with a JID**, a timeline, and one
ordinary job per step (each with its own JID), so everything it did is in
the job cache.

An orchestration template sees the `pillar` the caller passed
(`--pillar '{"version":"1.2"}'`) and nothing else: the hub is not a node
and has no pillar of its own. It can see grains of nobody.

## The step types

| Step | Does |
|---|---|
| `salt.state` | Apply states on the matching nodes and wait for the returns. With `highstate: True`, or no `sls`, it is a highstate; with `sls: [a, b]`, those files. |
| `salt.sls` | Apply the named SLS files. |
| `salt.highstate` | Apply the highstate. |
| `salt.function` | Run an **execution function** (`name:`) on the matching nodes; `arg` and `kwarg` are its arguments. |
| `salt.runner` | Run a **hub runner** (`name:`); `arg` and `kwarg`. |
| `salt.wheel` | Run a hub management function. |
| `salt.wait_for_event` | Block until an event whose tag matches arrives, or the step's `timeout` passes. |
| `salt.parallel` | **Not built**; refused when the step runs (and `orch lint` does not catch it). Steps run one at a time. |

The word `salt` in these names is the module name, retained so that an
existing orchestration runs unchanged. It is not a reference to anything
else.

Options common to `salt.state`, `salt.sls`, `salt.highstate` and
`salt.function`:

| Option | Meaning |
|---|---|
| `tgt`, `tgt_type` | Which nodes, and the kind of target (`G`, `C`, …; default glob). |
| `batch`, `batch_safe_limit` | Run against N nodes or N% at a time; stop after this many have failed. |
| `subset` | Run against a random N of the matched set. |
| `tolerate_failures` | Node globs whose failure does not fail the step. The older spelling `fail_minions` is accepted. |
| `test` | Run this step in test mode (state steps only). |
| `saltenv` | The environment the job names. |
| `pillar`, `pillarenv` | Pillar overrides for this step (state steps). |
| `timeout` | Seconds before the step is given up on (a state option available on every step). |
| `queue` | Hold the step on the hub's durable queue. **Not built**; refused by name. |

The batching options are the ones from
[chapter 14](14-remote-execution.md), now inside a procedure: `apply_web`
above does one node at a time and stops at the first failure.

Every step is authorized **twice**: once as the orchestration (is this
principal allowed to run `state.orchestrate` at all?) and once as the job it
dispatches (may it run `state.apply` on `web*`?). Permission to run an
orchestration is not permission to run whatever it happens to name.

## Running, rehearsing and checking

| Command | Does |
|---|---|
| `halite-hub orch lint <sls> [--pillar '<json>']` | Compile the file and list its steps without running anything. |
| `halite-hub orch run <sls> [--pillar '<json>'] [--test]` | Run it. |
| `halite-hub orch show <jid>` | The timeline of a run: each step, its status, the job it made. |
| `halite-hub orch list [--limit N]` | Recent runs. |
| `halite-hub orch resume <jid> --from <step>` | Restart a failed run from a named step. |

`orch lint` catches syntax and unknown step parameters, one error per
problem:

```
rolling-update.sls:9:3 [announce_start]: salt.runner: argument "data": is not a
parameter of this function
```

(The author had written `data:` where `salt.runner` takes `kwarg:`.) It
does not catch everything; for example, a `salt.parallel` step is only
refused when it runs.

**`--test`** rehearses. State steps are sent out with `test` set, so they
report what they would change, because a state honours test mode by contract
([chapter 4](04-idempotency.md)). Function and runner steps are **not
dispatched at all** in a test run; they say what they would have done:

```
$ halite-hub orch run rolling-update --test --pillar '{"version":"1.2"}'
20261011T190627625174  complete
  ok   announce_start     salt.runner
       Would call the runner event.send.
  ok   rehearse_web       salt.state      job … on 2 node(s)
       state.apply ran on 2 node(s).
  ok   apply_web          salt.state      job … on 2 node(s)
       state.apply ran on 2 node(s).
  ok   verify_web         salt.function
       Would run test.ping on the nodes matching "web*".
  skip roll_back          salt.function
       This state was skipped because none of its onfail requisites failed.
```

This is deliberate. `salt.function` runs whatever it names, and finding out
what a test run would do by actually running it is how a test run becomes a
deployment. Read the rehearsal as: *the state steps ran in test mode and
the rest would have run as listed*.

`orch run` exits `0` when every step succeeded and `1` when one failed, and
**prints the timeline either way**, because the next command, `orch resume
--from <step>`, needs a step name.

## Failure, rollback and resume

Make the verification step fail (here by checking for a file that does not
exist) and run it:

```
$ halite-hub orch run rolling-fail --pillar '{"version":"1.3"}'
20261011T190639505610  failed
  ok   announce_start     salt.runner
  ok   rehearse_web       salt.state      job … on 2 node(s)
  ok   apply_web          salt.state      job … on 2 node(s)
  FAIL verify_web         salt.function   job 20261011T190640348890 on 2 node(s)
       cmd.run: 2 of 2 node(s) failed: web1.lab, web2.lab.
       failed: web1.lab, web2.lab
  ok   roll_back          salt.function   job 20261011T190640620952 on 2 node(s)
       cmd.run ran on 2 node(s).
  skip announce_done      salt.runner
       This state was skipped because verify_web (salt.function) failed.
orchestration 20261011T190639505610 failed; `orch show 20261011T190639505610` has the timeline
```

Look at what happened. `verify_web` failed. Because `roll_back` named it in
`onfail`, **the rollback ran**. Because `announce_done` required it, the
announcement was skipped and said why. The log of the rollback is on the
nodes (`rolling back 1.3`, once per node), and the exit status is `1`.

Now you fix the cause (in the lab, create the file the check wanted), and
rather than starting over, **resume from the failed step**:

```
$ halite-hub orch resume 20261011T190639505610 --from verify_web --pillar '{"version":"1.3"}'
20261011T190640964052  complete
resumed 20261011T190639505610 from step verify_web
  skip announce_start     salt.runner
       Carried forward from 20261011T190639505610: event.send ran on the hub.
  skip rehearse_web       salt.state
       Carried forward from 20261011T190639505610: state.apply ran on 2 node(s).
  skip apply_web          salt.state
       Carried forward from 20261011T190639505610: state.apply ran on 2 node(s).
  ok   verify_web         salt.function   job 20261011T190640975512 on 2 node(s)
       cmd.run ran on 2 node(s).
  skip roll_back          salt.function
       This state was skipped because none of its onfail requisites failed.
  ok   announce_done      salt.runner     job 20261011T190641241655
       event.send ran on the hub.
```

Steps before the named one are **carried forward** from the failed run, not
repeated. This is what makes a long deployment usable in practice: a
failure at step nine of ten does not mean redoing the first eight. Two
safeguards: a real resume of a `--test` run is refused (none of its steps
happened), and so is a resume whose `--pillar` is not the override the run
was started with (the carried-forward steps were made under different data).

Every run is a first-class record: `orch list` shows them, `orch show <jid>`
the timeline, and the job each step started is in `halite-hub jobs`.

## Waiting for the outside world

`salt.wait_for_event` turns an orchestration from something that only
pushes work outward into something that can coordinate with a person or a
system. This one, `approval-gate.sls`, pauses until someone says go:

<!-- example: handbook/automation/orchestration/approval-gate.sls -->
```yaml
# Pause a procedure until something outside the fleet says to go on.
#
# The step waits for an event whose tag matches, counting only events that
# arrive AFTER it starts waiting, and fails if none comes within `timeout`
# seconds. Whoever approves -- a person, a chat bot, a ticketing system
# through the API's webhook -- sends the event:
#
#     halite-hub runner event.send tag=halite/audit/approved data='{"by":"ed"}'
request_approval:
  salt.runner:
    - name: event.send
    - kwarg:
        tag: halite/audit/approval_requested
        data:
          change: "widen the connection pool"

wait_for_approval:
  salt.wait_for_event:
    - name: halite/audit/approved
    - timeout: 120
    - require:
      - salt: request_approval

proceed:
  salt.function:
    - name: test.echo
    - tgt: 'web*'
    - arg:
      - approved, carrying on
    - require:
      - salt: wait_for_approval
```

Run in the lab, with an approval sent four seconds after it started:

```
20261011T190654254258  complete
  ok   request_approval    salt.runner     job 20261011T190654262810
       event.send ran on the hub.
  ok   wait_for_approval   salt.wait_for_event
       An event matching "halite/audit/approved" arrived: halite/audit/approved.
  ok   proceed             salt.function   job 20261011T190658256828 on 2 node(s)
       test.echo ran on 2 node(s).
```

and with nobody approving and a 3-second timeout:

```
20261011T190703308229  failed
  ok   request_approval    salt.runner
  FAIL wait_for_approval   salt.wait_for_event
       No event matching "halite/audit/approved" arrived within the timeout.
  skip proceed             salt.function
       This state was skipped because wait_for_approval (salt.wait_for_event) failed.
```

The wait counts only events that arrive *after* it begins, so history cannot
satisfy it. The approver can be a person with `halite-hub runner
event.send`, a chat integration, or an external system posting to the API's
webhook ([chapter 27](27-http-api.md)). The same step can wait for a node
to report back after a reboot (`halite/node/<id>/start`), or for a
deployment to announce `halite/node/*/deploy/finished`.

## Starting orchestrations automatically

An orchestration is a runner (`state.orchestrate`), so a reactor can start
one, and the API can ([chapter 27](27-http-api.md)):

```yaml
# a reaction
start_rollout:
  runner.state.orchestrate:
    - args:
        sls: rolling-update
        pillar:
          version: {{ data['version'] }}
```

Make sure the principal that reaction runs as holds the
`state.orchestrate` runner permission, and restrict which orchestrations it
may run in the policy ([chapter 26](26-authorization.md)).

## Patterns

**Rolling change.** The example above: rehearse everywhere, apply in
batches with a safe limit, verify, roll back on failure. Replace
`test.ping` with the real health check (an `http.query` function, a
command that talks to the service).

**Drain, change, return.** Wrap the change between a step that tells your
load balancer to stop sending traffic (a `salt.function` on the balancer
nodes) and one that puts the node back, with the second requiring the
change and the rollback `onfail`-ing it.

**Gate on a human or a system.** `salt.wait_for_event` after a rehearsal,
before the apply.

**Fan-in.** A reboot or a long task started on many nodes, followed by a
wait for each node's `halite/node/<id>/start` event.

**Keep orchestrations small.** One procedure, one file. Share common steps
the way you share states: with `include` and templates. Pass differences
as pillar on the command line, not by editing the file.

## Key ideas

- **Orchestration** runs procedures on the **hub**, written as SLS with
  `salt.*` steps and the usual requisites; `onfail` expresses a rollback.
- Steps: `salt.state`/`sls`/`highstate`, `salt.function`, `salt.runner`,
  `salt.wheel`, `salt.wait_for_event`. (`salt.parallel` and `queue` are not
  built.)
- Batching, safe limits, subsets and `tolerate_failures` apply per step.
- `orch run --test` runs state steps in test mode and **does not dispatch**
  function steps; each step is authorized twice.
- `orch resume --from <step>` continues a failed run without repeating the
  steps before it.
- `salt.wait_for_event` lets a procedure wait for a person or a system.

Next: [runners and the mine](22-runners-and-mine.md).
