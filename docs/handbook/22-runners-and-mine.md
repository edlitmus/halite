# Runners and the mine

<!-- abstract: Functions that run on the hub rather than on a node, every runner this build ships, and the mine: how nodes publish facts for other nodes to read. -->
<!-- kind: guide -->
<!-- index: runner; halite-hub runner; runner list; runner doc; jobs.list_jobs; manage.status; manage.up; manage.down; manage.versions; key.accept; key.revoke; key.list; nodegroups; fileserver.update; pillar.show_pillar; cache.grains; event.send; survey.hash; survey.diff; saltutil.refresh_pillar; saltutil.refresh_grains; mine; mine_functions; mine_interval; allow_tgt; mine.get; mine.valid; peer; load balancer backend list -->

Everything so far has been done *to nodes*. A **runner** is a function that
runs on the **hub itself**: asking the hub who is connected, which jobs are
still outstanding, what pillar it would compile for a node, accepting a key,
putting an event on the bus. It is the hub-side counterpart of the
execution functions.

```sh
halite-hub runner <module.function> [arguments]
```

A runner is a request to the hub even when typed on the hub: you
authenticate with an operator certificate, and being logged in on the hub is
not a credential. Each is authorized by the `runners:` list of your role,
which is a **separate permission** from `functions:`. Permission to ask the
hub a question and permission to run a command on every node are not the
same thing. A runner that then reaches the fleet (`saltutil.refresh_pillar`
does) is authorized a second time as the job it sends, so a runner grant
cannot quietly become fleet-wide execution.

Every runner call gets a JID, is recorded in the job cache with the
principal who asked, and emits `halite/run/<jid>/new` and
`halite/run/<jid>/ret` on the event bus. Reactors and orchestrations call
runners constantly (`runner.event.send`, `salt.runner`).

```sh
halite-hub runner list            # every runner, including those not built
halite-hub runner doc jobs.missing   # one signature
```

Both answer from the binary, so they work when the hub does not.

## Every runner

This is the whole inventory this build knows of, taken from `halite-hub
runner list` and grouped by purpose. **Built** ones work. **Not built** ones
are named so that you know to plan around them: calling one is refused with
the reason.

### Seeing the fleet

| Runner | Does |
|---|---|
| `manage.status` | The connected and disconnected halves of the accepted fleet. |
| `manage.up` / `manage.alived` / `manage.present` | The accepted nodes holding a stream to the hub. One fact, three names (kept so existing orchestration reads unchanged). |
| `manage.down` / `manage.not_alived` | The accepted nodes with no stream. |
| `manage.list_state`, `manage.list_not_state` | Nodes in, or not in, one presence state. |
| `manage.versions` | The build each node last reported, against the hub's own. Your upgrade progress report. |
| `nodegroups.list`, `.show`, `.expand` | Configured groups, one expression, and the nodes a group matches right now. |
| `cache.grains` | The grains the hub last received from a node (or every node). |
| `cache.mine` | One node's published mine data. |
| `cache.clear_grains`, `cache.clear_mine`, `cache.clear_all` | Forget cached grains or mine data. |

```
$ halite-hub runner manage.status
up:
    - web1.lab
down: []
```

Salt separates `present`, `alived` and `up` because its transport cannot
tell a live connection from a dead one without asking. Here a node holds a
stream to the hub or it does not.

### Jobs

| Runner | Does |
|---|---|
| `jobs.list_jobs` | Recent jobs, newest first. |
| `jobs.lookup_jid`, `jobs.list_job`, `jobs.print_job` | What each node returned for a job. |
| `jobs.active` | Jobs the hub is still expecting returns for. |
| `jobs.missing` | The nodes a job has not heard from. |
| `jobs.exit_success` | Whether **every** expected node returned success. A node that never answered is not a success, which is the distinction this exists to make. |
| `jobs.prune` | Drop jobs past retention, and report how many. |
| `survey.hash` | Group a job's nodes by the digest of what they returned. |
| `survey.diff` | Group them by return, with one representative from each group. |

`survey.*` answers "did every machine give the same answer?" for a command
such as `file.get_hash` or `pkg.version` across a fleet, and shows the
odd ones out.

### Keys and enrollment

