# The event bus

<!-- abstract: The stream of everything that happens: tags, payloads, listening, replaying, sending, and the rules that stop a node from forging the control plane's events. -->
<!-- kind: concept -->
<!-- index: event; event bus; event tag; halite-hub event listen; event tags; event.send; event.replay; offset; replay; halite/job; halite/node; halite/state; halite/presence; halite/key; halite/error; halite/run; event_retention; event_max_size; event_tag_compat; correlation; durable; subscriber -->

Everything the hub does leaves a trace on one durable log: the **event
bus**. A node connects, a job starts, a return arrives, a key is accepted,
a beacon fires. Each is an **event**: a **tag**, saying what kind of thing
happened and to whom, and a body of **data**.

The bus is the connective tissue of the automation half of halite.
**Beacons** put events on it, **reactors** read from it, **orchestration**
can wait for events on it, and you can follow or replay it from a
terminal. Understanding it makes the next four chapters easy.

## An event

Here are real events from the lab hub as a node stopped and started again:

```json
{"_stamp":"2026-10-11T18:49:25.044524Z","_tag":"halite/node/web1.lab/stop","_node":"web1.lab","_schema":"halite.event/1","_offset":"00000001:8764"}
{"_stamp":"2026-10-11T18:49:25.044577Z","_tag":"halite/presence/change","_node":"web1.lab","_schema":"halite.event/1","data":{"connected":1,"left":"web1.lab"},"_offset":"00000001:8924"}
{"_stamp":"2026-10-11T18:49:33.626497Z","_tag":"halite/node/web1.lab/start","_node":"web1.lab","_schema":"halite.event/1","data":{"version":"0.1.0-7-g8ad761ce275b+8ad761ce275b"},"_offset":"00000001:10475"}
```

Every event carries:

| Field | Meaning |
|---|---|
| `_tag` | The path-like name. The whole identity of what happened. |
| `data` | A body; its shape depends on the tag. |
| `_stamp` | When, to the microsecond (RFC 3339, UTC). |
| `_node` | The node it concerns, when there is one. |
| `_offset` | Its position in the log, such as `00000001:8764`. Store it to resume exactly here. |
| `_correlation` | An ID meant to be carried through causally related events, so a chain can be followed. Optional. A node can set it (`event send --correlation`); the hub stamps it on events it creates, but a runner's `event.send` starts a new one, so it does not yet link a reaction's output back to its cause. |
| `_schema` | `halite.event/1`. |

## Tags

A tag is a `/`-separated path starting with `halite`. Ask the hub for the
namespace it uses:

```sh
halite-hub event tags
```

```
halite/job/<jid>/new                   a job is published
halite/job/<jid>/ret/<node>            a return arrives
halite/state/<jid>/<node>/<result>     a state run completes
halite/node/<node>/start               a node connects
halite/node/<node>/stop                a node disconnects
halite/node/<node>/enroll/<state>      an enrollment changes state
halite/node/<node>/...                 anything a node sends with `halite-node event send`
halite/key/<node>/<action>             a key lifecycle action
halite/presence/change                 the connected set changes
halite/error/...                       a structured error worth reacting to
```

Others you will see in practice:

| Tag | When | Data |
|---|---|---|
| `halite/job/<jid>/queued/<node>` | A held job is delivered to a node that came back | `fun`, `jid` |
| `halite/run/<jid>/new`, `halite/run/<jid>/ret` | A runner (a hub-side function) or an orchestration starts and finishes | `fun`, `principal`, `success`, `error` |
| `halite/state/<jid>/<node>/ok` | A state run finished; the last segment is the result | `jid`, `retcode` (`0` changed, `2` converged) |
| `halite/beacon/<node>/<beacon>/…` | A beacon fired ([chapter 19](19-beacons.md)) | the beacon's data |
| `halite/node/<node>/evidence/conflict` | A node's reported evidence head contradicts the hub's record ([chapter 28](28-hardening.md)) | |
| `halite/reactor/error`, `halite/reactor/overflow` | A reaction failed, or the reactor's queue dropped events | |

`halite-hub event tags` prints the namespace the build you are running
actually uses. If you need an old-style spelling for a consumer that cannot
change, `event_tag_compat: true` additionally emits every event under its
`salt/…` name.

## Following the bus

```sh
halite-hub event listen                            # follow everything, live
halite-hub event listen --tag 'halite/job/*/ret/*' # only returns
halite-hub event listen --tag 'halite/node/**,halite/presence/*'   # a list
halite-hub event listen --from earliest --once     # replay the whole log and stop
halite-hub event listen --from '00000001:52876'    # resume from an offset
```

Output is one JSON object per line, ready for `jq`, a file, or a pipe into
another program.

