# The life of a job

<!-- abstract: A walk through the source from the command `halite-hub run '*' test.ping` to the answer on the terminal: authorize, resolve, record, deliver, check, execute, return, gather. -->
<!-- kind: concept -->
<!-- index: life of a job; job flow; dispatch; submit; Policy.Authorize; resolve; Jobs.Put; subscribe; acceptJob; Guard.Admit; executeJob; postReturns; returned; completeIfDone; gather; replay cache; nonce; halite.job/1 -->

The best way to understand how the parts fit is to follow one request all the way
through. This is the path of

```sh
halite-hub run web1.lab test.ping
```

from the keyboard to the line `web1.lab: True`, with the function or file where
each step happens, so you can open the source and read along. The names are those of
this build; use `grep` if a refactor has moved one.

```
 operator                       hub                                   node
 ────────                       ───                                   ────
 cmd/halite-hub/run.go
   runRun ── POST /v1/jobs ───▶ internal/hub/jobs.go  submit
                                  Policy.Authorize         (deny by default)
                                internal/hub/dispatch.go  Dispatch
                                  resolve (target.Matcher) (who matches?)
                                  Jobs.Put                 (record BEFORE delivery)
                                  emit halite/job/<jid>/new
                                  write job to each stream ──────────▶ cmd/halite-node
                                                                        acceptJob
                                                                          Guard.Admit  (replay, nonce, expiry)
                                                                          signature check, queue
                                                                        executeJob
                                                                          exec.Registry.Call
                                                                        postReturns
                                internal/hub/jobs.go  returned ◀──────  POST /v1/return
                                  Jobs.AddReturn, emit ret/<node>
                                  completeIfDone
   gather ◀── returns ──────────
   exit status
```

## 1. The operator's side: `cmd/halite-hub/run.go`

`runRun` parses the command line (`resolveTarget` turns `-G 'os:Ubuntu'` or a bare
glob into a target kind and expression, and separates the function and its
arguments), builds an `operatorClient` from the operator certificate
(`--as`, or the default operator found in the `pki_dir`), and, if
`--sign-key` or `--sign-extension` was given, signs the job
(`prepareSigning`, `signJob`) over a canonical byte string so that the node can
check it without trusting the hub. It then submits a `transport.SubmitRequest`
with `POST /v1/jobs` over the mutual-TLS client in `internal/transport`, and
calls `gather` to wait for returns.

Note what is *not* here: the client does not know which nodes will answer. It asks
the hub, which decides.

## 2. Authorization: `Server.submit`

The hub's HTTP handler for job submission is `submit` in `internal/hub/jobs.go`.
The first thing it does after parsing is ask the policy:

```go
decision := s.Policy.Authorize(policy.Request{ ... Principal, Target, Fun, Arg, Kwarg ... })
```

`internal/policy` is the whole of the access-control logic: roles of rules,
bindings, and the rule that a wildcard never grants a function whose signature
says `arbitrary_code`. A denial logs `job refused by policy` with the reason and
returns `403`; an allow logs `job authorized … role=… rule=…`, which is the audit
line [chapter 26](26-authorization.md) shows. The principal comes from the
**certificate on the connection**, never from the request body.

## 3. Resolving the target: `Server.Dispatch` and `resolve`

`Dispatch` (`internal/hub/dispatch.go`) is where a submission becomes a job. It
compiles the target (`target.Matcher`; the grammar of
[chapter 13](13-targeting.md), including nodegroups and a pillar-aware
evaluation for `-I`/`-J`) and calls `resolve`, which considers **only accepted
nodes** — a pending or rejected request is not part of the estate and a revoked one
is deliberately out of it — and evaluates the expression against the last grains
the hub holds for each. The answer is the expected-respondents list that goes into
the job record.

