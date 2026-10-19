# The hub and its nodes

<!-- abstract: How the control plane and the agents connect: the one port, the persistent connection, what lives where on disk, and what happens when either side goes away. -->
<!-- kind: concept -->
<!-- index: hub; node; connection; port 4510; mutual TLS; HTTP/2; subscribe stream; reconnect; hub_tries; hub_alive_interval; state_dir; cache_dir; pki_dir; doctor; presence; relay; quiesce; job expiry; replay protection; manage.status; manage.up -->

Up to now you have used one machine. This chapter is about what changes
when there are many: how a **node** connects to a **hub**, what each
side keeps, and what happens when either goes away. The mechanics of
installing are in the runbooks
([chapter 30](30-install-hub.md), [31](31-install-nodes.md)); this is
the picture that makes those procedures make sense.

## The direction of connection

**The node dials the hub. The hub never dials a node.**

```
      node ─────────────▶ hub:4510   TLS 1.3, both ends present certificates
      node ◀── jobs, events, control on that same connection ──── hub
```

This has consequences worth stating plainly:

- The hub opens **one TCP port**, by default `4510`. The first thing that
  happens on it is a TLS handshake handled by the Go standard library,
  in which a node presents its certificate. Two things are reachable
  without one: a health check that returns the hub's version, and
  enrollment, which is how a node gets a certificate in the first place.
  Enrollment does read what an unknown caller sends, a small request
  holding a certificate signing request, and the most it can do with it
  is leave a pending request for you to accept or reject. Everything
  else refuses a caller without a certificate.
- A node listens on **nothing**. (Optionally, a node can serve Prometheus
  metrics on a port you choose; that is the one exception and is off
  until you set an address.)
- A node behind NAT, or a firewall that blocks everything inbound, needs
  **no special arrangement**. If it can reach the hub's port 4510 over
  TCP, it works.
- A hub cannot reach a node that has not connected. There is no "wake
  up". A node that is off is unreachable, exactly as in any such system;
  [chapter 14](14-remote-execution.md) shows what you can do about jobs
  for such a node.
- There is no plaintext mode, not even on loopback, not even for testing.
  That setting is always the one found in production.

The protocol is HTTP/2 over TLS 1.3, with an ALPN identifier of
`halite/1`. A node opens one connection and multiplexes everything over
it, so a large file transfer cannot stall job delivery. A middlebox that
only speaks HTTP/1.1 is tolerated, at the cost of a second connection
per node.

## What a node does when it starts

`halite-node connect` (the service) does this, in order:

1. Reads its configuration and its **certificate**. If it has none, it
   refuses to run and tells you to enrol ([chapter 25](25-trust-and-identity.md)).
2. Checks the hub's CA against the fingerprint it pinned at enrollment.
3. Connects, **sends its grains** to the hub, and opens a long-lived
   stream on which the hub writes jobs.
4. Fires `halite/node/<id>/start`, which the hub puts on the event bus.
5. Starts its **beacons** and **scheduler** ([chapter 19](19-beacons.md),
   [chapter 23](23-scheduler-and-returners.md)).
6. Waits. Each job the hub writes to the stream is checked, then run.

It keeps the connection alive with a ping every `hub_alive_interval`
(30 seconds by default); a missed budget makes the node reconnect from
its side. If the hub is unreachable, the node **retries with backoff
forever** (`hub_tries: 0`, the default). Set `hub_tries` to a number if you
want an agent that gives up and exits. The only two reasons a node exits
by itself are a revoked enrollment and `hub_tries` being reached, both of
which a restart could not fix, and the service files are written so that
exit status `1` stops the unit instead of restarting it into a loop.

When a node is configured with a list of hubs, `hub_type: failover`
rotates through them; the default `static` uses the first that answers.

## What a node does with a job

A job arrives as one line on the stream: a JID, a function, arguments, an
environment, an expiry and a random nonce. Before running it the node
checks, and refuses with a structured reply if any check fails:

| Check | If it fails |
|---|---|
| Has this JID been run here before? | The job is refused, as a duplicate. |
| Has this nonce been seen? | Refused, as a replay. |
| Is the expiry in the past? | Refused, as expired. |
| (If `require_job_signature`) is there a valid signature from a trusted signer? | Refused, as unsigned. |
| Is the queue full? | Refused, out loud, rather than held in memory. |

