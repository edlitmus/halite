# Event reference

<!-- abstract: Every event tag the hub and nodes emit, when, and the shape of its data, with the rules for sending your own. -->
<!-- kind: reference -->
<!-- index: event reference; event tags; halite/job; halite/node; halite/state; halite/run; halite/presence; halite/beacon; halite/key; halite/error; halite/reactor; halite/hook; halite/audit; halite.event/1; evidence conflict; event schema -->

The event bus is described in [chapter 18](18-events.md). This is the
reference: every tag family this build emits, what causes it, and what its
`data` holds. Everything here was read from the event log of a running hub
unless marked otherwise. `halite-hub event tags` prints the namespace the build
you are running uses.

An event is a JSON object (`halite.event/1`) with `_tag`, `_stamp`, `_offset`,
`_schema`, usually `_node`, sometimes `_correlation`, and `data`.

## The families

### Jobs

| Tag | Emitted | `data` |
|---|---|---|
| `halite/job/<jid>/new` | A job is published | `fun`, `arg`, `tgt`, `tgt_type`, `nodes` (the resolved set), `submitter`, `test` |
| `halite/job/<jid>/ret/<node>` | A node's return arrives | `fun`, `jid`, `success`, `retcode`, `duration_ms` |
| `halite/job/<jid>/queued/<node>` | A job held for an offline node is delivered to it | `fun`, `jid` |

```json
{"_tag":"halite/job/20261011T182508812680/new",
 "data":{"arg":null,"fun":"test.ping","jid":"20261011T182508812680",
         "nodes":["web1.lab"],"submitter":"cert:CN=ed","test":false,
         "tgt":"web1.lab","tgt_type":"glob"}}
{"_tag":"halite/job/20261011T182508812680/ret/web1.lab","_node":"web1.lab",
 "data":{"duration_ms":2,"fun":"test.ping","jid":"20261011T182508812680","retcode":0,"success":true}}
```

> `halite/job/<jid>/new` names no node in its tag, and its data carries the job's
> target and **arguments**. See the known gap in
> [chapter 27](27-http-api.md): a caller with any event grant sees these for
> every job, so do not pass secrets as job arguments.

### State runs

| Tag | Emitted | `data` |
|---|---|---|
| `halite/state/<jid>/<node>/ok` | A node finished a state run successfully | `jid`, `retcode` (`0` something changed, `2` converged) |
| `halite/state/<jid>/<node>/failed` | A node's state run failed | `jid`, `retcode` |

This is the tag to react to for "the run finished"; the `job/…/ret/…` event
fires as well.

### Hub-side functions and orchestration

| Tag | Emitted | `data` |
|---|---|---|
| `halite/run/<jid>/new` | A runner (a hub-side function) starts | `fun`, `arg`, `jid`, `principal` |
| `halite/run/<jid>/ret` | It finishes | `fun`, `jid`, `success`, `error` |
| `halite/run/<jid>/orch/new` | An orchestration starts | |
| `halite/run/<jid>/orch/step/<step>` | A step completes | |
| `halite/run/<jid>/orch/ret` | The orchestration finishes | |

### Nodes and presence

| Tag | Emitted | `data` |
|---|---|---|
| `halite/node/<node>/start` | A node connects | `version` |
| `halite/node/<node>/stop` | A node disconnects | none in this build (the design has a reason) |
| `halite/presence/change` | The connected set changes | `connected` (the count), and `joined` or `left` (the node) |
| `halite/node/<node>/enroll/pending` | A request arrives | `fingerprint`, `from` |
| `halite/node/<node>/enroll/accepted` | An operator accepts it, or a token admits it | `fingerprint`, `from` |
| `halite/node/<node>/enroll/…` | Other enrolment states | |
| `halite/node/<node>/evidence/conflict` | A reported evidence head contradicts the hub's record | |
| `halite/node/<node>/…` | Anything the node sent with `halite-node event send` | what it sent |

```json
{"_tag":"halite/node/web1.lab/start","_node":"web1.lab","data":{"version":"0.1.0-7-g8ad761ce275b"}}
{"_tag":"halite/presence/change","_node":"web1.lab","data":{"connected":1,"joined":"web1.lab"}}
```

