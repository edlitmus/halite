# The scheduler and returners

<!-- abstract: Work on a timetable, run by the node itself; and where results and events go: local files, syslog, mail and signed webhooks with a durable spool. -->
<!-- kind: guide -->
<!-- index: scheduler; schedule; cron; every; splay; maxrunning; run_on_start; catchup; schedule.list; schedule.add; schedule.run_job; schedule.d; returner; webhook returner; syslog returner; smtp returner; file returner; local returner; event_return; spool; X-Halite-Signature; HMAC; SIEM; return_job -->

Two small subsystems finish the automation picture. The **scheduler** makes
a node do something on a clock, with no hub involved. **Returners** decide
where the results of that work, and the events of the whole system, go once
they leave the machine.

## The scheduler

A node can run functions on a timetable, from its own configuration:

```yaml
schedule:
  nightly_highstate:
    function: state.apply
    cron: '17 3 * * *'
    splay: 900
    maxrunning: 1
    return_job: True

  collect_inventory:
    every: 15m
    function: grains.items
    return_job: False
    run_on_start: True
```

This matters most exactly when the hub cannot be reached: a node that
schedules its own highstate keeps itself converged through a hub outage.

What a node reports about the schedule above (the first entry, run on a
lab node):

```
nightly_highstate:
    function: state.apply
    cron: 17 3 * * *
    splay: 15m0s
    return_job: True
    enabled: True
    timezone: Local
    next_fire_time: 2026-10-12T03:17:00-07:00
```

### When it runs

| Setting | Meaning |
|---|---|
| `cron: '17 3 * * *'` | A five-field cron expression; also `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`, `@reboot`. Names for months and days work. |
| `every: 15m` | A fixed period. |
| `seconds`, `minutes`, `hours`, `days` | Periods that add up. |
| `when`, `once`, `once_fmt` | At a given time or times; once. |
| `after`, `until`, `range` (with `invert`), `skip_during_range`, `skip_explicit` | Windows in which a job may or may not run. |
| `timezone: <IANA name>` | The zone to evaluate in. Default: the node's local time. Set it on a fleet that spans zones, or `0 3 * * *` means a different moment on every machine. |

And what it does:

| Setting | Meaning |
|---|---|
| `function`, `args`, `kwargs` | What to run. |
| `splay` | A random delay up to this many seconds (or a `start`/`end` range) so a fleet does not act in the same second. **Use it on anything that touches the hub.** |
| `maxrunning` | How many copies may run at once. |
| `run_on_start` | Also run when the agent starts. |
| `catchup: true` | Run a missed job once on start (the node was off at 03:17). |
| `enabled` | Turn a job off without deleting it. |
| `return_job` | Send the job's return to the returner. |
| `offset`, `metadata` | A shift of the clock; opaque notes. |

Not supported, and refused by name rather than guessed at: the cron
extensions `L`, `W`, `#`, `?` and a seconds field. `jid_include` is
accepted and means nothing.

Two cron behaviours surprise people and are the standard ones: when both
the day-of-month and day-of-week fields are restricted, **either** matches
(`0 0 1 * mon` runs on the first of the month *and* on Mondays); and across
a daylight-saving change, an hour that repeats runs a job once, and an
hour that is skipped runs it once, at the change.

### Where schedules live

`schedule:` in `node.yaml`, or one file per concern in `schedule.d/`. A
file in `schedule.d/` is a mapping of names to definitions with no
`schedule:` above them, because the directory already says what they are.
Keeping schedules in `schedule.d/` and managing the directory with a state
means every node gets the schedule it should and the schedule is reviewed
like everything else.

### Managing it at runtime

```sh
halite-hub run web2.lab schedule.list
halite-hub run web2.lab schedule.show_next_fire_time name=nightly_highstate
halite-hub run web2.lab schedule.run_job name=nightly_highstate
```

| Function | Does |
|---|---|
| `schedule.list` | The jobs, with their next fire time. |
| `schedule.show_next_fire_time name=…` | When one will next run. |
| `schedule.add`, `modify`, `delete` | Change a job. |
| `schedule.enable`, `disable` | Hold the whole schedule. |
| `schedule.enable_job`, `disable_job` | Hold one. |
| `schedule.run_job` | Run one now, out of turn and without splay. |
| `schedule.save`, `reload` | Write runtime changes to `schedule.d/99-runtime.yaml`; re-read the directory and discard unsaved changes. |

