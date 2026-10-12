# Runbook: sizing, scaling and relays

<!-- abstract: How big a hub needs to be, what has and has not been measured, the knobs that matter, and how to add a relay for a distant or isolated segment. -->
<!-- kind: runbook -->
<!-- index: scaling; sizing; capacity; performance; performance targets; relay; halite-hub serve --relay; accept_relays; relay.proxy; relay_upstream; relay_pki_dir; relay_spool_dir; relay_event_tags; relay_max_depth; reactor_workers; job_queue_depth; splay; simfleet; make perf; thundering herd; hub high availability -->

**Use this runbook to** size a hub, spread a fleet's load, and add a **relay**
for a segment that cannot reach the main hub or whose returns must survive a
link going down.

**You will end with** a hub sized with evidence, an idea of where it will
saturate, and, if you need one, a working relay.

## Before you start

Be honest about what is known. The design sets **targets**, and this build has
measured only some of them:

| SPEC 30 target | Status |
|---|---|
| Highstate compile, 500 states over 50 SLS files, heavy Jinja: under 2 s | **Measured.** `make perf` on a development workstation: **26 ms** |
| Pillar compile, 200 pillar SLS: under 500 ms cold | **Measured: 45 ms** |
| Pillar compile, cached: under 5 ms | Not measured: the hub has no pillar cache; it compiles on each request |
| 20,000 connected nodes per hub (25,000 hard limit); 100,000 with relays | **Not measured.** A simulated-node harness exists (`tools/simfleet`, `make simfleet`) and has been run once, at 200 nodes |
| `test.ping` to 10,000 nodes: 95% of returns in 10 s | Not measured |
| Dispatch of `state.apply` to 10,000 nodes in 30 s | Not measured |
| Hub memory at 20,000 nodes: under 4 GiB | Not measured |
| Reactor throughput: 5,000 events/s sustained | Not measured |
| Node idle memory (under 40 MiB with 10 beacons) and CPU (under 0.5% of a core) | Not measured: it needs a soak |
| File server saturating 10 Gb | Not measured |
| Cold start to first highstate in 15 s including enrolment | Not measured |

`make perf` prints this table and which rows wait on what, and fails if either
measured row is over target. **Do not plan a 20,000-node estate on the strength
of a target.** Plan on the sizing method below, and measure your own.

For orientation only: the design compares itself to a predecessor that is
generally held to about 5,000 nodes per central server with substantial tuning, whose
reactor is a known serialization point, and whose large-tree compile is
measured in tens of seconds. The targets are set to be a clear improvement.

## Procedure

### 1. Understand where load comes from

| Load | Driven by | Scales with |
|---|---|---|
| **Connections** | One long-lived HTTP/2 connection per node (default max 25,000 per hub) | node count |
| **Returns** | A node files each return as it completes; each opens a new request | jobs × nodes |
| **Pillar compilation** | Each node asks at every highstate; compiled on the hub, per node, **on every request** | highstates/hour × nodes × pillar size |
| **File server** | Each node fetches a manifest then only changed files | nodes × tree churn |
| **Events** | Job and return events, beacon events, node events | activity |
| **Job cache and event log** | Written for every job, return and event | activity × retention |
| **Reactor** | Events matching reactor entries | event rate × entries |

The node does the heavy work (it **compiles its own states**), which is why a
hub's cost is mostly pillar compilation, file serving, and bookkeeping for
returns and events.

### 2. Measure your own tree

Before sizing, measure the two costs that are yours:

```sh
# pillar: how long does the hub take to compile one node's?
time halite-hub runner pillar.show_pillar web1.example.com > /dev/null

# states: how long does a node take to compile and rehearse?
time halite-node state show_lowstate --local --file-root … --pillar-root … > /dev/null
```

Multiply the first by (nodes ÷ the interval between highstates) to get the
hub's pillar load per second, and compare with its cores. A 200-SLS pillar
compiled in 45 ms on one core means one core serves ~20 compilations a second;
10,000 nodes each converging every 30 minutes is about 5.5 a second.

### 3. Flatten the spikes (these are all free)

The dominant failure at scale is **everyone doing the same thing at once**. In
the order of effect:

- **`splay` on every scheduled job** that touches the hub
  ([chapter 23](23-scheduler-and-returners.md)). A thousand nodes with
  `cron: '17 3 * * *'` and no splay is a thousand pillar compilations in the
  same second. `splay: 900` spreads them over 15 minutes.
