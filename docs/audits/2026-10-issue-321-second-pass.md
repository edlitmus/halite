# A second pass over what the issue 321 audit did not read

The first audit's section 8 listed files it never read or only skimmed
(`docs/audits/2026-10-issue-321-triage.md` records the findings it did make).
This file records a pass over some of them on 2026-10-31, at `main` `b7e9496a`
and after. It is a record of what was read and what came of it, not a ledger:
each fix has its own `DIVERGENCE.md` section. It is **not** a claim that the
rest of the list is clean, and the last section says what was not read.

## What was read, and how

Read in full, as code, looking for two paths that should agree and for a
stated property nobody had shown:

| File | Lines | Found |
|---|---|---|
| `internal/hub/reactor.go` | 649 | three defects, below |
| `internal/hub/reactor_config.go` | 256 | nothing that was run |
| `internal/hub/runners_reactor.go`, and `runReactions` in `reaction.go` | 141 + 60 | one defect |
| `internal/hub/batch.go` | 328 | a SPEC/code disagreement and an unstated behaviour |
| `internal/hub/orch_steps.go` | 451 | nothing that was run; one note |

Nothing here was read against a real hub and node: every demonstration below is
a Go test on the hub's own test server with stub nodes, plus one measurement of
a single function. The reactor and batch tests were also run on FreeBSD 15.1 in a jail on `beastie` (5.499): 45 of 46 pass, and the one failure needs the source tree the jail lacks. A jail shares the host's kernel and its nodes are stubs, so that is the Go code on FreeBSD's runtime, not a node on FreeBSD.

## Findings