| Runner | Does |
|---|---|
| `key.list` | Every key request, grouped by lifecycle state. |
| `key.accept`, `key.reject`, `key.revoke`, `key.delete` | The lifecycle ([chapter 25](25-trust-and-identity.md)). `delete` revokes an accepted node first, so deleting a record can never leave a live certificate behind. |
| `key.finger` | One node's key fingerprint. |
| `key.gen_signature`, `key.token` | **Not built** (the pinning is `--hub-fingerprint`; mint tokens with `halite-hub keys token create`). |
| `manage.safe_accept`, `manage.bootstrap` | **Not built.** |

### Tree, pillar and files

| Runner | Does |
|---|---|
| `pillar.show_pillar` | The pillar the hub would compile for one node. |
| `pillar.show_top` | The pillar files the top file selects for one node, in merge order. |
| `pillar.clear_cache`, `cache.pillar`, `cache.clear_pillar` | **Not built:** the hub compiles on every request, so there is no cache to show or clear. |
| `fileserver.update` | Fetch every remote backend (git, S3) now and report what each ref resolved to. |
| `fileserver.envs`, `.file_list`, `.dir_list` | What the hub serves. |
| `fileserver.symlink_list`, `.clear_cache`, `.clear_lock`, `.lock`, `.versions` | **Not built.** |
| `saltutil.refresh_grains`, `saltutil.refresh_pillar` | Tell matched nodes to re-collect grains or recompile pillar. |
| `saltutil.sync_all` | **Not built** as a hub-side push; run it on the nodes. |

### Events and reactions

| Runner | Does |
|---|---|
| `event.send` | Put an event on the bus. |
| `event.listen`, `event.replay` | Wait for events, or read events already on the bus from an offset. |
| `reactor.list` | The configured reactions, their files and principals. |
| `reactor.test` | Render what a tag would fire, with the authorization decision, without dispatching. |

### Orchestration

| Runner | Does |
|---|---|
| `state.orchestrate` / `state.orch` | Compile an orchestration on the hub and run it. |
| `state.orchestrate_show_sls` | The steps it would run, without running them. |
| `state.orch_list`, `state.orch_show`, `state.orch_resume` | Past runs, a timeline, and resume ([chapter 21](21-orchestration.md)). |
| `state.pause`, `state.resume`, `state.single`, `state.event` | **Not built.** |
| `error.error` | Raise an error: an orchestration that has decided to stop. |

### The mine

| Runner | Does |
|---|---|
| `mine.get` | What matched nodes published for one function. |
| `mine.update` | Tell matched nodes to recompute and republish. |
| `mine.valid` | What each node has published, when, and who may read it. |
| `mine.flush`, `mine.delete` | Drop everything one node has published, or one function's entry. |

### Not built

`queue.insert`, `queue.delete`, `queue.list_queues`, `queue.list_length`,
`queue.list_items` and `queue.process_queue` (a durable work queue; refused with a
reference to SPEC 19.4),
`net.arp`, `net.find`, `net.interfaces` (readers over mine data),
`http.query`, `slack.post`, `smtp.send` (the hub has no outbound
notification runner; see the returners in
[chapter 23](23-scheduler-and-returners.md) for how things leave).

A runner that ran and failed exits `1` and prints its error. Only a refusal,
an unknown name or a malformed call is a transport failure. An unknown name
tells you what the namespace *does* have:

```
halite: /v1/runners: fileserver.nonesuch is not a runner this hub serves; it has
fileserver.clear_cache, fileserver.clear_lock, fileserver.dir_list, fileserver.envs, …
```

## The mine

Sometimes one node needs to know a fact about others. A load balancer needs
the addresses of every web server. A monitoring node needs a list of what is
running where. The facts change as nodes come and go, so hard-coding them
in pillar is a chore that goes stale.

The **mine** is a small notice board held on the hub. Nodes **publish**
values to it on a timer; other nodes, and operators, **read** them. The
publisher decides what to publish and who may read it; the hub keeps it.
Nodes never talk to each other directly, because nodes only connect
outward to the hub.

### Publishing

In the publishing node's `node.yaml`:

```yaml
mine_interval: 30          # seconds between publications
mine_functions:
  ips:
    mine_function: network.ip_addrs
  roles:
    mine_function: grains.get
    key: role
    allow_tgt: 'lb*'
```

