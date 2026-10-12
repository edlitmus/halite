# Beacons

<!-- abstract: Watchers on the node that put an event on the bus when something changes: configuration, every built beacon with real output, the loop-prevention controls, and runtime management. -->
<!-- kind: guide -->
<!-- index: beacon; beacons; diskusage; filechanges; load; memusage; proc; ps; service; cert_info; status; onchangeonly; interval; disable_during_state_run; rate_limit; coalesce_window; queue_depth; beacons.d; beacons.add; beacons.list; max_causality_depth; inotify; self-inflicted loop -->

A **beacon** is a small watcher that runs on a node and fires an event
when something it watches changes. It is the *senses* of the automation
half of halite: the disk is filling, a configuration file was edited, a
service stopped, a certificate is about to expire. The event goes onto the
bus ([chapter 18](18-events.md)); what happens next is up to a **reactor**
([chapter 20](20-reactors.md)) or to a person reading the stream.

A beacon does not decide anything and does not change anything. It
observes and reports. That separation is deliberate: the thing that
notices and the thing that responds are written, reviewed and permitted
separately.

## Configuring beacons

Beacons are configured in the node's `node.yaml`, under `beacons:`, or in
files in a `beacons.d/` directory beside it. This is the configuration
used to produce every event shown in this chapter:

```yaml
beacons:
  diskusage:
    - /: 1%
    - interval: 5
  load:
    - 1m: ['>', 0.0]
    - interval: 5
    - onchangeonly: True
  filechanges:
    - files:
        - /tmp/halite-watched.txt
    - interval: 2
  proc:
    - processes:
        sshd: running
    - interval: 6
  service:
    - services:
        ssh:
          onchangeonly: True
    - interval: 5
  cert_info:
    - files:
        - /etc/ssl/certs/site.pem
    - notify_days: 30
    - interval: 3600
  status:
    - functions:
        - status.uptime
    - interval: 8
```

(The thresholds are set artificially low so that the beacons fire at once.
Use real ones.)

The shape is a map from beacon name to a **list of single-key mappings**.
It looks odd and it is the shape the predecessor used, so existing
configuration works unchanged. The items of the list are the beacon's own
settings (`/: 85%`, `files:`, `processes:`) mixed with common ones:

| Setting | Meaning |
|---|---|
| `interval` | Seconds between checks. |
| `onchangeonly` | Fire only when the observation differs from the last one, not every interval. |
| `disable_during_state_run` | Do not fire while a state run is in progress on this node. Prevents a state run from triggering itself. |
| `delay`, `emitatstartup` | Delay the first check; fire once at start. |
| `rate_limit`, `coalesce_window`, `queue_depth` | The backpressure controls, below. |

A beacon this build does not have, or one that is declared and not built,
**stops the node starting** rather than being skipped: a watcher that is
configured and silently not running is indistinguishable from a quiet
machine.

## The tag

A beacon fires under

```
halite/beacon/<node ID>/<beacon>/<what>
```

where `<what>` is the thing watched with its leading slash removed. So a
reactor can listen to exactly one filesystem, one file or one service:

| Beacon event | Tag |
|---|---|
| root filesystem | `halite/beacon/db1.lab/diskusage/root` |
| the file `/tmp/halite-watched.txt` | `halite/beacon/db1.lab/filechanges/tmp/halite-watched.txt` |
| the service `ssh` | `halite/beacon/db1.lab/service/ssh` |
| the process `sshd` | `halite/beacon/db1.lab/proc/sshd` |
| load, one minute | `halite/beacon/db1.lab/load/1m` |
| the status beacon | `halite/beacon/db1.lab/status` |

The root filesystem is `root`, not an empty string, because a tag that ends
at the beacon's own name could not be reached by `halite/beacon/*/diskusage/**`.

## The beacons that ship

Each of these was configured on a node as above and the event shown is the
one that appeared on the bus.

### `diskusage`: a filesystem is too full

Each key is a mount point, its value a percentage. Fires when usage is
above it.

```yaml
diskusage:
  - /: 85%
  - /var: 90%
  - interval: 60
```

```json
{"mount": "/", "percent_used": 38, "threshold": 1}
```

### `filechanges`: a file changed