- **`RandomizedDelaySec`** in the systemd timer, or `sleep` with a random
  delay in cron (`jot` on FreeBSD; `$RANDOM` does not exist in its `sh`).
- **Batches** for operator-driven runs: `--batch 10%`.
- **A reactor on `halite/node/*/start`** applies to every node when the hub
  restarts and the whole fleet reconnects. Rate-limit it
  (`rate_limit: 100/m`) or use `startup_states` on the node instead.
- **Stagger enrolment** so certificates do not all fall due on the same day.
- Keep grain refresh at its default (`grains_refresh_interval: 30m`) unless
  facts change faster.

### 4. Size the hub

A starting point, to be replaced by your measurements:

| Fleet | Hub | Notes |
|---|---|---|
| up to ~500 nodes | 2 cores, 2 GiB, any disk | Comfortable; the lab-style hub with a few nodes is far under this |
| ~500 to ~5,000 | 4 to 8 cores, 4 to 8 GiB, SSD for `state_dir` | Watch `halite_hub_requests_total` and pillar duration |
| beyond | measure; add relays | The design target is 20,000 on 16 cores and 4 GiB, **not yet demonstrated** |

Disk: the **job cache** and **event log** are bounded by retention and size
(defaults 720 h and 10 GiB / 4 GiB); size `state_dir` above their sum, put it
on storage that does not share a spindle with the tree, and keep the **tree**
out of it. Memory: dominated by connections and caches. File descriptors: one
per node plus headroom (`LimitNOFILE=65536` in the unit; raise it before 25,000).

Watch, in the order that matters ([chapter 34](34-monitoring.md)):
`halite_hub_request_duration_seconds` and `halite_pillar_compile_duration_seconds`
(CPU saturation shows first), `halite_event_subscriber_lag_seconds` and
`halite_reactor_dropped_total` (the bus falling behind),
`halite_jobs_missing_returns` (returns not arriving), and the connected gap.

### 5. Know the knobs

| Setting | Default | Why you would move it |
|---|---|---|
| `reactor_workers` | 2 × CPUs | A reaction storm queues; raise with care, since each worker dispatches jobs |
| `reactor_queue_depth` | 10000 | Larger absorbs bursts; overflow drops the oldest and tells you |
| `job_queue_depth` (node) | 16 | A node's bounded queue of waiting jobs; a full queue is refused out loud |
| `job_cache_retention`, `job_cache_max_size` | 720 h, 10 GiB | Smaller for a busy fleet, larger for audit |
| `event_retention`, `event_max_size` | 720 h, 4 GiB | Same; also how long a consumer may be down |
| `evidence_anchor_rate`, `evidence_anchor_burst` | | A node running many short jobs reaches the limit in passing |
| `hub_alive_interval` (node) | 30 s | Longer cuts keepalive traffic; shorter detects loss faster |
| `max_body` | 64 MiB | Request size limit |
| `relay_max_depth` | 2 | Relay nesting |

Every limit is enforced before allocation, so a malformed or oversized request
cannot exhaust memory.

### 6. Add a relay

A **relay** is a hub that serves its own nodes and presents itself to a parent
hub as a single client. Use one for a segment that cannot reach the main hub
directly, to put the connection close to distant nodes, or for returns that
must survive the link between them going down:

```
   nodes ──▶ relay ──▶ hub
```

The relay enrols with its upstream *as an ordinary node*, then runs as a hub.
In order:

**1. On the upstream hub**, accept relays and grant this one the right to proxy:

```yaml
# hub.yaml
accept_relays: true
```
```yaml
# policy.yaml
roles:
  relay:
    - runners: ['relay.proxy']
bindings:
  - principal: 'node:relay1.example'
    roles: ['relay']
```

Restart the hub (no reload), then confirm:

```sh
halite-hub policy test 'node:relay1.example' '*' relay.proxy --runner
# allowed by role "relay" rule 0
```

A rule that names its principal *inside* the role (`principals:` beside
`runners:`) loads and grants nothing; the principal belongs in `bindings`.

**2. On the relay, enrol it with the upstream**, using a configuration of its
own (a separate `--root`, `pki_dir` and `node_id`) so it does not collide with the
host's own node identity:

```yaml
# /etc/halite/relay/node.yaml
hub: hub.example
node_id: relay1.example
pki_dir: /var/lib/halite/relay-pki
hub_fingerprint: '3e:3e:…'
```
```sh
halite-node enroll --root /etc/halite/relay --config /etc/halite/relay/node.yaml
halite-hub keys accept relay1.example            # on the upstream
halite-node enroll --root /etc/halite/relay --config /etc/halite/relay/node.yaml   # collect it
```

**3. Run the relay**, with a hub configuration that names the upstream:

```yaml
# the relay's hub.yaml
relay: true
relay_upstream: hub.example
relay_pki_dir: /var/lib/halite/relay-pki         # the enrolment's pki_dir
relay_spool_dir: /var/lib/halite/relay-spool     # returns during an outage
relay_event_tags:
  - halite/job/**                                # empty forwards nothing
```

then `halite-hub serve --relay --upstream hub.example`, or set the service's
flags. **Nodes behind the relay enrol with the relay**, not the upstream; their
keys are accepted there. The upstream never holds a key for them, but
`manage.up` and targeting see them, and a job submitted upstream reaches them
through the relay's stream.

The relay renews its upstream certificate itself, at half its life, and
reconnects. While the upstream is unreachable it keeps serving its segment and
**spools returns** to `relay_spool_dir`, draining them oldest first when the
link returns; watch it with `halite-hub metrics --filter relay`.

Things to know before deploying one:

- A job submitted to a relay's own command line **stays local** and does not
  appear in the upstream's job cache; run it upstream if it should.
- The upstream **trusts the relay's word** about which nodes it proxies for,
  bounded by the `relay.proxy` grant (ledger 1.8 and 1.9 say what that does and
  does not buy).
- Depth is limited (`relay_max_depth`, default 2), because unbounded nesting is
  how such estates become undebuggable.
- A relay is not high availability for the upstream. It is a closer, more
  patient ear for one segment.

### 7. High availability

There is one active hub. To shorten an outage, keep a restored standby
([chapter 35](35-backup-and-recovery.md)); to make an outage *matter less*,
give nodes `schedule:` entries, `startup_states`, and (if secrets are
acceptable on disk) `pillar_cache_disk: true`, so they converge on their own
while the hub is away.

## Verify

- [ ] You have measured your pillar compile and your highstate compile and
      written the numbers down.
- [ ] Every scheduled job has `splay` (check
      `halite-hub run '*' schedule.list`).
- [ ] The hub's dashboards show request duration, pillar duration, bus lag and
      the connected gap, with alerts.
- [ ] A rehearsed worst case: restart the hub in a lab with N simulated or real
      nodes and watch reconnection, the start-event reactor and CPU.
- [ ] If you run a relay: `policy test` allows `relay.proxy`; a node behind it
      shows in `manage.up`; stopping the upstream for a minute and starting it
      drains the spool (watch `halite-hub metrics --filter relay`).

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| The hub's CPU pegs every half hour | Everyone converging in the same second | `splay`; random delay in timers |
| `halite/reactor/overflow` events | A burst larger than `reactor_queue_depth` | Rate-limit/debounce the reactor; raise depth |
| Returns arrive slowly or go missing | The hub is saturated; or `halite_jobs_missing_returns` rising | Cores; fewer simultaneous jobs; batches |
| A relay does not connect upstream | The relay's node was not accepted, `accept_relays` is off, or the binding is missing | Steps 1 and 2; `policy test` |
| The relay's spool does not shrink | Returns are being **refused** upstream, not lost | The relay's log says which JID and why |
| `serve` refuses a relay start | `relay_upstream` is an address not in the upstream's certificate | Set `relay_server_name` to a name that is |

## Key ideas

- The targets are **targets**: compile (26 ms) and pillar (45 ms) are
  measured; hub capacity at scale is **not yet**.
- The node compiles its own states, so the hub's load is pillar, files,
  returns and events. **Measure your pillar compile first.**
- **Splay everything** that touches the hub; most scale problems are
  synchronized fleets.
- A **relay** serves a segment, spools returns through an upstream outage,
  and renews its own certificate; it is not hub high availability.
- Keep the outage window of the single hub in mind; give nodes the means to
  converge without it.

Next: [troubleshooting](39-troubleshooting.md).