Each key under `mine_functions` is the name readers will ask for. The
value is either a plain function name (`network.ip_addrs: []`), or a
mapping with `mine_function:` naming an execution function, its arguments
as sibling keys, and optionally `allow_tgt`, a target expression of the
nodes allowed to read this entry.

Publishing is under the identity on the node's own certificate and no
other. A node cannot publish on another's behalf, and that is what makes
what is read worth believing.

A full publication **replaces**: a function removed from `mine_functions`
stops being served, rather than lingering.

### Reading, as an operator

```sh
halite-hub runner mine.get 'web*' ips
halite-hub runner mine.valid
```

```
web1.lab:
    - 10.67.183.52
    - 172.29.11.239
    - 192.168.1.57
web2.lab:
    - 10.67.183.52
    - 172.29.11.239
    - 192.168.1.57
```

```
web1.lab:
    ips:
        updated: 2026-10-11T19:08:28Z
        bytes: 47
    roles:
        updated: 2026-10-11T19:08:28Z
        allow_tgt: lb*
        bytes: 4
```

`mine.valid` shows when each entry was last refreshed and who may read it.
An entry nobody has refreshed is what a stale answer looks like before it
becomes a wrong one.

### Reading, as a node

In a state or a template, `salt['mine.get']('web*', 'ips')` returns the same
mapping, and a load balancer's configuration can then be built from it:

```jinja
{% for node, addrs in salt['mine.get']('web*', 'ips').items() %}
    server {{ node }} {{ addrs[0] }}:8080;
{% endfor %}
```

A node reading the mine acts under its own identity, and **the policy
decides what it may read**, deny by default. This replaces the predecessor's
`peer` configuration with the one policy file. Run as `db1.lab` with no
grant:

```
$ halite-hub run db1.lab mine.get 'web*' ips
db1.lab:
/v1/mine/get: node:db1.lab is bound to no role in policy.yaml
```

Give it exactly the right to read `ips` from `web*`:

```yaml
roles:
  read-backends:
    - target: 'web*'
      functions: ['ips']
bindings:
  - principal: 'node:db1.lab'
    roles: ['read-backends']
```

and it can (the answer is the listing above), but only that:

```
$ halite-hub run db1.lab mine.get 'web*' roles
db1.lab:
/v1/mine/get: no rule in [read-backends] permits roles against "web*"
```

The grant reads `ips` from `web*`, not `roles` from `web*`, and not `ips`
from `*`. `allow_tgt` is a second gate, set by the publisher: a node
publishing something sensitive decides who may see it without trusting every
reader's policy to be right. An operator is not restricted by `allow_tgt`,
having already passed the policy.

Use a node principal pattern for many readers: `principal: 'node:lb*'`.

> **Not built:** `publish.publish` and `publish.runner`, a node asking the
> hub to run something on other nodes, are not built. Reading the mine is
> the supported way for nodes to learn about each other.

## Patterns

- **A load balancer from the mine.** Web nodes publish `ips`; the balancer's
  template loops over `mine.get('web*', 'ips')`. A new web server appears in
  the balancer's configuration at its next highstate with no one editing a
  list. Pair it with `watch` on the balancer's service.
- **Inventory without an inventory.** Publish `grains.get` of a few keys
  and `mine.get('*', 'role')` is a live map of the fleet.
- **A "who has it?" question.** `halite-hub run '*' pkg.version nginx`
  followed by `halite-hub runner survey.diff <jid>` groups nodes by the
  version they have and lets you see the stragglers.
- **Upgrade progress.** `halite-hub runner manage.versions`.

## Key ideas

- A **runner** runs on the **hub**: `halite-hub runner module.function`,
  authorized by a role's `runners:` list, recorded as a job and an event.
- `halite-hub runner list` is the authoritative inventory, built and not
  built.
- The **mine** is a hub-held notice board: nodes publish with
  `mine_functions`, readers use `mine.get` under their own identity and the
  deny-by-default policy; `allow_tgt` is the publisher's own gate.
- The mine, `survey.*` and `manage.*` answer fleet-wide questions without
  logging into anything.

Next: [the scheduler and returners](23-scheduler-and-returners.md).
