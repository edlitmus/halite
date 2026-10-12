# Reactors

<!-- abstract: Rules that turn events into actions on the hub: configuration, reaction files, the four reaction types, authorization as a principal, rate and loop controls, and how to test one before it fires. -->
<!-- kind: guide -->
<!-- index: reactor; reaction; reaction SLS; reactor principal; reactor:default; local reaction; runner reaction; wheel reaction; caller reaction; debounce; dedupe_window; dedupe_key; rate_limit; reactor.test; reactor.list; halite/reactor/error; reactor_workers; reactor_queue_depth; reactor_timeout; max_causality_depth; self-healing -->

A **reactor** connects an event to an action. It is a rule, held in the
hub's configuration:

> When an event whose tag matches *this pattern* arrives, run *that
> reaction file*.

With beacons ([chapter 19](19-beacons.md)) noticing things and the event
bus ([chapter 18](18-events.md)) carrying the news, reactors close the
loop: something happens on a machine, the hub notices, the hub acts.

Typical reactions:

- A watched configuration file changed on a node: record it, tell someone,
  or re-apply the state that owns it.
- A node connected: rehearse (or apply) its highstate.
- A disk crossed 90%: run a cleanup, or open a ticket.
- A service stopped: try to start it, and tell someone if that fails.
- A deployment finished (a node sent `deploy/finished`): start the next
  stage.

## Configuring a reactor

In `hub.yaml`. This is the working configuration from the example tree,
`contrib/examples/handbook/automation/hub-reactor.yaml`:

<!-- example: handbook/automation/hub-reactor.yaml -->
```yaml
# The reactor: section of hub.yaml.
reactor:
  # The Salt-shaped form: a tag glob and the reaction files it runs.
  - 'halite/node/*/start':
      - /etc/halite/reactor/node-started.sls

  # The form that carries the controls and the identity.
  - tag: 'halite/beacon/*/filechanges/**'
    sls:
      - /etc/halite/reactor/config-changed.sls
    principal: 'reactor:watch'
    debounce: 5s
    dedupe_window: 30s
    rate_limit: 600/m
```

Two forms:

- **The short form** is a single tag glob mapped to a list of files. It
  exists because an existing configuration uses it. It has nowhere to put a
  principal, so its reactions run as `reactor:default`, which is bound to
  nothing unless you bind it — so they are refused until you do.
- **The full form** is a mapping with `tag`, `sls` and any of `principal`,
  `debounce`, `dedupe_window`, `dedupe_key` and `rate_limit`.

Patterns follow the bus rules: `*` is one tag segment and `**` is many
([chapter 18](18-events.md)).

A reaction file is named by an absolute path on the hub, or by a
`salt://`/`halite://` name in the hub's own tree, so reactions can live in
version control with everything else. The hub reads the configuration when
it starts; there is no reload, so changing a reactor means restarting the
hub.

## A reaction file

A reaction is an SLS file, rendered by the hub when its event arrives. It
declares what to do, using the same ID/function/arguments grammar as a
state, with functions from four families:

```yaml
# /etc/halite/reactor/config-changed.sls
{% set node = data['id'] %}

record_{{ node }}:
  local.cmd.run:
    - tgt: {{ node }}
    - arg:
      - echo "{{ data['path'] }} changed on {{ node }}" >> /tmp/halite-reactions.log

announce_{{ node }}:
  runner.event.send:
    - args:
        tag: halite/audit/config_changed
        data:
          node: {{ node }}
          path: {{ data['path'] }}
```

The full file, with its comments, is in the example tree:

<!-- example: handbook/automation/reactor/config-changed.sls -->
```yaml
# Reaction to a watched file changing on a node.
#
# The context is `data`, `tag` and `id`. Nothing else: a reaction has no
# `salt` and no pillar. `data` is the body of the event, plus `id` (the node
# it concerns) and the envelope keys `_tag`, `_stamp` and `_correlation`.
# A filechanges event's body carries `path`, `hash`, `size` and `mode`.
{% set node = data['id'] %}

record_{{ node }}:
  local.cmd.run:
    - tgt: {{ node }}
    - arg:
      - echo "{{ data['path'] }} changed on {{ node }}" >> /tmp/halite-reactions.log

announce_{{ node }}:
  runner.event.send:
    - args:
        tag: halite/audit/config_changed
        data:
          node: {{ node }}
          path: {{ data['path'] }}
```

