# Jobs and the job cache

<!-- abstract: What a job is, how it is recorded, how to look one up afterwards, and how long the record lasts. -->
<!-- kind: guide -->
<!-- index: job; jid; return; job cache; jobs list; jobs show; jobs lookup; jobs export; jobs missing; jobs active; jobs resume; jobs kill; jobs prune; job_cache_retention; job_cache_max_size; halite.job/1; halite.ret/1; returns schema; audit trail -->

Every command you send to the fleet becomes a **job**, and the hub records
it. Because the record is written *before* delivery, a caller that
disconnects loses nothing and an incident can be reconstructed: who asked
for what, of which nodes, when, and what each one said.

## Anatomy of a job

A job has:

| Field | Meaning |
|---|---|
| `jid` | The job ID, `YYYYMMDDThhmmssffffff` in UTC: `20261011T185455764041`. It sorts by time, and you can read the date out of it. |
| `fun`, `arg` | The function and its arguments. |
| `target`, `target_kind` | What was asked for (`web1.lab`, `glob`) |
| `nodes` | The node IDs the target resolved to when the job was submitted: the **expected respondents**, so a missing answer is detectable. |
| `submitter` | The principal that asked: `cert:CN=ed`. |
| `created`, `expires` | When it was made and the instant after which no node will run it. |
| `nonce` | Random, recorded by each node so a replay is refused. |
| `offline` | The offline policy it was submitted with. |
| `state` | `dispatched`, `complete`, `aborted`, … |
| `batch` | For a batched run: its size and how far it has got. |
| `delivered` | The nodes that have received it. |

Each node's **return** is its own record, in a schema kept compatible with
the predecessor's so that existing dashboards and returners keep working:

```json
{
  "jid": "20261011T185455764041",
  "id": "web1.lab",
  "fun": "test.echo",
  "fun_args": ["hi"],
  "success": true,
  "retcode": 0,
  "return": "hi",
  "start_time": "2026-10-11T18:54:55.785397676Z",
  "duration_ms": 1,
  "node_version": "0.1.0-7-g8ad761ce275b+8ad761ce275b",
  "schema": "halite.ret/1"
}
```

`success` and `retcode` say whether it worked; `return` carries the value
(for `state.apply`, the whole per-state structure); `node_version` makes
"which version of the agent was this?" answerable after the fact.

## Reading the cache

`halite-hub jobs` is the interface:

| Command | Does |
|---|---|
| `jobs list [--limit N]` | Recent jobs, newest first. |
| `jobs show <jid>` | One job and every return filed against it, readable. |
| `jobs lookup <jid>` | The same, as JSON. |
| `jobs missing <jid>` | The nodes that were sent it and have not answered. |
| `jobs active` | Jobs the hub has not finished delivering. |
| `jobs resume <jid>` | Pick up a batch the hub was part-way through. |
| `jobs kill <jid>` | Stop a job that has not finished. |
| `jobs export <jid>` | The job, every return, and who never answered, as one JSON document. |
| `jobs prune` | Enforce retention now rather than on the hour. |

```sh
halite-hub jobs list --limit 3
```

```
20261011T185455764041  test.echo                web1.lab       complete
20261011T185350641819  sys.doc                  web1.lab       complete
20261011T185350364816  pkg.version              web1.lab       complete
```

```sh
halite-hub jobs show 20261011T184928102545
```

```
jid       20261011T184928102545
function  test.ping
target    web1.lab (glob)
submitter cert:CN=ed
created   2026-10-11T18:49:28Z
expires   2026-10-11T19:49:28Z
nodes     web1.lab

web1.lab: ok (retcode 0, 2 ms)
True
```

A job that was sent to nodes that never answered shows its state as
`dispatched, 1 outstanding`; that is the first thing to look for when
"I ran it and some machines did not do it".

```sh
halite-hub jobs list
```
```
20261011T184928078450  test.ping   web1.lab   dispatched, 1 outstanding
```

and `jobs missing` names who:

```
$ halite-hub jobs missing 20261011T185328511519
web1.lab
web2.lab
```

Hub-side functions (runners) are jobs too, with the hub as the target:

```
20261011T184920437966  jobs.active   hub   complete
```

so the job cache is the audit trail of *operator actions on the hub* as
well as of changes to nodes.

### Stopping a job

```sh
halite-hub jobs kill <jid>
```

stops the job reaching any node it has not yet reached, removes it from
queues for nodes that were offline, and tells the nodes that have it. A
node **already applying a state finishes it**. A state run interrupted
halfway leaves a machine in neither the old condition nor the new one,
which is worse than completing something you changed your mind about; the
command says so, rather than implying more than it does.

## Retention

The cache is bounded by age and by total size, whichever binds first, and
the hub enforces both, hourly (or `jobs prune` now):

```yaml
# hub.yaml
job_cache_retention: 720h        # 30 days
job_cache_max_size: 10737418240  # 10 GiB
```

The job cache is the estate's audit trail of what ran and what answered, so
keep it longer than the time it takes you to notice a problem. If your
obligations are longer than the cache, ship returns elsewhere
([chapter 23](23-scheduler-and-returners.md)) rather than raising
retention without limit.

## Events from a job

Alongside the record, a job emits events as it moves, which is what a
reactor listens to ([chapter 18](18-events.md)):

```
halite/job/<jid>/new                  published
halite/job/<jid>/queued/<node>        held for an offline node
halite/job/<jid>/ret/<node>           a return arrived
```

and a state run adds `halite/state/<jid>/<node>/<result>` when it ends. A
job to the hub itself uses `halite/run/<jid>/new` and
`halite/run/<jid>/ret`.

## What a node keeps

The hub's record says what the node returned. The node's own record says
what it did: a hash-chained **evidence** log of every job accepted, who
asked, and the result, anchored at the hub ([chapter 28](28-hardening.md)).
The two are independent, which is the point: when they disagree,
something is wrong.

## Key ideas

- A **job** is one command to a target; it gets a time-sortable **JID**
  and is recorded, with its expected respondents, **before** delivery.
- Each node's answer is a **return** (`success`, `retcode`, `return`).
- `jobs list/show/lookup/missing/active/resume/kill/export/prune`.
- `dispatched, N outstanding` and `jobs missing` find nodes that did not
  answer.
- `jobs kill` cannot interrupt a state run in progress, by design.
- Retention is by age and size; the cache is your audit trail.

Next: [configuration management at scale](16-fleet-operations.md).
