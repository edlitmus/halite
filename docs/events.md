# The event bus

The control plane publishes an event for everything that happens to the
fleet: an agent says hello, a key is accepted, a job is dispatched, an
agent returns. Agents publish their own events upward. Operators tail the
stream, and — once the reactor lands — rules act on it.

```sh
halite events
halite events -tag 'halite/job/**'
halite events -tag 'halite/agent/*/hello'
halite events -tag 'halite/beacon/**' -history 50 -json
```

## Tags

Tags are slash-delimited, most general segment first, following Salt's
convention. `*` matches within one segment; `**` matches any number of
segments, including none.

| Pattern | Matches |
|---|---|
| `**` | everything |
| `halite/job/**` | every job event |
| `halite/job/*/dispatch` | dispatches only |
| `halite/job/*/ret/*` | returns only |
| `halite/agent/web*/hello` | hellos from agents whose id starts with web |

`halite/job/*` does **not** match `halite/job/123/ret/web1` — a single star
stays inside its segment. Use `halite/job/**` when you mean "anything
below here".

## What the control plane publishes

| Tag | Raised when | Data |
|---|---|---|
| `halite/key/<id>/pending` | an enrollment request is filed | `id`, `state` |
| `halite/key/<id>/accepted` | a request is accepted | `id`, `state` |
| `halite/key/<id>/rejected` | a request is rejected | `id`, `state` |
| `halite/agent/<id>/enrolled` | an agent collects its certificate | `id` |
| `halite/agent/<id>/hello` | an agent connects or reconnects | `id`, `version` |
| `halite/job/<jid>/dispatch` | work is queued | `job_id`, `kind`, `target`, `test`, `agents` |
| `halite/job/<jid>/ret/<agent>` | an agent answers | `job_id`, `result`, counts, `error` if it failed |

Events carry a time-ordered `id`, a timestamp, and a `source` — the agent
id, or `master` for events the control plane raises itself.

## Agents raise events too

An agent `POST`s to the same endpoint, and the control plane stamps the
source from its client certificate. An agent cannot raise an event
attributed to another host or to the control plane, however it fills in the
body. That is what makes a reactor rule keyed on `.Source` safe.

## Streaming

`GET /v1/events` is newline-delimited JSON, flushed per event, held open
until the client disconnects. This is the long-lived stream ADR-5 reserved
for the bus; job delivery stays a long poll, because agents need work
pushed at them and operators need to watch.

* `?tag=<pattern>` subscribes to a subset.
* `?history=N` replays the last N matching events first; `?history=0`
  replays everything the bus still holds. Without it, a tail starts from
  now.
* A blank line every 30 seconds keeps intermediaries from reaping an idle
  stream.

Only operator certificates may stream. An agent posting its own events is
fine; an agent reading the whole fleet's activity is not.

## What the bus is not

* **Not durable.** The bus keeps the last 1000 events in memory and loses
  them on restart. It is for reacting and watching, not for audit. Durable
  history is what returners are for.
* **Not guaranteed.** A subscriber that stops reading fills its buffer
  (256 events) and then loses events rather than blocking the control
  plane. A slow consumer degrades itself, never the fleet.
* **Not ordered across sources.** Ids sort chronologically by the control
  plane's clock, which is the only clock involved — but an agent's event
  is timestamped when the control plane receives it, not when the agent
  raised it.

## Returners

The bus is in-memory and lossy on purpose. Returners are the durable half:
every finished job result is written to whatever sinks the control plane
was started with.

```sh
halite master -root /usr/local/etc/halite/states \
  -returner file:/var/log/halite/results.ndjson \
  -returner webhook:https://example.com/halite
```

`-returner` is repeatable and takes `kind:target`:

| Kind | Target | Writes |
|---|---|---|
| `file` | a path | one JSON object per line, appended, mode 0600 |
| `webhook` | an http(s) URL | one POST per result, `Content-Type: application/json` |

Each record is the job and one agent's answer to it, including every state
outcome:

```json
{"time":"2026-08-09T21:31:02Z",
 "job":{"id":"20260809213101.882-4e58","kind":"state.highstate","target":"*"},
 "result":{"job_id":"...","agent_id":"web1","result":true,"succeeded":3,"changed":1,
           "states":[{"id":"nginx","function":"pkg.installed","result":true,"changed":false,
                      "comment":"nginx is already installed"}]}}
```

Records go through a queue, so a slow webhook delays its own writes and
nothing else. A full queue drops records and logs it, on the same principle
as the bus: a sink must never stall the control plane or an agent's return.
Queued records are flushed on a clean shutdown.

**Results can contain file diffs.** The file returner is mode 0600 for that
reason, and a webhook endpoint sees whatever the diffs contain — use
`show_diff: false` on states that write confidential files. See
[pillar-security.md](pillar-security.md).

There is no database returner: a Postgres or MySQL sink means a driver, and
drivers are dependencies (ADR-1). Point a webhook at something that owns
the database instead, or ship the NDJSON file.

## The reactor

Reactor rules turn events into jobs. The control plane loads them from a
file and evaluates them against every event on the bus:

```sh
halite master -root /usr/local/etc/halite/states -reactor /usr/local/etc/halite/reactor.sls
```

```yaml
# reactor.sls — tag patterns to work
'halite/agent/*/hello':
  - run:
      kind: state.highstate
      target: '{{ .Source }}'

'halite/beacon/*/service-down':
  - run:
      kind: call
      target: '{{ .Source }}'
      fn: service.running
      args:
        name: '{{ .Data.service }}'

'halite/key/*/accepted':
  - run:
      kind: state.apply
      target: '{{ .Data.id }}'
      sls:
        - baseline
      test: "true"
```