It assigns the JID (`identify`, using the hub's monotonic `job.Clock`), a
random nonce (`job.Nonce`) and an absolute expiry, with the 15-minute default or the
one-hour default for `--offline queue`, because a queued job waits for a machine
that is off.

## 4. Record first, then deliver

```go
if err := s.Jobs.Put(j); err != nil { ... }
s.emitCorrelated(tagJobNew(string(j.JID)), "", j.Correlation, map[string]any{ ... })
```

`Jobs` is `job.Cache` (`internal/job/cache.go`), the append-only job store with a
schema marker (`halite.job/1`) so that an older hub refuses to rewrite a record a
newer one wrote. The job is **written to disk before any node is sent it**: that
ordering is why a caller who disconnects loses nothing, why a missing return is
detectable, and why `jobs show` works for a job that never reached a node.
`halite/job/<jid>/new` goes onto the event bus (`internal/eventbus`).

Then delivery. A batched job is handed to a goroutine owned by the hub
(`runBatches`), so closing the operator's terminal does not abandon it; an
`--offline queue` job is spooled for the nodes that are not connected; and for
the connected nodes the job is written as a line of NDJSON to each node's open
**subscribe stream** (`internal/hub/subscribe.go`, `subscribe`), through the same
mutual-TLS HTTP/2 connection the node dialled.

## 5. The node receives it: `cmd/halite-node/enroll.go`

The connected agent reads the stream. For a `job` message it calls `acceptJob`,
which builds a `job.Job` from the message and offers it to the executor:

```go
if err := n.executor.Offer(j); err != nil { n.refuse(j, err); return }
```

Before anything runs, `executor` (`cmd/halite-node/execute.go`) calls
`guard.Admit(j)` (`internal/job/guard.go`): has this JID been run here? has this
nonce been seen? is the expiry in the past? A job that fails any is **refused with
a structured return**, not dropped, and written to the node's evidence chain
(`refuse` → `recordJobRefused`). If the node requires signatures
(`require_job_signature`), `internal/jobsign` verifies the signature against the
node's trusted keys *and* that this node matches the signed target. A full queue
refuses out loud rather than growing memory.

## 6. Execution: `executeJob`

`executeJob` runs the job against a **copy** of the node's state (the first
version mutated the agent, and a job carrying `test: true` made every later job on
that node a dry run). It dispatches by function name through `internal/exec`'s
registry: `exec.Registry.Call` binds the arguments against the function's
`signature`, type-checks them, and invokes the Go function in `internal/builtin`.
`test.ping` is the simplest of them. A panicking module is recovered into a failed
return, so one bad job cannot stop a node answering. A span for the job is started
under the trace context the hub sent (`traceparent`) when tracing is on.

## 7. Returns: `postReturns` and `Server.returned`

The finished `job.Return` (`halite.ret/1`) goes back through `postReturns`,
which retries a transient failure up to five times, because a return lost to a
blinked connection is a job that looks unresponsive forever. It is a separate
`POST /v1/return`, idempotent by `(jid, node, chunk)`.

On the hub, `returned` (`internal/hub/jobs.go`) validates the node's identity from
its certificate, files the return (`Jobs.AddReturn`), emits
`halite/job/<jid>/ret/<node>` (and `halite/state/<jid>/<node>/ok|failed` for a state
run), and calls `completeIfDone`, which marks the job `complete` when every expected
respondent has answered.

## 8. Gathering: back to `gather`

The operator's `gather` asks the hub for the job's status (`JobStatus`) until it is
complete or the `--timeout` passes, prints each return (`printReturn`), and
computes the exit status: `0` all succeeded, `1` one failed, `3` one never answered.

## What to notice

- **Three trust boundaries, each enforced where it matters.** The hub authorizes
  (policy); the node refuses (guard, signature); the transport authenticates
  both (TLS). No step trusts what the previous one *said* about identity.
- **The order is the design.** Authorize, then record, then deliver; check, then
  execute, then return. Each ordering answers a failure someone had: a lost
  terminal, a replayed message, a hub that was taken over.
- **The record is shared, not duplicated.** The hub's job cache and the node's
  evidence chain are independent records of the same event, so when they
  disagree something is wrong.
- **Tests sit on every seam.** The chaos layer (`internal/chaos`) registers the
  failure scenarios of this path (a hub restart mid-job, a network partition,
  disk full, clock skew, a certificate expiring mid-run, an extension that
  hangs, the event bus at its retention limit, and a reactor queue overflow),
  each with its defined behaviour and a stated limit, and `internal/hub`'s tests drive them with real transports.

## Key ideas

- `run` → `submit` (authorize) → `Dispatch` (resolve, **record**, deliver) →
  node `acceptJob` (**guard**, signature) → `executeJob` (registry call) →
  `postReturns` → hub `returned` → `completeIfDone` → `gather`.
- Identity always comes from the certificate; records are written before
  delivery; the node refuses with a reason rather than dropping.

Next: [the life of a highstate](52-life-of-a-highstate.md).
