# Orchestration

Some changes have to happen in an order that spans hosts: drain the load
balancer, upgrade the backends, put the load balancer back. `halite run`
targets one set of hosts at a time and does not know about "after".
Orchestration does.

```sh
halite orchestrate deploy
halite orchestrate deploy -test
```

## An orchestration file

Orchestrations live in their own tree, `orch/` beside the state tree by
default (`-orch-root` on the control plane). Each file is an ordinary SLS
file whose states are `halite.run` steps:

```yaml
# orch/deploy.sls
drain_lb:
  halite.run:
    - target: 'lb*'
    - kind: call
    - fn: cmd.run
    - args:
        name: lb-drain

upgrade_web:
  halite.run:
    - target: 'web*'
    - kind: state.apply
    - sls:
      - web.nginx
    - require:
      - halite: drain_lb

restore_lb:
  halite.run:
    - target: 'lb*'
    - kind: call
    - fn: cmd.run
    - args:
        name: lb-restore
    - require:
      - halite: upgrade_web
```

A step takes the same fields as `halite run`:

| Field | Meaning |
|---|---|
| `target` | which agents, in the usual target language (required) |
| `kind` | `state.highstate` (default), `state.apply`, `call`, `grains`, `pillar` |
| `sls` | sls names, for `state.apply` |
| `fn`, `args` | the function and arguments, for `call` |
| `test` | dry run this step |

## Ordering is the SLS ordering you already know

An orchestration file is compiled by the same pipeline as any other SLS
file and executed by the same engine. `require` between steps is the same
`require` between states — it just means "those hosts finished, and
succeeded" instead of "that local state ran".

That has consequences worth knowing:

* **A failed step stops what depends on it.** Dependents are reported as
  failed with the blocking step named, exactly as a local requisite
  failure would be.
* `watch`, `onchanges`, and `prereq` work too. A step "changed" if any
  agent reported a change.
* The universal gates apply: `unless`, `onlyif`, and `creates` are
  evaluated on the control plane before a step dispatches.
* Steps with no requisites between them still run in declaration order,
  one at a time. Orchestration is about sequencing; use one step with a
  broad target to do many hosts at once.

## Waiting, and not waiting

A step dispatches, then waits for **every** agent it matched to answer
before the next step begins. Two things end that wait early:

* **A step that matches no online agents fails.** Silently succeeding is
  the dangerous behaviour: a drain step that reached no load balancer must
  not let the upgrade proceed.
* **A step that waits too long fails** (`-orch-step-timeout`, ten minutes
  by default), naming how many agents never answered.

## Following a run

`halite orchestrate` starts the run and follows it, printing each step as
the control plane reports it, and exits non-zero if any step failed.

The run itself happens on the control plane, detached from your request —
an operator hanging up does not abandon a half-finished deploy, and a
fleet-wide deploy does not die to a proxy timeout. Progress also goes on
the event bus:

| Tag | Raised when |
|---|---|
| `halite/orch/<id>/start` | the run begins |
| `halite/orch/<id>/step/<step>` | a step dispatches, and again when it finishes |
| `halite/orch/<id>/done` | the run ends |

```sh
halite events -tag 'halite/orch/**'
```

Steps are dispatched under the identity `orchestrator`, so they are
distinguishable from an operator's own work in logs and events.

## What it is not

* **Not durable.** Like everything else the control plane holds, a run's
  outcome is in memory and lost on restart. A run in flight when the
  control plane stops does not resume.
* **Not parallel across steps.** Steps run one after another. The
  parallelism is within a step, across the agents it targets.
* **Not a place for logic.** There are no conditionals beyond the gates
  and requisites. An orchestration that needs to decide something wants a
  reactor rule, or a state on the host that knows its own situation.
