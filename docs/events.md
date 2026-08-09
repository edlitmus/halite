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