`halite/node/<node>/evidence/conflict` lives in the node's own namespace, which
the node can also write to (`halite-node event send evidence/conflict` lands on
the same tag), so the event is a **prompt to look**, never the finding itself.
The finding is the conflict line in the hub's anchor file, which only the hub
writes (`halite-hub evidence anchors <node>`).

A node can only emit under its own `halite/node/<its ID>/` prefix, whatever tag
it asks for ([chapter 18](18-events.md)).

### Keys

| Tag | Emitted | `data` |
|---|---|---|
| `halite/key/<node>/<action>` | A key lifecycle action taken through the hub's runners | |

(Actions taken with the `halite-hub keys` command edit the key store directly;
the enrolment events above are the record of requests and acceptances.)

### Beacons

| Tag | Emitted | `data` |
|---|---|---|
| `halite/beacon/<node>/<beacon>/<what>` | A beacon fires on a node | The beacon's data ([chapter 19](19-beacons.md)); `_count` when events were coalesced |
| `halite/beacon/<node>/<beacon>/overflow` | A beacon's bounded queue dropped events | the drop count |

### Reactors

| Tag | Emitted | `data` |
|---|---|---|
| `halite/reactor/error` | A reaction failed to render or dispatch | `error`, `file`, `tag`, and the position |
| `halite/reactor/overflow` | The reactor's queue dropped events | `dropped`, `tag`, `reactor` |
| `halite/reactor/throttled` | An entry's `rate_limit` refused an event | `tag`, `reactor`, `rate_limit` |
| `halite/reactor/loop` | A causality chain was broken (see the known gap in [chapter 20](20-reactors.md)) | `tag`, `reactor`, `max_causality_depth` |

### Webhooks and your own events

| Tag | Emitted | `data` |
|---|---|---|
| `halite/hook/<path>` | A webhook delivery was accepted by the API | `_principal`, `_remote`, `body` |
| any tag you choose, from the hub (`event.send`) | A runner or reaction puts it there | what you passed, plus `_principal` |
| `halite/audit/…` | A convention, not a reserved family: use it for events that record that something was done | |

### Errors

| Tag | Emitted | `data` |
|---|---|---|
| `halite/error/enrollment` | An enrolment problem the hub chose to raise | |
| `halite/error/…` | A structured error worth reacting to | |

## Rules for events you send

- **From a node**, the tag is rewritten under `halite/node/<its ID>/`. A node
  can therefore never forge `halite/job/…`, `halite/key/…`, `halite/run/…` or
  `halite/auth…`; any reaction that matches `halite/node/*/x/…` knows the
  middle segment is the sender's certified identity.
- **From the hub**, `halite-hub runner event.send tag=… data=…` accepts any tag
  rooted at `halite/`, and records the principal that sent it in
  `data._principal` (taken from the connection, never from the payload).
- Choose a prefix for **your own** events that no reactor pattern matches, so a
  reaction cannot trigger itself.
- Keep `data` small and flat. It is carried in the bus and every returner.

## Compatibility spellings

`event_tag_compat: true` also emits each event under its older `salt/…`
spelling, for a consumer that cannot change at the same time as the estate. It
doubles the event volume; turn it off when the consumer is migrated.

## Delivery guarantees

| Property | Behaviour |
|---|---|
| Order | Total, by offset, within one hub |
| Durability | Security tags (`halite/auth*`, `halite/key/*`) are on disk before the append returns; the rest are synced on an interval |
| Retention | `event_retention` / `event_max_size`, whichever binds first |
| Replay | From any retained offset (`--from`), which is what makes a reactor restart lossless |
| A slow subscriber | Told explicitly that it fell behind; never given a silently incomplete stream |
| Across a relay | Only the tags the relay is configured to forward (`relay_event_tags`) |

## Key ideas

- Tags are `halite/<family>/…`; the families are **job, state, run, node,
  presence, key, beacon, reactor, hook, error**, plus whatever you add.
- Read `halite-hub event tags` for the namespace of your build, and a few real
  events before writing a reaction that reads one.
- Nodes speak only under their own prefix; the hub records who sent what it
  sends.