A change made this way lives in memory until `save`. `add` and friends work
on a node that already runs at least one scheduled job; on a node with
none they are refused, because there is no running set to add to. And
`schedule.run_job` and `schedule.add` can run arbitrary code, so a
wildcard in the policy never grants them: a role has to name them.

> **Not built:** schedules delivered through pillar.

### The machine's own scheduler

cron, a systemd timer or launchd run `halite-node state apply --local`
equally well, and there is a real argument for leaving them in charge: they
are one less thing in the agent, and every operator can read a crontab. The
shipped timer is `contrib/systemd/halite-highstate.timer`, and on FreeBSD
`contrib/rc.d/halite_highstate` plus a cron line
([chapter 33](33-configuration.md)). Use the built-in scheduler when you want
the schedule to travel with the node, change without a restart, or catch up
after an outage.

## Returners

A **return** is a job's result ([chapter 15](15-jobs.md)). For a job the
hub sent, it goes to the hub, which keeps it in the job cache. For a job the
node ran **by itself** (a scheduled job with `return_job: True`), there is
no hub waiting, and the **returner** says where it goes. Set it in
`node.yaml`:

```yaml
returner: local
```

| Returner | Sends results to | Notes |
|---|---|---|
| `local` (default) | An append-only NDJSON file, `returns.ndjson` in the node's `state_dir` | One JSON return per line. |
| `local_cache` | The hub's job cache | |
| `file` | An NDJSON file at `returner_file`, with rotation | |
| `syslog` | RFC 5424 syslog: local socket or TCP, optionally over TLS | |
| `webhook` | An HTTPS endpoint, **signed, retried and spooled** | The one to read about below. |
| `smtp` | One mail per return | |
| `mysql`, `postgres`, `redis`, `elasticsearch`, `splunk`, `slack`, `kafka`, `sqs`, … | **Extensions** of kind `returner` | Each needs a driver or vendor client, which is why none are linked in. `returner: postgres` finds the extension by name. |

A returner name this build does not have is **not fatal at start-up**: it
becomes a returner that fails every return with the reason, said once. That
is deliberate: a returner extension arrives through
`saltutil.sync_returners`, which needs a running node, so a node that
refused to start could never be sent what it is waiting for. It does not
fall back to `local` either, which would put the returns in a file nobody is
watching while the configuration says they are in a database. A genuine
misconfiguration (an `http://` URL, a missing secret) is fatal, because you
can fix it by reading.

A line of the `local` returner, from a node running a scheduled
`test.echo`:

```json
{"jid":"20261011T190815686639","id":"web2.lab","fun":"test.echo","fun_args":["tick"],
 "success":true,"retcode":0,"return":"tick","start_time":"2026-10-11T19:08:15.686707593Z",
 "duration_ms":1,"node_version":"0.1.0-7-g8ad761ce275b+8ad761ce275b","schema":"halite.ret/1"}
```

### The webhook returner

For getting results into a system of your own, the webhook is the sturdy
choice:

```yaml
returner: webhook
returner_webhook_url: https://ci.example/halite/returns
returner_webhook_secret_file: /etc/halite/returner.secret
returner_webhook_ca_file: /etc/halite/internal-ca.pem
returner_webhook_attempts: 5
returner_spool_max_size: 268435456
```

It does three things together, and the third is the one that matters:

1. **Signs** every request: an HMAC-SHA-256 of the timestamp, a `.`, and the
   raw body, keyed with the shared secret, in `X-Halite-Signature`, with
   `X-Halite-Timestamp` and a random `X-Halite-Nonce`. A receiver can
   therefore tell the request is genuine, unmodified and fresh. `X-Halite-Node`
   and `X-Halite-Kind` say who it concerns and what it is.
2. **Retries with backoff**, except on a `4xx`, which means the receiver
   understood and refused (retrying would fill a disk with a request nobody
   will accept).
3. **Spools to disk** when the receiver is down, and delivers the backlog
   *oldest first, ahead of new returns*, when it comes back. A returner
   without a spool loses exactly the returns from the incident that took
   the receiver down. A full spool refuses rather than making room: a spool
   that silently discards is the failure it exists to prevent.