**Globs.** `*` matches exactly **one** path segment, and `**` matches any
number. That distinction is easy to miss. The tag `halite/node/web1.lab/start`:

| Pattern | Matches? |
|---|---|
| `halite/node/*/start` | yes |
| `halite/node/**` | yes |
| `halite/node/*` | **no** (one segment after `node`, but there are two) |
| `halite/*` | **no** |
| `halite/job/*/ret/*` | matches returns, and nothing else |

So the safe default when you mean "everything under this" is `**`.

**Positions.** `--from` takes `latest` (the default), `earliest`, or an
offset. A consumer that stores the offset of the last event it handled can
stop for a month and resume without missing or repeating a thing. That is
what makes a reactor restart lossless (the reactor records the position before its
oldest unfinished event, so a restart repeats the few that finished after it
rather than losing one that had not), and what makes the log usable in an
incident, when the question is "what happened between 02:10 and 02:40?".
A reader that falls behind further than the bus can hold is told so
explicitly, and not given a silently incomplete stream.

## Sending events

**From a node**, the command line:

```sh
halite-node event send deploy/finished version=1.2
halite-node event send deploy/started '{"version":"1.3","host":"web1"}' --correlation abc123
```

What lands on the hub is always **under the node's own prefix**,
whatever tag it asks for:

```
halite/node/web1.lab/deploy/finished
halite/node/web1.lab/deploy/started    (with "_correlation":"abc123")
```

and, to prove the rule, a node that tries to impersonate the control plane:

```
$ halite-node event send halite/job/evil/new '{"x":1}'
halite/node/web1.lab/job/evil/new
```

It has been put in its own namespace, harmless. **A node can only speak
under `halite/node/<its own ID>/`.** This is not a convenience, it is a
security rule: if a node could emit `halite/job/…` or `halite/key/…`, and
a reactor listened for that tag, any node could trigger any reaction. Here
a reactor that listens on `halite/node/*/deploy/finished` knows the middle
segment is the sender's certified identity, and not data the sender
chose.

**From the hub**, the `event.send` runner:

```sh
halite-hub runner event.send tag=halite/custom/hello data='{"k":"v"}'
```

```
tag: halite/custom/hello
offset: 00000001:53603
```

Use this from orchestration and reactors to leave an audit trail ("deploy
begun", "deploy complete") that other automation can wait on. The hub-side
runners `event.listen` and `event.replay` read the bus, for use in
orchestration.

**From outside**, a webhook posted to the API appears on the bus under
`halite/hook/<path>` with the authenticated sender recorded, so CI systems
and monitoring tools can start automation ([chapter 27](27-http-api.md)).

## Durability and retention

The bus is an **append-only log of segment files** under the hub's
`state_dir`, not an in-memory queue. Two properties follow.

**Retention** is by age and by total size, whichever binds first:

```yaml
event_retention: 720h        # 30 days
event_max_size: 4294967296   # 4 GiB
```

Subscribers resume from an offset, so retention is also how long a
consumer may be down without losing events. Size it for your slowest
consumer.

**Durability** is per class: security-relevant tags (`halite/auth*`,
`halite/key/*`) are on disk before the append returns; the rest are synced
on an interval, trading a few seconds of exposure to a crash for
throughput. That is the right trade for a bus that sees every return.

## Who can read it

`event listen` goes through the same authorization as everything else: it
needs an operator certificate, and a role that permits it (the starting
policy created by `keys operator create --admin` does). Through the API,
events are filtered per caller, so someone allowed to see `web*` nodes
cannot subscribe to events about databases ([chapter 27](27-http-api.md)).

## Patterns

- **Debugging.** `halite-hub event listen` in one terminal while you run a
  command in another is the fastest way to see what the system does.
- **Audit.** Pipe `--tag 'halite/key/**,halite/run/**'` to a log
  aggregator. Together with the job cache, it is who did what and when.
- **A change feed.** A script following `halite/state/*/*/ok` knows each
  time a state run finishes, and with `retcode` 0 that it changed something.
- **Hand-offs.** Orchestration steps and external systems coordinate by
  sending and waiting for custom events
  ([chapter 21](21-orchestration.md)).

## Key ideas

- The **bus** is a durable, ordered log of **events**: a **tag** and
  **data**, with a time, node and **offset**.
- Tags are `halite/…`; **`*` is one segment, `**` is many.**
- `halite-hub event listen [--tag …] [--from latest|earliest|offset]
  [--once]`; store the offset to resume losslessly.
- Nodes may only emit under **their own `halite/node/<id>/`** prefix; that
  rule is what makes reactions safe to trust.
- Retention is by age and size; security tags are synced to disk at once.

Next: [beacons](19-beacons.md), the part that puts events on the bus from
the machines themselves.