So a job captured by someone on the network, or replayed by a
compromised hub operator, does nothing a second time. The default
lifetime of a job is 15 minutes (`--ttl`), and a job queued for a node
that was off lasts one hour.

A node runs jobs **one at a time** by default, so two package-manager
runs cannot collide. The reader of the stream and the executor are
separate, so a long job does not stop pings or new jobs arriving into the
bounded queue (`job_queue_depth`, default 16).

If a job needs pillar, the node asks the hub, which compiles it for that
node (identity taken from the certificate, not from the request) and
returns only that. If it needs files, the node fetches them from the hub's
file server and caches them. The hub is not asked to compile states: the
**node compiles and runs its own**, which is why one hub can serve a very
large fleet.

## What the hub keeps

Everything durable on the hub is in a small number of places. The paths
below are Linux's; FreeBSD's are in the table in
[chapter 33](33-configuration.md).

| Directory | Setting | Holds | Lose it and… |
|---|---|---|---|
| `/etc/halite/pki` | `pki_dir` | The enrollment CA (certificate **and private key**), the hub's own certificate, operator certificates | …every node must enrol again. Back it up; protect `ca.key` like a root password. |
| `/var/lib/halite` | `state_dir` | `keys/` (every node's record), `events/` (the bus), `jobs/` (the job cache), `orch/` (orchestration runs), `mine/`, `evidence-anchors/` | …you lose the fleet's identity records and history. It is **durable state, not cache**. |
| `/var/cache/halite` | `cache_dir` | Compiled-pillar cache, git mirrors, `nodes/` | …nothing that cannot be rebuilt. |
| `/etc/halite/hub.yaml`, `policy.yaml` | | Configuration and authorization | …the hub starts with defaults, and nobody may do anything. |

The lab hub used in preparing this handbook shows the state directory:

```
hub/state/events           the event bus: segmented, durable
hub/state/evidence-anchors the chain heads nodes report
hub/state/jobs             the job cache
hub/state/keys             one record per node
hub/state/mine             the mine
hub/state/orch             orchestration runs
```

`serve` refuses to start if a file root (a directory served to nodes)
contains `state_dir`, `cache_dir` or `pki_dir`, because that would serve
the job cache, and so every return in the estate, to every node.

The hub runs as an unprivileged account (conventionally `halite`) and
needs no root. Starting and stopping is a service restart: **there is no
reload**. Nothing handles `SIGHUP` and the service files deliberately have
no `ExecReload`; changing configuration is a restart, and nodes reconnect
on their own.

## What a node keeps

| Directory | Setting | Holds |
|---|---|---|
| `/etc/halite/pki` | `pki_dir` | The node's **private key** (generated here, never sent anywhere), its certificate, the hub CA it pinned |
| `/var/lib/halite` | `state_dir` | The **evidence chain** (below), the returner's local file |
| `/var/cache/halite` | `cache_dir` | Files fetched from the hub, kept so a second run asks only "has it changed?" |
| `/etc/halite/node.yaml`, `grains`, `grains.d/` | | Configuration and custom grains |

The **evidence chain** is the node's own record of every job it accepted,
who asked, and the result, in an append-only log in which each record
carries the hash of the one before. The node reports the latest hash to
the hub after every job, and the hub signs a receipt. A compromise of the
node cannot quietly rewrite what it reported, and a compromise of the hub
cannot deny having been told. `halite-node verify-evidence` checks it
([chapter 28](28-hardening.md)).

## Seeing the connection

From the hub:

```sh
halite-hub runner manage.status        # which nodes are up and down
halite-hub runner manage.up
halite-hub keys list                   # every node the hub has heard of
halite-hub event listen --tag 'halite/node/*'
```

```
up:
    - web1.lab
down: []
```

When a node stops, the bus shows it, with the reason, and the
presence set changes:

```json
{"_tag":"halite/node/web1.lab/stop","_node":"web1.lab",…}
{"_tag":"halite/presence/change","_node":"web1.lab","data":{"connected":1,"left":"web1.lab"},…}
…
{"_tag":"halite/node/web1.lab/start","data":{"version":"0.1.0-7-g…"},…}
{"_tag":"halite/presence/change","data":{"connected":1,"joined":"web1.lab"},…}
```

Those are the same events a reactor can act on
([chapter 20](20-reactors.md)): "when a node starts, apply its highstate"
is a three-line reactor.

## Diagnosing it: `doctor`

Each program has a `doctor` command that runs a set of checks that apply
to it, changes nothing, starts no listener, and says what to do about each
failure. It is the first thing to run when something is wrong, on either
side:

```sh
halite-node doctor
```

```
halite-node doctor — web1.lab

  pass  configuration validity           …/node1.yaml loads, and matches what this process is running
  pass  certificate validity and expiry  the hub's CA: 3649 days left; this node's certificate: 89 days left
  pass  connectivity                     127.0.0.1 answered in 2ms: halite-hub 0.1.0-7-g… ok
  pass  clock skew against the hub       within 1m of the hub (299ms ahead of)
  pass  file server reachability         the hub's file server: 1 root(s)
  pass  pillar compilation               compiles, 0 top-level key(s)
  pass  disk space                       … 135.4 GiB free
  skip  extension signatures             no extensions are installed
  pass  module verification              every module that changes this machine as root has been run against its tool; 24 unprivileged ones have not
  pass  evidence chain                   31 records in 1 segment(s) …
  pass  FIPS mode consistency            neither the kernel nor this build is in FIPS mode

  10 pass, 1 skip
```

and `halite-hub doctor` and `halite-api doctor` check theirs: configuration,
the three kinds of certificate and their expiry, file-server roots, a trial
pillar compile, disk space, queue depths, FIPS consistency. A check that
does not apply is *skipped*, with the reason, rather than quietly omitted.
`doctor` exits non-zero only on a failure, not on a warning, so it is safe
in a monitoring probe. Use it as the first step of every runbook in
Part VI.

## When the hub is unavailable

| Capability | Behaviour while the hub is down |
|---|---|
| **Running jobs** | Nothing can be dispatched; operators' commands fail. |
| **State tree** | A node falls back to its own configured copy of the roots, with a warning, if it has one. |
| **Pillar** | Does **not** fall back (a state applied against missing secrets would report a convergence that never happened) unless you enabled `pillar_cache_disk`. |
| **Local mode** | `halite-node state apply --local` works regardless. This is the break-glass path. |
| **The node itself** | Stays up and retries with backoff; it does not exit. |

This is why a fleet that must stay configurable through a hub outage keeps
a copy of the tree on each node (a git checkout works) or runs the hub
with a hot standby. [Chapter 35](35-backup-and-recovery.md) covers
recovery, and [chapter 38](38-scaling.md) covers relays and sizing.

## Relays

For a network segment that cannot reach the main hub directly, or one
whose returns must survive the link between them going down, a **relay** is
a hub that serves its own nodes and presents itself upstream as a single
client:

```
 nodes ──▶ relay ──▶ hub
```

The relay enrols with the upstream like a node, then runs as a hub. While
the upstream is unreachable it keeps serving its segment and spools returns
to disk, draining them oldest-first when the link returns. The upstream
holds no keys for the relay's nodes, but targeting and `manage.up` see
them, and a job submitted upstream reaches them through the relay. It is
`halite-hub serve --relay --upstream <hub>`; the full procedure is in
[chapter 38](38-scaling.md).

## Key ideas

- **The node dials the hub** on one port (4510) over mutual TLS 1.3. The
  hub never dials; the node listens on nothing.
- A node holds **one multiplexed connection**, checks every job (unique,
  fresh, unexpired, signed if required) and runs them one at a time.
- The **node compiles its own states**; the hub serves files and
  compiles **pillar**, per node.
- The hub's durable state is `pki_dir` (CA!) and `state_dir`; the rest is
  cache. A node's is its key, its evidence chain, and a cache.
- A hub outage stops dispatching but not the nodes; the tree falls back,
  **pillar does not** (by default). `--local` always works.
- `doctor` on each program is the first diagnostic.

Next: [choosing which nodes](13-targeting.md).