The URL must be `https://`; `returner_webhook_ca_file` lets an internal CA
work without anyone reaching for a way to skip verification.

Run in the lab, the whole thing: a node scheduled a job every ten seconds,
and the receiver was stopped for about a minute and restarted. The returns
that accumulated while it was down were held in the node's spool directory;
once it was back they all arrived, in chronological order, each with a valid
signature, and the spool ended empty. The receiver:

<!-- example: handbook/automation/webhook-receiver.py -->
```python
#!/usr/bin/env python3
# A receiver for halite's webhook returner, in forty lines of the standard
# library, that checks the signature the way any receiver must.
#
#     python3 webhook-receiver.py SECRET_FILE CERT.pem KEY.pem
#
# The returner (or `event_return: webhook` on a hub) POSTs JSON over HTTPS
# with these headers:
#
#     X-Halite-Timestamp   when it was signed
#     X-Halite-Signature   hex HMAC-SHA-256, keyed with the shared secret, of
#                          the timestamp, a ".", and the raw body
#     X-Halite-Nonce       random, for replay detection
#     X-Halite-Node, X-Halite-Kind   which node, and what kind of record
#
# Verify BEFORE parsing the body, with a constant-time comparison, and refuse
# a timestamp that is too old. This receiver does the first two; a real one
# does the third too.
import hashlib
import hmac
import http.server
import json
import ssl
import sys

secret = open(sys.argv[1]).read().strip().encode()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        stamp = self.headers.get("X-Halite-Timestamp", "")
        got = self.headers.get("X-Halite-Signature", "")
        want = hmac.new(secret, stamp.encode() + b"." + body, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(got, want):
            self.send_response(401)
            self.end_headers()
            return
        print(json.dumps({"path": self.path, "node": self.headers.get("X-Halite-Node"),
                          "record": json.loads(body)}), flush=True)
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):
        pass


server = http.server.HTTPServer(("127.0.0.1", 14599), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(sys.argv[2], sys.argv[3])
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
```

The same signature construction is used by the API's webhook *ingress*, so
an estate that has written one verifier has written both
([chapter 27](27-http-api.md)).

## Sending the event stream out

Returners also carry the **event bus**. `event_return` on the **hub**
ships selected events to a returner, which is the recommended path to a
SIEM or an alerting system:

```yaml
# hub.yaml
event_return: webhook
event_return_tags: 'halite/audit/**,halite/reactor/**,halite/key/**'
returner_webhook_url: https://alerts.example/halite/events
returner_webhook_secret_file: /etc/halite/returner.secret
returner_webhook_ca_file: /etc/halite/internal-ca.pem
```

or `event_return: syslog` with `returner_syslog_address` and
`returner_syslog_tls: true`. Run in the lab with the tags `halite/audit/**`:
an event sent as `halite/audit/demo` was delivered, signed and with its
bus offset; one sent as `halite/other/ignored` was not.

It **resumes from a bus offset**, so a receiver that was unreachable for an
hour catches up and leaves no hour-shaped hole. A failed delivery does not
advance the offset. `event_return_from: earliest` ships the existing
backlog on a first run; the default is `latest`, because shipping a month of
history into a SIEM on first boot is a bill and an alert storm. `local_cache`
is refused here: it would be the bus writing into the cache of the hub that
owns the bus.

This is also the answer to "how do I get a notification?". The hub has no
outbound mail or chat runner in this build; the event goes out through a
returner, and the system on the far end does the telling. A reaction that
wants to tell somebody sends an `halite/audit/…` event
([chapter 20](20-reactors.md)).

## Key ideas

- The **scheduler** runs functions on a node's own clock, from `schedule:`
  or `schedule.d/`: `cron`, `every`, `when`, `range`; **always set `splay`**;
  `maxrunning`, `run_on_start`, `catchup`.
- Change it at runtime with `schedule.*`; `save` to persist.
- A **returner** says where results go: `local` by default; `webhook` for
  your own system.
- The **webhook returner** signs (HMAC-SHA-256 over `timestamp.body`),
  retries, and spools to disk; the backlog goes out oldest first.
- `event_return` on the hub ships the **event stream** to a returner, from an
  offset it remembers.

Next: [putting it all together](24-scenarios.md).