| What | Shown how | Status |
|---|---|---|
| The reactor swept every causality chain it held on every event: 74 us per event at 10,000 chains, 813 us at 80,000, against the 200 us SPEC 30's 5,000 events/s leaves | measured, `overLength` alone; the fix's test counts sweeps | fixed, 5.494 (#534) |
| A reactor restart lost events it had accepted but not finished (debouncing, queued, mid-reaction), against SPEC 17.2's "lossless" | a failing test: the event was lost after a restart | fixed, 5.495 (#538); restart is now **at least once** |
| Two reactor entries with the same tag glob shared a rate bucket, dedupe window and debounce timer, so the second's reaction was dropped as a duplicate | a failing test, dedupe and debounce variants | fixed, 5.497 (#536) |
| `reactor.test` judged a `caller.` reaction against a placeholder, and said "denied" about one the reactor dispatched | a failing test pairing the plan and the reactor | fixed, 5.498 (#537) |
| SPEC 9.3 said the safe limit aborts "if more than N nodes fail"; the code and chapter 14 say once N have | read; SPEC corrected | fixed, 5.496 (#535) |
| A node that takes a batched job and never answers is not a failure and does not trip the safe limit; the batch moves on after the timeout | a test of today's behaviour | **recorded, not changed**: the owner's decision, 5.496 |

## Read, not shown

These came out of reading and no test or run backs them. They are leads.

- **The default per-slice batch timeout is the job's whole lifetime** (15 minutes
  unless `--ttl` is given), taken once and waited again for each slice, and the
  slices after the first expiry are delivered to a job nodes should refuse. A
  silent node in a slice would then hold a rollout for 15 minutes a slice.
  Whether the nodes refuse as expected through a batch was not run (5.496).
- **A finished batch with a return outstanding stays `batching`** until `Settle`
  closes it at expiry (seen in the hub's test, not on a real hub).
- **`salt.wait_for_event` listens from "latest" when the step starts**, so an
  event emitted between the previous step finishing and this one reading is
  missed and the step waits out its timeout. Salt's `wait_for_event` has the
  same shape; whether it matters here was not tried.
- **`reactor.test` for `local.` and runner reactions** asks what the reactor
  asks, by reading both; no test pairs them.
- **Negative durations in a reactor entry** (`debounce: -5s`) are accepted and
  mean "no debounce", quietly. Not run.
- **The reactor's chain map** still holds about ten minutes of events, so at
  5,000 events/s it holds millions of entries; its memory was not measured.
- **The throughput target itself** (5,000 events/s sustained) was not run end to
  end; there is no harness for it in the tree.

## What was not read

From the first audit's list, **still unread or only skimmed**: the rest of
`internal/hub` (`queue`, `inflight`, `orch`, `orchstore`, `runners_jobs`,
`runners_orch`, `runners_mine`, `metrics`, the anchor store internals);
`cmd/halite-hub` apart from a search of `ssh.go` for host-key handling (it runs
the system `ssh`, with `BatchMode=yes`, and its trust tests were not read);
every file under `cmd/halite-node` on the list; `internal/target` matchers other
than the compound parser; `internal/awsauth`; `internal/winsec`, `dbus`,
`varlink`, `tracing`, `log`, `metrics`, `doctor`, `nodeevidence` and `fips`;
the `*_windows.go` and `*_other.go` files; the template filters beyond the
differential corpus; and the parsers the first audit read but did not run
against real tool output. The owner's decisions (aws-secrets defaults, LDAP case
folding, `ps.pkill` and sshd, firewall lockout) were not reopened.

## Round two: the rest of `internal/hub` that was not read

On 2026-10-31, at `main` `7f35a3c9` and after. Read in full: `queue.go`, `inflight.go`, `runners_jobs.go`, `runners_mine.go`, `runners_orch.go` and `orch.go`. Same method and same limits as above: Go tests on the hub's test server with stub nodes, FreeBSD in a jail (5.505), no real node.

| What | Shown how | Status |
|---|---|---|
| A queued job was looked for among the 200 most recent jobs, so a hub running a job every ten seconds lost it inside the queue's default hour | a failing test: 230 other jobs, then the node connects | fixed, 5.500 (#542) |
| The target glob was `path.Match`: `run 'web[!1]*'` reached `web1` and skipped `web2`; a grain `*` stopped at `/` | measured side by side with fnmatch, then a failing test | fixed, 5.501 (#543), **a behaviour change** |
| `tolerate_failures` used `path.Match`, so `['web[!1]*']` forgave `web1` alone | a failing test | fixed, 5.502 (#544) |
| `jobs.list_jobs limit=N` cut the whole estate's jobs before the caller's scope, so a scoped caller saw none of its own | a failing test | fixed, 5.503 (#545) |
| `state.orch_resume` returned a run `state.orch_show` hid (the carried-forward step comments); `orch_list` had the `limit` defect too | a failing test: alice resumed ed's run | fixed, 5.504 (#546) |

**Read, not shown (round two).**
- Other `path.Match` callers on a glob a person writes, none measured: the state allowlist (`internal/state/compile.go`), `logrotate`'s patterns, the grain-key match in `introspect.go`, `halite-node`'s environment patterns, and `halite-ext-aws-secrets`' secret patterns (5.501).
- `inflight.wait` records one idle channel: two concurrent waiters would leave the first never woken. Only `Serve` waits today.
- `OrchStore.List`'s comment says an unreadable record is "not silently dropped"; the code drops it silently.
- `jobs.active`'s `limit` is documented as "recent jobs to consider" and is a window by its own words; left alone.
- SPEC 9.5 reads "a queued job that expires produces an event"; the event is emitted only when the node connects after the expiry, so a node that never returns leaves none.
- `mine.get` for an operator ignores a publisher's `allow_tgt` (the code says an operator has already been through the policy); a decision, not a defect found.
- `salt.wait_for_event` listens from "latest" when the step starts, so an event emitted between the previous step and this one is missed.

**Still unread in `internal/hub`:** `orchstore.go`, `metrics.go`, the anchor store internals (`anchors.go`), `events.go`, `subscribe.go`, `fleet.go`, `pillar.go`, `files.go`, `relay.go`, and the parts of `server.go`, `dispatch.go`, `runner.go`, `runners_fleet.go` and `runners_tree.go` that were not needed to follow a finding. Everything under `cmd/halite-node` and the other packages in the first pass's "not read" list is as it was.

## Round three: the rest of `internal/hub`

On 2026-10-31, at `main` `fc79b8cc` and after. Read in full: `subscribe.go`, `events.go`, `relay.go`, `fleet.go`, `orchstore.go`, `pillar.go`, `files.go`, `anchors.go`, `metrics.go`, `runner.go`, `runners_fleet.go` and `runners_tree.go`. Read in part: `dispatch.go` (the whole of `Dispatch`, `resolve`, `targetPillar`, `identify`) and `server.go` from line 150 (the handler, authentication, `enroll`, `renew`; the first 150 lines are fields). Same method and limits as the rounds before: Go tests on the hub's test server with stub nodes, no real node, and no FreeBSD run for this round (see below).

| What | Shown how | Status |
|---|---|---|
| A relay that reconnected while the hub held its old stream open lost every route behind it, because the old handler's exit dropped them by relay name; the same exit published a stop event for a connected node | a real HTTP server and the real handler, opened twice: route gone, node not connected, stop event, all three failing on the old code | fixed, 5.506 (#548) |
| The last node of an estate leaving reported one connected in `halite/presence/change` | a failing test, then the detach moved back to break it | fixed, 5.506 (#548) |
| `POST /v1/relay` ignored `accept_relays` | the update answered 200 and routed a node on a hub that takes no relays | fixed, 5.507 (#549) |
| A scanner could fill the hub's `route` label with its own paths, after which `/v1/health` itself was counted as `__overflow__` | 612 unknown paths, then a real request | fixed, 5.508 (#550) |
| `--subset` was applied after the offline policy counted the nodes it left out: `queue` spooled the job for the rest, `require` refused for them | six nodes subset to two: `Nodes` 2, `Queued` 6; refused 40 of 40 draws | fixed, 5.509 (#551) |
| The same route-label defect in the API (`routeOf`) | 612 unknown paths with no token | fixed, 5.510 (#554) |
| `key.accept`, `key.reject`, `key.revoke` and `key.delete` published no event, and `key.revoke` left the node connected for the reconcile interval (`Server.Revoke`, which does both, had no caller); the hub had no test of these four runners | a connected node revoked through `CallRunner`; the bus held nothing | fixed, 5.511 (#552) |
| `event.send` skipped what `emit` does: the published count, the forward to a relay's upstream, the `salt/...` spelling | the forward never arrived; each of the three removed in turn fails its own assertion | fixed, 5.512 (#553) |

**Read, not shown (round three).**
- `POST /v1/relay` does not require the relay to have a stream open, deliberately: the relay announces concurrently with subscribing, so an early update is legitimate. An update arriving after the stream has gone leaves routes `targetableNodes()` lists and `Connected()` hides (5.507).
- The hook route in the API, `/v1/hook/{path...}`, still keeps its tail in the `route` label, and a hook request is unauthenticated (5.510).
- `halite-hub keys accept|reject|revoke|delete` run at a terminal is another process and publishes no `halite/key/...` event; whether the reconcile loop should publish what it sees changed on disk is a decision (5.511).
- `event.replay` takes its `limit` before the caller's scope filter, so a scoped caller can be handed an empty page that carries a `next` offset; it pages on correctly, and a caller that takes an empty page for the end stops early (5.512).
- `identify` checks that a signed job's identifier is unused and the job is stored later, so two concurrent submissions of the same signed submission can both pass; the node's nonce guard (SPEC 6.3) is what stops the second running there. Neither was run.
- A runner job is recorded `dispatched` with no expiry (`recordRunnerJob`), so one that was in flight when the hub stopped is listed as running for ever; `Settle` only closes a job that has an expiry. Not run.
- A runner that panics is recovered by `net/http` for the request, and leaves the same dispatched record and no `halite/run/.../ret` event. Not run.
- `file` in `files.go` hashes the file separately from opening it, so a file replaced between the two is served with the old digest; the node refuses a body whose digest differs, so it fails safe and retries. Not run.
- `OrchStore.List` still drops an unreadable record silently, against its own comment (round two, not changed).
- `halite_hub_requests_total`'s route label is now bounded for unknown paths; the `fun` label of `jobsDispatched` and `jobDuration` is whatever function name a job carries, bounded by the registry's cap and not by validation here. Not checked whether every submitted function is validated against a registry first.

**Not verified in round three.** No real `halite-node`, no real relay across a network, no Prometheus reading the metrics, `-race` on the hub package only for the relay change, no FreeBSD jail run for these fixes (round two's jail run, 5.505, covers earlier ones), and `make check`.

**Still unread (as of round three; round four read all but the declarations).** In `internal/hub`, no round had read these in full: `enrolllimit.go`, `eventreturn.go`, `grains.go`, `jobs.go` (the submit, status, resume and kill handlers; only searched), `listen.go`, `metrics_endpoint.go`, `mine.go`, `nodes.go` (the node cache; only searched), `runners_pending.go`, `servingcert.go`, `reaction.go` outside `runReactions`, and the first 60 lines of `dispatch.go` and 150 of `server.go` (type declarations and fields). Everything under `cmd/halite-node` and the other packages in the first pass's "not read" list is as it was.

## Round four: the last of `internal/hub`

On 2026-11-01, at `main` `5b6ff546`. Read in full: `jobs.go`, `mine.go`, `nodes.go`, `enrolllimit.go`, `eventreturn.go`, `grains.go`, `listen.go`, `metrics_endpoint.go`, `runners_pending.go`, `servingcert.go` and `reaction.go`. Every file in the package has now been read, in the first pass's table or in a round after it, except the type declarations and fields at the top of `dispatch.go` (60 lines) and `server.go` (150), which a finding never needed. Same method and limits as before: Go tests on the hub's test server with stub nodes, no real node, no FreeBSD run for this round.

| What | Shown how | Status |
|---|---|---|
| `GET /v1/jobs/{jid}` checked only the operator certificate, where `submit`, `resume` and `kill` authorize: an operator bound to `web*`, refused a submit against `db1`, read a `db1` job's record and returns | the administrator's job read by that operator, with a failing control for the submit; the new test fails twice on the old code (node job, runner call) | fixed, 5.513 (#556) |
| `EventReturn.Run` turned any read error into a jump to the end of the bus with no pause and no look at the context, and resent the first part of a half-sent batch | a bus that had turned over with a pruned offset (nothing shipped); segments made unreadable (`Run` did not stop); two of four events sent (no offset recorded) | fixed, 5.514 (#558) |
| `kill` aborted a completed job and told its nodes; a late return completed a killed job | both states read wrong on the old code, each in its own test | fixed, 5.515 (#559) |

**Read, not shown (round four).**
- ~~`EventReturn.Run`: a read error resets the offset to `Latest` and loops with no wait.~~ Shown and fixed, 5.514 (#558); the loop did not stop with its context either.
- ~~`EventReturn.Run`: a half-sent batch is resent whole.~~ Fixed, 5.514 (#558): the offset is recorded at the last event that arrived.
- `halite_events_dropped_total{reason="returner_failed"}` is incremented on every failed attempt, but the event is retried, not dropped.
- ~~`kill` sets `Aborted` on any job.~~ Shown and fixed, 5.515 (#559), with the mirror case it led to (`completeIfDone` overwriting `Aborted`). `kill`'s runner-job authorization is still the node form against `hub` (5.513 did not change it). Not run.
- `returned` publishes `halite/state/<jid>/<node>/<result>` when `ret.Out == "highstate"`, a field the node supplies and the hub does not check against `ret.Fun`. A node the job was sent to could fire a state-result reactor from a `test.ping` return.
- `JobStatus.Delivered` is an attempt, as `job.Job` says, and listed a never-connected node in the demonstration for 5.513; the field name reads as arrival.
- `MineStore.Update` is read-modify-write with no lock, so two concurrent publishes from one node can lose one. A node's mine data is removed only by the `mine.delete` runner; no key runner clears it.
- A `caller` reaction that sets `tgt` has it silently ignored and runs on the event's node; a non-scalar `arg` is flattened through `KeyString`.
- `grainsJSON` in `cmd/halite-node` returns nil when encoding fails, and a subscribe that carries a version and no grains overwrites the hub's cached grains with none. Needs an encode failure.
- Not checked for the same gap as 5.513: the `GET /v1/jobs` list, which goes through the `jobs.*` runners.

**Not verified in round four.** No real node, relay or operator policy file, no FreeBSD jail run for 5.513, and `make check`.