### What a reaction can see

Only three names:

| Name | Is |
|---|---|
| `data` | The event's body, **plus** `id` (the node the event concerns) and the envelope keys `_tag`, `_stamp` and (when set) `_correlation`. |
| `tag` | The full tag. |
| `id` | The node ID. |

There is **no `salt` dispatcher and no pillar** in a reaction. Reactions
render on the hub, and the hub's template environment is deliberately
bare: a reaction that could call arbitrary functions while rendering would
be a way to run code on the hub from an event that any node could send.
The undefined-name rule applies, too: a reference to a key the event does
not have is an error, and the reaction does not fire.

The shape of `data` depends on who sent the event. The beacon examples of
chapter 19 show the shapes: `diskusage` gives `mount` and `percent_used`,
`service` gives `service` and `running`, `filechanges` gives `path`,
`hash`, `size`, `mode`. For an event a node sent with `event send`, it is
whatever it sent. Look at a real event with `halite-hub event listen`
before you write the reaction that reads it.

### The four reaction types

The function name starts with the type:

| Type | Does | Example |
|---|---|---|
| `local` | Runs an **execution function on nodes**, as `halite-hub run` would | `local.state.apply`, `local.cmd.run`, `local.service.restart` |
| `runner` | Runs a **hub runner** | `runner.event.send`, `runner.state.orchestrate` |
| `wheel` | Runs a hub management function (the key lifecycle, for instance) | see [the command reference](../command-reference.md#reactors) |
| `caller` | Runs on the node that fired the event, from a node's own reactor | see [the command reference](../command-reference.md#reactors) |

For `local`, `tgt` selects the nodes (any target expression; add
`tgt_type: compound` and the like), `arg` is the positional arguments and
`kwarg` the named ones:

```yaml
check_{{ id }}:
  local.state.apply:
    - tgt: {{ data['id'] }}
    - kwarg:
        test: True
```

For `runner`, `args` (or `kwarg`) are the runner's parameters.

## Authorization: a reaction is somebody

In the predecessor, the reactor ran with the control plane's full
privilege, so a node able to fire the right event could cause arbitrary
fleet-wide execution. Here **every reaction runs as a principal and is
authorized by the policy exactly as a person would be.** The full form's
`principal` names it; the short form uses `reactor:default`.

Give the principal a role in `policy.yaml`:

<!-- example: handbook/automation/hub-reactor-policy.yaml -->
```yaml
# The policy a reactor needs. A reaction is authorized as its principal,
# exactly like a person, so nothing runs until a role says it may.
roles:
  watch:
    - target: '*'
      functions:
        - 'cmd.run'
    - runners: ['event.send']

bindings:
  - principal: 'reactor:watch'
    roles: ['watch']
```

The role `watch` may run `cmd.run` on any node and call the `event.send`
runner, and nothing else. Anything outside is refused and logged. Test the
policy the same way you test a person's:

```
$ halite-hub policy test reactor:nobody web1.lab cmd.run 'echo hi'
denied: reactor:nobody is bound to no role in policy.yaml
$ halite-hub policy test reactor:watch web1.lab cmd.run 'echo hi'
allowed by role "watch" rule 0
$ halite-hub policy test reactor:watch web1.lab pkg.install nginx
denied: no rule in [watch] permits pkg.install against "web1.lab"
```

Note that `cmd.run` has to be named: a wildcard never grants it
([chapter 26](26-authorization.md)). Narrow the `target` to what the
reaction touches. A reaction that restarts nginx has no business holding a
role that reaches the databases.

## Test a reactor before you trust it

`reactor.test` renders a reaction for a made-up event and prints what it
*would* dispatch, **with the authorization decision**, without dispatching
anything:

```sh
halite-hub runner reactor.test \
  tag=halite/beacon/db1.lab/filechanges/tmp/halite-watched.txt \
  'data={"id":"db1.lab","path":"/tmp/halite-watched.txt"}'
```

```
tag: halite/beacon/db1.lab/filechanges/tmp/halite-watched.txt
matched:
    - halite/beacon/*/filechanges/**
reactions:
    -
        id: record_db1.lab
        reactor: halite/beacon/*/filechanges/**
        principal: reactor:watch
        kind: local
        fun: cmd.run
        tgt: db1.lab
        arg:
            - echo "/tmp/halite-watched.txt changed on db1.lab" >> /tmp/halite-reactions.log
        authorized: allowed by role "watch" rule 0
    -
        id: announce_db1.lab
        principal: reactor:watch
        kind: runner
        fun: event.send
        kwarg:
            data: {node: db1.lab, path: /tmp/halite-watched.txt}
            tag: halite/audit/config_changed
        authorized: allowed by role "watch" rule 1
```

"It renders" and "it is permitted" are different questions, and the second
is the one that fails at three in the morning, which is why the
authorization is printed on the same page. `halite-hub runner reactor.list`
prints the configured entries.

## Watching one fire

The sequence, run end to end in the lab (a beacon on `db1.lab` watching
a file; the reactor above):

1. The file is edited. The `filechanges` beacon fires
   `halite/beacon/db1.lab/filechanges/tmp/halite-watched.txt`.
2. The reactor matches it, renders the reaction, and dispatches two things
   as `reactor:watch`:
3. a `cmd.run` job to `db1.lab`, which appends a line to a log:

   ```
   /tmp/halite-watched.txt changed on db1.lab
   ```

4. an `event.send` runner job, which puts
   `halite/audit/config_changed` on the bus, with the principal recorded:

   ```json
   {"_tag":"halite/audit/config_changed","_correlation":"20261011T190452285617",
    "data":{"_principal":"reactor:watch","node":"db1.lab","path":"/tmp/halite-watched.txt"}}
   ```

Both dispatches are ordinary jobs in the job cache
([chapter 15](15-jobs.md)), submitted by `reactor:watch`:

```
20261011T190452285617  event.send  hub       complete
20261011T190452262292  cmd.run     db1.lab   complete
```

so what a reactor did is as auditable as what a person did.

## When a reaction fails

A reaction that cannot render or dispatch never fails silently. It puts a
`halite/reactor/error` event on the bus, with the file, the position and the
reason. This was one:

```json
{"_tag":"halite/reactor/error","_node":"db1.lab","data":{"error":
 "…/config-changed.sls:11:30: data[path] is undefined (mapping has no key data);
  use `| default(...)`, `is defined`, or `.get(name, fallback)` to make it optional",
  "file":"…"}}
```

(The reaction had read `data['data']['path']`, but `data` already *is* the
body.) Make your own alert from the stream:

```sh
halite-hub event listen --tag 'halite/reactor/**'
```

## Controls: debounce, dedupe, rate limit

A busy event source will fire a reaction a great many times. The full form
has three controls, per pattern:

| Setting | Effect |
|---|---|
| `debounce: 5s` | A burst becomes **one** reaction, run when the burst has gone quiet (the latest event of the burst is the one used). |
| `dedupe_window: 30s` | The **first** event with a given key fires; further events with the same key inside the window are dropped. The key is the event's **tag** unless `dedupe_key` names a field of the event (`dedupe_key: path`), in which case it is the tag plus that field's value. |
| `rate_limit: 600/m` | A token bucket for the pattern, so one noisy source cannot starve the others. Over the limit, the event is dropped and a `halite/reactor/throttled` event says so. |

Choose between the first two deliberately. **`debounce` loses nothing**: the
reaction runs once, after the burst, with the last event. **`dedupe_window`
drops events**: if the dropped one was the only signal of a real change (an
edit made ten seconds after the last), nothing will arrive later to cover
for it. Use `dedupe_window` to suppress repeats of a *condition* a beacon
reports every interval; use `debounce` for a burst around a *change*.

And across all reactions:

| Setting | Default | Meaning |
|---|---|---|
| `reactor_workers` | 2 × CPUs | How many reactions render and dispatch at once. Events with the same correlation (or node) are processed in order; unrelated events run in parallel. |
| `reactor_queue_depth` | 10000 | How many may wait. On overflow the oldest are dropped, a `halite/reactor/overflow` event reports how many, and a counter increments. |
| `reactor_timeout` | 60s | How long one reaction may take to render and dispatch. (The job it starts has its own limit.) |
| `max_causality_depth` | 5 | How long a chain of cause and effect may grow before the hub breaks it with a `halite/reactor/loop` event. **See the next paragraph: it does not catch a reaction that re-triggers itself through `event.send`.** |

**The loop breaker, and what it does not catch.** `max_causality_depth`
counts a chain of cause and effect by the `_correlation` an event carries, and
breaks a chain longer than the limit with a `halite/reactor/loop` event. In
this build **it does not catch a reaction that re-triggers itself through
`event.send`**, because a runner call starts a new chain: the event it puts on
the bus is stamped with the runner job's own ID. This was tested, on purpose,
in the lab:

```yaml
# loop.sls: on halite/audit/loop, send halite/audit/loop
echo_again:
  runner.event.send:
    - args:
        tag: halite/audit/loop
```

One event started it. In a few seconds there were more than two hundred events
on that tag, **each with a different `_correlation`**, no `halite/reactor/loop`
event, and no end, until the hub was restarted without the entry. Adding
`dedupe_window: 30s` to the entry stopped it at **two** events: the first
fired, and every later one inside the window was dropped.

So do not rely on `max_causality_depth` alone (the ledger records the gap). The
brakes that work are:

- **Do not react to events your own reactions send.** Choose tags so that no
  reaction's output matches any reaction's pattern. A separate prefix for
  audit events (`halite/audit/…`) that no reactor listens on is the habit.
- **`dedupe_window`** on any entry that could be re-triggered: the first
  fires, the rest are dropped.
- **`rate_limit`**, so a runaway is slow rather than fast (it throttles and
  emits `halite/reactor/throttled`; it does not by itself end a loop).
- **`debounce`**, `onchangeonly` and `disable_during_state_run` on the beacon
  side, so the source is quiet.
- **Test with `reactor.test`**: if a reaction's rendered output (`tag:` of an
  `event.send`) matches a pattern in `reactor.list`, you have written a loop.