Each rule is a tag pattern mapped to a list of `run:` actions. An action
takes the same fields as `halite run`: `kind`, `target`, `sls`, `fn`,
`args`, `test`. Every string is a Go template evaluated against the event:

| In a rule | Is |
|---|---|
| `{{ .Source }}` | the agent that raised it, from its certificate |
| `{{ .Tag }}` | the full tag |
| `{{ .Data.x }}` | a field of the event's data |
| `{{ .ID }}`, `{{ .Time }}` | the event's identity |

A template referencing something the event does not carry is an **error**,
not an empty string: the rule is logged and does not fire. Dispatching a
job with a blank target would be worse than not reacting.

### Loops

Reacting to job events with a job is a loop: the dispatch and the return
both raise events that match the same rule. Two things prevent a runaway:

* **Work the reactor caused is marked.** Both the dispatch event and every
  return event of a reacted job carry `reactor: true`, and the reactor
  ignores those. A rule on `halite/job/**` therefore reacts to operators'
  work and to its own results exactly once, then stops.
* **Reactions are rate limited** to 60 per minute. If a rule still manages
  to feed itself, the limit stops it and logs which rule is responsible,
  rather than letting it consume the fleet.

### Trusting event data

`.Source` comes from the agent's client certificate and cannot be forged.
Everything in `.Data` is whatever the agent put there.

A rule like `target: '{{ .Source }}'` is safe: an agent can only ever cause
work on itself. A rule like `target: '{{ .Data.host }}'` lets any agent
that can raise that event choose who the job runs on — including `*`.
Prefer `.Source` for targets, and treat `.Data` as untrusted input the same
way you would a request body.

## Beacons

Beacons are agent-side watchers. They notice something on the host and
raise an event, which the reactor can act on.

```sh
halite agent -master master.example.com -beacons /usr/local/etc/halite/beacons.sls
```

```yaml
# beacons.sls
disk:
  - mount: /var
    threshold: "90"
    interval: 60s
  - mount: /
service:
  - name: nginx
    interval: 30s
file:
  - path: /usr/local/etc/nginx/nginx.conf
    interval: 10s
```

Each kind maps to a list, so one kind can watch several things.

| Beacon | Fires when | Data |
|---|---|---|
| `disk` | usage crosses `threshold` percent, and again when it drops back | `mount`, `used`, `threshold`, `over` |
| `service` | a service stops running, and again when it returns | `service`, `running` |
| `file` | a watched file is created, changed, or removed | `path`, `change`, `sha256` |

`interval` defaults to 60s and must be at least a second. `disk` defaults
to `/` at 90%. Beacons are checked once at startup, so a service that is
already down when the agent connects is reported immediately.

### Edge triggering

**A beacon fires on change, not on condition.** A disk sitting at 95%
raises one event when it crosses the threshold and one when it drops back
under — not one per check. Without this, a reactor rule keyed on a full
disk would dispatch a job every interval, forever.

Two consequences worth knowing:

* The `file` beacon's first check is a baseline. A file that already exists
  when the agent starts is not a change. `disk` and `service` do report at
  startup, because "already broken" is news in a way that "already exists"
  is not.
* `file` compares content digests, so rewriting a file with identical bytes
  is correctly not a change, whatever the mtime says.

A beacon that panics is logged and its watcher keeps ticking; a broken
watcher does not take the agent down. A beacon event that cannot be
delivered is dropped, and the condition is reported again the next time it
changes.

### Beacon tags are constrained

An agent may raise exactly `halite/beacon/<its-own-id>/<name>`. Anything
else is refused with a 403.

The source of an event already comes from the client certificate, but
reactor rules match on the **tag** — so without this, `web1` could raise
`halite/beacon/db1/service` and fire a rule written for `db1`. The tag is
as trustworthy as the source; the *data* inside it is still whatever the
agent chose to send.

## The mine

The mine is how one host's states learn facts about other hosts. Agents
publish on a schedule, the control plane keeps the latest value per agent
per function, and states read the lot as `{{ .Mine }}`.

```sh
halite agent -master master.example.com -mine /usr/local/etc/halite/mine.sls
```

```yaml
# mine.sls — what this host publishes
grains:
  interval: 5m
network.interfaces:
  interval: 60s
disk.usage:
```

Publishable functions are the read-only execution modules (`disk.usage`,
`status.uptime`, `status.loadavg`, `network.interfaces`) plus `grains`. A
name that is neither is refused when the agent starts, so a typo is a
startup error rather than a function that silently never publishes. The
interval defaults to five minutes; each function publishes once at startup
so a host that has just connected is usable straight away.

### Using it in states

```
# files/backends.conf.tmpl
upstream backends {
{{- range $agent, $data := .Mine.grains }}
{{- if hasPrefix $agent "web" }}
    server {{ $agent }}.internal;
{{- end }}
{{- end }}
}
```

`.Mine` is `function -> agent -> data`. Dots in a function name become
underscores so the path resolves: `network.interfaces` is
`{{ .Mine.network_interfaces }}`.

Masterless and `halite ssh` runs get an empty `.Mine` — there is no fleet
to gather from. A state tree that iterates over it produces nothing rather
than failing.

### Reading it by hand

```sh
halite mine
halite mine network.interfaces
halite mine grains -target 'os_family:FreeBSD'
halite mine disk.usage -json
```

### What the mine is not

* **Not durable.** It lives in the control plane's memory and is empty
  after a restart until agents republish, which they do on their next
  interval.
* **Not private between agents.** Any enrolled host can read what every
  other host publishes — that is what makes it useful, and it means the
  mine is for facts, not for secrets. Pillar is the private channel.
* **Not fresh.** An entry is as old as its interval, and it carries the
  time it was published so consumers can judge. An agent that has gone
  away leaves its last value behind until the control plane restarts.