Polls the digest, size and mode of each file in `files` and reports what it
reads. **Add `onchangeonly: True`**: without it the beacon reports the
reading on *every* interval, changed or not (verified: a file left alone
produced an identical event every two seconds), and it is `onchangeonly` that
turns the readings into "this file changed", or appeared, or disappeared.

```yaml
filechanges:
  - files:
      - /etc/nginx/nginx.conf
  - interval: 10
  - onchangeonly: True
```

```json
{"exists": true, "hash": "2c8b08da5ce6…", "mode": "0664",
 "path": "/tmp/halite-watched.txt", "size": 4}
```

This build watches by **polling** on every platform; the efficient
kernel-notification beacons (`inotify`, `fanotify`, `fsevents`) are not
built, so very short intervals on large file sets cost CPU. For
configuration files, an interval of a few seconds is ample.

### `load`: the load average

`1m`, `5m` and `15m` each take a comparison operator and a number.

```yaml
load:
  - 1m: ['>', 2.0]
  - 15m: ['>', 1.0]
  - interval: 30
  - onchangeonly: True
```

```json
{"comparison": ">", "load": 1.31, "threshold": 0, "window": "1m"}
```

A key that is not a valid identifier, such as `1m`, cannot be typed as a
keyword on the command line, so `beacons.add` takes the whole configuration
as JSON (below).

### `service`: a service changed state

Each key under `services` is a service name.

```yaml
service:
  - services:
      nginx:
        onchangeonly: True
  - interval: 5
```

```json
{"running": true, "service": "ssh"}
```

With `onchangeonly`, it fires when the service goes from running to stopped
or the other way, which is the event a reactor wants to attempt a restart.

### `proc`: a process is there, or is not

Each key under `processes` is a pattern matched against the process name
(or the whole command line with `full: true`). A value of `running` or
`stopped` emits only in that case.

```yaml
proc:
  - processes:
      sshd: running
      my-batch-job: stopped
  - interval: 10
```

```json
{"count": 3, "pids": [5321, 78544, 78769], "process": "sshd", "running": true}
```

### `ps`: a process crosses a resource threshold

Like `proc`, with a comparison on a field of the process table:

```yaml
ps:
  - processes:
      java:
        cpu_percent: ['>', 80]
  - interval: 30
```

### `cert_info`: a certificate is about to expire

Fires when a certificate in `files` expires within `notify_days`.

```yaml
cert_info:
  - files:
      - /etc/ssl/certs/site.pem
  - notify_days: 30
  - interval: 3600
```

```json
{"expires_within_days": 30, "path": "/…/test-cert.pem"}
```

This is the cheapest outage prevention in the system: a reactor on this
tag can renew the certificate, or open a ticket, or at least tell a
person, a month early. (Found while writing this chapter: the beacon asked
the certificate function a question it could not answer and so always fired
an error; it was fixed with a test that runs the two together. See the
ledger.)

### `status`: report a value on a clock

Emits the result of selected `status` functions every interval, so a
consumer can *pull* a metric by listening instead of polling each node.

```yaml
status:
  - functions:
      - status.uptime
  - interval: 60
```

```json
{"status.uptime": {"human": "16h36m0s", "seconds": 59751, "since": "2026-10-11T02:23:57Z"}}
```

### `memusage`: memory in use

Documented to fire when memory in use is above `percent`.

> **Known defect:** on every platform this build supports, `memusage`
> fires an error event instead:
> `status.meminfo did not report a total and an available figure`. The
> `status.meminfo` function it reads returns only total memory and total
> swap, with no figure for memory available, so the beacon cannot compute
> a percentage. It was found by running the beacon on a node; its test had
> supplied an answer the real function does not give. Until it is fixed
> (the ledger records it), watch memory with a `status` beacon calling
> `status.meminfo` plus your own arithmetic, or with the metrics endpoint
> ([chapter 34](34-monitoring.md)).

## Not built

`halite-node call beacons.list available=True` prints every beacon the
build knows of and, for each that is not usable, **why**. At the time of
writing these are named but not built, and listing one in the configuration
stops the node starting:

| Beacon | Platform | Status |
|---|---|---|
| `inotify`, `fanotify` | Linux | Not built; use `filechanges`. |
| `fsevents` | macOS | Not built; use `filechanges`. |
| `watchdirs`, `eventlog` | Windows | Not built. |
| `journald`, `pkg` | Linux | Not built. |
| `cpuusage`, `swapusage`, `network_info`, `network_settings`, `log`, `sh`, `wtmp`, `btmp` | various | Not built. |

Beacons delivered through pillar are also not built; beacon configuration
lives in `node.yaml` and `beacons.d/`, which you can manage with a state.

## Preventing the self-inflicted loop

Beacons invite one classic accident: a file that changes in a loop fires a
beacon that fires a reactor that changes the file. halite addresses it at
every point on the path:

| Control | What it does |
|---|---|
| **Token-bucket rate limit** per beacon instance | Caps events per second so a noisy source cannot flood the bus. |
| **Coalescing** (`coalesce_window`, default 1 s) | Identical events in the window become one, carrying `_count`. |
| **Bounded queue** (`queue_depth`, default 1000) | On overflow the oldest are dropped, **and a single overflow event reports how many**: loss is reported, never silent. |
| **`disable_during_state_run`** | Stops a state run from setting off the beacon that watches the file the state is writing. |
| **Causality depth** | Meant to break a chain deeper than `max_causality_depth` (default 5) with an alert. **Not reliable in this build:** it does not catch a loop through a reaction's `event.send` ([chapter 20](20-reactors.md)). Do not rely on it. |

Use `disable_during_state_run: True` on any beacon that watches a file a
state manages. It is the single most useful setting here.

## Managing beacons at runtime

From the hub, with the usual functions:

```sh
halite-hub run db1.lab beacons.list
halite-hub run db1.lab beacons.disable_beacon name=memusage
halite-hub run db1.lab beacons.add name=load beacon_data='{"5m": [">", 99.0], "interval": 30}'
```

| Function | Does |
|---|---|
| `beacons.list` | What is running, with intervals and whether enabled (`available=True` lists every beacon this build knows). |
| `beacons.add` / `modify` / `delete` | Change one beacon. |
| `beacons.enable` / `disable` | Hold every beacon. |
| `beacons.enable_beacon` / `disable_beacon` | Hold one. |
| `beacons.save` | Write runtime changes to `beacons.d/99-runtime.yaml`. |
| `beacons.reset` | Re-read the directory, discarding unsaved runtime changes. |

Two details. Changes made this way live **in memory** until `save`; and
`add`, `modify` and `delete` work on a node that already runs at least one
beacon, because with none configured there is no running set to add to
(the call is refused and says so). The durable way to change beacons is
declarative: manage `beacons.d/` with a state, and the change is
reviewed, versioned and applied to every node that should have it.

## Practical advice

- **Start with the worst day.** The five beacons that earn their keep:
  `diskusage` (full disks), `service` (a daemon stopped), `cert_info`
  (expiry), `filechanges` on files that must never change, `load` for
  runaway work.
- **Use `onchangeonly`** for facts (is the service up?) and leave it off
  for thresholds (is the disk above 85%?) where you may want repeated
  reminders at the interval.
- **Watch the files your states own** with `disable_during_state_run`.
- **A beacon produces data; filter at the reactor.** Do not try to
  encode policy in thresholds that have to be edited on every node.
- **Beacons add load.** `interval: 1` across thousands of nodes is a
  decision, not a default.

## Key ideas

- A **beacon** watches on the node and fires `halite/beacon/<node>/<beacon>/<what>`
  when its condition holds.
- Configured in `node.yaml` or `beacons.d/` as a **list of single-key
  mappings**; common settings `interval`, `onchangeonly`,
  `disable_during_state_run`.
- Nine ship: `diskusage`, `filechanges`, `load`, `service`, `proc`, `ps`,
  `cert_info`, `status`, `memusage` (the last with a known defect); the
  rest are named but not built and stop the node starting.
- Rate limit, coalescing, a bounded queue, `onchangeonly` and
  `disable_during_state_run` keep a beacon-reactor loop from running away; the
  hub-side causality limit is not yet a dependable brake.
- Manage at runtime with `beacons.*`; make it durable with a state.

Next: [reactors](20-reactors.md), which respond.