The reactor keeps its position in the event log, so a hub restart resumes
where it stopped and does not lose events that arrived while it was down.

## Patterns

**Self-healing, with a safety rail.** A `service` beacon on `nginx` fires
when it stops; a reaction restarts it. Give the principal only
`service.start` on that target, add `dedupe_window` so a flapping service
does not restart in a storm, and also have the reaction send an
`halite/audit/…` event so a person sees that it happened.

**Onboarding.** `halite/node/*/start` triggers `local.state.apply` for that
node. (Run it with `test: True` for a long time before you take the flag
off.) `startup_states` on the node does the same without the hub; the
reactor is useful when you want the *hub* to decide.

**Notification.** The hub has no outbound notification runner in this
build (`slack.post`, `smtp.send` and `http.query` are listed by `runner
list` as not built). Notification instead leaves from the return path: set
`event_return` on the hub to ship selected tags to a webhook, mail or
syslog **returner**, and your alerting sees `halite/reactor/error`,
`halite/key/**` or any `halite/audit/…` event you put on the bus
([chapter 23](23-scheduler-and-returners.md)). A reaction that has
something to say sends an event, and the returner does the telling.

**Hand-off to a person's process.** An `halite/node/*/deploy/finished`
event from a node can start an orchestration
(`runner.state.orchestrate`) that verifies and promotes
([chapter 21](21-orchestration.md)).

**Things to avoid.** Do not put policy in the pattern. Do not give the
principal `*` "to get it working": make the failing `reactor.test` tell you
what it needs. Do not react to your own audit events. Do not dispatch a
change at everything: `tgt: {{ data['id'] }}` reacts at the node that
spoke.

## Key ideas

- A **reactor** maps an event-tag glob to a **reaction SLS** in
  `hub.yaml`; a restart loads it.
- A reaction sees only `data`, `tag` and `id`; `data` is the event body
  plus `id` and the envelope keys.
- Four reaction types: `local`, `runner`, `wheel`, `caller`.
- **A reaction is authorized as its principal** (`reactor:default` unless
  you name one) by the same deny-by-default policy as a person.
- `halite-hub runner reactor.test` shows what would be dispatched **and
  whether it is allowed**.
- Failures become `halite/reactor/error` events; `debounce`,
  `dedupe_window`, `rate_limit`, a bounded queue and
  `max_causality_depth` (which, in this build, does not see a loop through `event.send`) bound the damage of a storm.

Next: [orchestration](21-orchestration.md), for steps that must happen in
order across many machines.
