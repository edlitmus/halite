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
- ~~An update arriving after the stream has gone leaves routes.~~ Shown and fixed, 5.533 (#580): held for the stream for 30 seconds (chosen, not measured), then dropped. An update before the stream is still legitimate and still works.
- ~~The hook route in the API, `/v1/hook/{path...}`, still keeps its tail in the `route` label.~~ Shown and fixed, 5.529 (#576): 612 POSTs to distinct paths made 612 labels. A hook request is still unauthenticated, as it is meant to be.
- ~~`halite-hub keys accept|reject|revoke|delete` run at a terminal publishes no event.~~ Decided and fixed, 5.536: the reconcile loop announces a change it sees in the store, tagged `via: terminal` and with no principal, because the store does not record who ran the command.
- ~~`event.replay` takes its `limit` before the caller's scope filter.~~ Shown and fixed, 5.525 (#573): a `web*` caller behind ten `db1` events was handed none of its three. A scan cap of 10,000 events per call is chosen, not measured.
- ~~`identify` checks that a signed job's identifier is unused and the job is stored later.~~ Shown (with the interleaving forced by a seam) and fixed, 5.531 (#578): two of two were accepted. How often a real hub loses the race is not known.
- ~~A runner job is recorded `dispatched` with no expiry.~~ Fixed, 5.528 (#575): the hub closes the ones a previous run left, once, at startup. The test writes the orphan; no hub was killed mid-call.
- ~~A runner that panics leaves a dispatched record and no `ret` event.~~ Shown and fixed, 5.527 (#575): it is a failed run now, with the stack in the log. A panic in a goroutine a runner starts itself is not covered.
- ~~`file` in `files.go` hashes the file separately from opening it.~~ Shown and fixed, 5.530 (#577), and the wrong way round from what this said: the old *body* went out under the *new* digest. A rewrite in place is still inconsistent and still refused by the node.
- ~~`OrchStore.List` still drops an unreadable record silently.~~ Fixed, 5.526 (#574): `orch list` shows it as `unreadable`, to a caller that sees every run.
- ~~The `fun` label of `jobsDispatched` and `jobDuration`.~~ Checked, and worse than the lead said: `jobDuration` took it from the node's return. Fixed, 5.532 (#579): the hub's record of the job, and `other` for a name that is not a dotted identifier. No registry validates a submitted function on the hub (the modules are the node's), so how many distinct valid names an operator allowed `*` can submit is unchanged.

**Closed afterwards (2026-11-01, #573 to #580):** every lead above except the terminal `keys` event, which is the owner's decision and was not touched. Not verified for any of them: `make check`, a real fleet or relay across a network, and a FreeBSD jail run.

**Not verified in round three.** No real `halite-node`, no real relay across a network, no Prometheus reading the metrics, `-race` on the hub package only for the relay change, no FreeBSD jail run for these fixes (round two's jail run, 5.505, covers earlier ones), and `make check`.

**Still unread (as of round three; round four read all but the declarations).** In `internal/hub`, no round had read these in full: `enrolllimit.go`, `eventreturn.go`, `grains.go`, `jobs.go` (the submit, status, resume and kill handlers; only searched), `listen.go`, `metrics_endpoint.go`, `mine.go`, `nodes.go` (the node cache; only searched), `runners_pending.go`, `servingcert.go`, `reaction.go` outside `runReactions`, and the first 60 lines of `dispatch.go` and 150 of `server.go` (type declarations and fields). Everything under `cmd/halite-node` and the other packages in the first pass's "not read" list is as it was.

## Round four: the last of `internal/hub`

On 2026-11-01, at `main` `5b6ff546`. Read in full: `jobs.go`, `mine.go`, `nodes.go`, `enrolllimit.go`, `eventreturn.go`, `grains.go`, `listen.go`, `metrics_endpoint.go`, `runners_pending.go`, `servingcert.go` and `reaction.go`. Every file in the package has now been read, in the first pass's table or in a round after it, except the type declarations and fields at the top of `dispatch.go` (60 lines) and `server.go` (150), which a finding never needed. Same method and limits as before: Go tests on the hub's test server with stub nodes, no real node, no FreeBSD run for this round.

| What | Shown how | Status |
|---|---|---|
| `GET /v1/jobs/{jid}` checked only the operator certificate, where `submit`, `resume` and `kill` authorize: an operator bound to `web*`, refused a submit against `db1`, read a `db1` job's record and returns | the administrator's job read by that operator, with a failing control for the submit; the new test fails twice on the old code (node job, runner call) | fixed, 5.513 (#556) |
| `EventReturn.Run` turned any read error into a jump to the end of the bus with no pause and no look at the context, and resent the first part of a half-sent batch | a bus that had turned over with a pruned offset (nothing shipped); segments made unreadable (`Run` did not stop); two of four events sent (no offset recorded) | fixed, 5.514 (#558) |
| `kill` aborted a completed job and told its nodes; a late return completed a killed job | both states read wrong on the old code, each in its own test | fixed, 5.515 (#559) |
| `kill` judged a runner call as a node job | the runner holder refused with the old form, allowed with the shared one | fixed, 5.516 (#561) |
| A state-result event for a `test.ping` or a `show_*` job | published for both on the old gate, and again with the gate widened | fixed, 5.517 (#562) |
| A returner outage counted as the bus dropping events | the old counting left `reason="returner_failed"` and no returner counter | fixed, 5.518 (#564) |
| The reactor answered every bus read error as a corrupt offset | `Run` did not stop when its context ended | fixed, 5.519 (#565) |
| Concurrent mine publications and deletes erased each other | `-race`, repeated; with the lock commented out functions were lost and deleted ones came back | fixed, 5.520 (#566) |
| `key.delete` left the node's mine served and its grains cached | both still there after the runner and after the reconcile loop | fixed, 5.521 (#567) |
| A reaction with a target it ignored, or an arg it garbled, was accepted | five documents accepted by the old parser | fixed, 5.522 (#568) |
| A reconnect that reported no grains erased the cached ones | grains `null` and their age reset on the old code | fixed, 5.523 (#569) |
| `delivered` documented as arrival, recorded as an attempt | not shown by a failing test (prose); a test pins the behaviour | corrected, 5.524 (#570) |

**Read, not shown (round four).**
- ~~`EventReturn.Run`: a read error resets the offset to `Latest` and loops with no wait.~~ Shown and fixed, 5.514 (#558); the loop did not stop with its context either.
- ~~`EventReturn.Run`: a half-sent batch is resent whole.~~ Fixed, 5.514 (#558): the offset is recorded at the last event that arrived.
- ~~`halite_events_dropped_total{reason="returner_failed"}` counts retries as drops.~~ Fixed, 5.518 (#564): the returner has its own counter, alert and dashboard series.
- ~~`kill` sets `Aborted` on any job.~~ Shown and fixed, 5.515 (#559), with the mirror case it led to (`completeIfDone` overwriting `Aborted`). ~~Its runner-job authorization was the node form against `hub`.~~ Fixed, 5.516 (#561); the 5.515 change also made a test racy, fixed in #563.
- ~~`returned` publishes the state-result tag on the node's `Out`.~~ Fixed, 5.517 (#562): the recorded job function decides.
- ~~`JobStatus.Delivered` reads as arrival.~~ It records an attempt; the documentation said otherwise and now says so, 5.524 (#570). The field's name is unchanged.
- ~~`MineStore.Update` is an unlocked read-modify-write.~~ Shown and fixed, 5.520 (#566). ~~A deleted node's mine was never cleared.~~ Fixed, 5.521 (#567), for the runner and for a delete seen by the reconcile loop; a revoked node's data is kept.
- ~~A `caller` reaction's `tgt` is ignored; a non-scalar `arg` is flattened.~~ Both refused when parsed, 5.522 (#568). Whether structured args should be JSON-encoded instead was answered, 5.535: no, the binder does not parse a string into a mapping, so refusing at parse time is the earlier refusal. A test pins it.
- ~~A subscribe with a version and no grains overwrites the cached grains.~~ Shown and fixed on the hub side, 5.523 (#569). `grainsJSON` returned nothing on an encode failure rather than saying so. Fixed, 5.534: a grain that cannot be encoded is left out and named, and the rest are sent. No real collector is known to produce one; the test builds it.
- Checked, no gap: the API's `GET /v1/jobs` and `/v1/jobs/{jid}` go through the scoped `jobs.list_jobs` and `jobs.list_job` runners (5.516's note). Not compared: whether the runner scope and 5.513's function rule agree for every policy.

**Not verified in round four.** No real node, relay or operator policy file, no FreeBSD jail run for 5.513, and `make check`.

## Round five: the first of the packages and commands no round had opened

On 2026-11-02, at `main` `2dbc28a4`. This round read, in full unless noted: `internal/awsauth` (all four files), `internal/varlink`, `internal/fips`, `internal/log` (`FromConfig`, and `log.go` as far as the redaction loop in `Log`), `internal/metrics` (`metrics.go`, `expose.go`, `merge.go`), `internal/target` (`target.go`, `compound.go`), `internal/tracing` (`context.go`, `carrier.go`), and in `cmd`: `halite-hub/ssh.go`, `keys.go`, `policy.go`, `extsign.go`, `extensionverify.go`, `serve.go` lines 600 to 780, and `halite-node/jobsignature.go` and `writeInlineFile` in `oneshot.go`. Nothing was fixed in this round: it records what was found. Each lead below says whether it was run.

### Shown (a scratch test, run on this commit and then removed)

| What | Shown how | Status |
|---|---|---|
| A grain or pillar value that contains a colon can never be targeted: `splitKeyValue` splits at the *last* colon, so `G@hwaddr_interfaces:eth0:aa:bb:cc:dd:ee:ff` looks for the key `hwaddr_interfaces:eth0:aa:bb:cc:dd:ee` and the value `ff`. A MAC address, an IPv6 address and any `host:port` value are out of reach of `-G`, `-P` and the compound `G@`. | five forms against a node holding that grain, all false | fixed, 5.537 (#588). Salt tries every split point (`salt.utils.data.subdict_match`); that is from memory of its source, and Salt is not installed here, so the differential was not run |
| `halite-hub ssh` sends the state tree as `map[string]string` inside JSON, so a file that is not valid UTF-8 (an image, an archive, a DER certificate) reaches the target with each invalid byte replaced by U+FFFD, and `file.managed` from it writes the corrupted bytes. Nothing refuses or warns. | `sshRequest` for `state.apply` over a tree holding the bytes `89 50 4e 47 ff fe 00 80`; the request decodes to `ef bf bd 50 4e 47 ef bf bd ef bf bd 00 ef bf bd` | fixed, 5.538 (#589) |
| `halite-hub ssh --timeout 30` (no unit) is silently ignored and the run takes the five-minute default; `halite-hub run --timeout 30` refuses it. | `sshTimeout` with `30` returned 5m0s; with `-5s` it returned `-5s`, which `sshexec` then replaces with its own default | fixed, 5.539 (#590) |
| `checkRootsAreNotTheHubsOwn` compares paths with `filepath.Abs`, which does not follow a symlink, so a `file_roots` entry that is a link to `state_dir` is not refused, and the hub serves its key store and job cache to every node, which is what the check exists to stop. | the state directory itself is refused; a symlink to it returns nil | fixed, 5.540 (#591) |
| `tracing.ParseTraceParent` accepts an uppercase trace id and a version that is not hexadecimal (`zz-...`). The W3C format is lowercase hexadecimal (from memory of the specification, which was not consulted). | the two headers parsed as valid | fixed, 5.541 (#592) |

### Read, not run

- `awsauth`: `AWS_CONTAINER_CREDENTIALS_FULL_URI` is fetched with no `Authorization` header, and nothing in the tree reads `AWS_CONTAINER_AUTHORIZATION_TOKEN` or its `_FILE` form (grep: no match anywhere), so EKS Pod Identity cannot be reached by this path. Not run against one.
- `awsauth`: `IMDS.Get` takes a 401 as an error and does not take a new token, so a metadata service that restarts costs up to the token's remaining life (at most five minutes). With `partition: aws-cn` and no region the STS endpoint is built for `us-east-1`, which has no `.com.cn` host.
- `awsauth`: `canonicalQuery` sorts by the raw name where SigV4 sorts by the encoded one, which differ for a character above `~` and for a repeated key with such values. No caller sends one (the S3 listing's keys are fixed names), so it is latent.
- `varlink.Call` sets a deadline from the context but does not stop when the context is cancelled without one, so a cancel waits out the 30 seconds.
- `metrics`: `Counter.Add(NaN)` passes the `n < 0` test and `Histogram.Observe(NaN)` makes `_sum` NaN for good; the series key joins label values with NUL, so values containing one can share a series. A histogram scrape is not atomic across its buckets and its count. No caller is known to pass a NaN or a NUL.
- `keys operator create --lifetime` is not checked: a negative or zero lifetime writes a certificate that is already expired, and there is no upper bound.
- `policy test --kwarg` drops a pair with no `=` without a word, and splits on every comma, so a typo in the flag gives a decision about a different request. `deny_kwargs` and `require_kwargs` are the rules it matters for.
- `writeInlineFile` writes every file at 0600 and refuses a file whose name starts with `..` (`..data`), which is not a path out of the tree.
- `target.compileSubnet` compares an unzoned, unmapped address: a `-S` address written as `::ffff:10.0.0.1`, or an `ipv6` grain with a zone, does not match as one might expect. Not run.

### Checked, no gap

`jobsign` checks in `jobsignature.go`: the function classes, the target match, the pillar-failure refusal and the expiry bound agree with what their comments say, and `SigningPayload` covers the JID, target and its kind, function, arguments, keyword arguments, environment and expiry. Not checked: whether `Batch`, `Offline`, `Nodes` and `Test` outside the keyword arguments can change what a signed job does on a node. `awsauth.ServiceURL` refuses the region characters it says it does. `Merge` keeps one declaration per family.

### Still unread

By line count this round read about a fifth of what the earlier rounds listed. **Not read:** `internal/winsec`, `internal/dbus`, `internal/doctor`, `internal/nodeevidence`, the rest of `internal/tracing` (`otlp.go`, `tracer.go`, `span.go`, `settings.go`), the rest of `log.go`, and the `*_windows.go` and `*_other.go` files. In `cmd/halite-hub` (`run.go` was read in round seven): `main.go`, `doctor.go`, `orch.go`, `jobs.go`, `relay.go`, `runner.go`, `gitfs.go`, `s3fs.go`, `extpillar.go`, `extensions.go`, `extensioncmd.go`, `extensionrun.go`, `evidence.go`, and the rest of `serve.go`. In `cmd/halite-node`: everything except the two files named above, with `enroll.go`, `main.go`, `evidence.go`, `commands.go`, `execute.go` and `metrics.go` the largest.

**Not verified in round five.** No Salt differential, no real AWS endpoint, no real fleet, no FreeBSD jail run, and `make check`. The five shown items were run on the Linux workstation only.

## Round six: `dbus`, `winsec`, `doctor`, `nodeevidence`, `tracing`, and parts of the two commands

On 2026-11-02, at `main` after #587. Read in full: `internal/dbus`, `internal/winsec`, `internal/doctor` (`doctor.go`, `checks.go`), `internal/nodeevidence` (`evidence.go`, `digest.go`, `verify.go`, `receipt.go`, `anchors.go`), `internal/tracing` (`tracer.go`, `otlp.go`, `settings.go`, with `context.go` and `carrier.go` from round five), and in `cmd`: `halite-hub/relay.go`, and `halite-node/execute.go` (to the executor), `enroll.go` (lines 156 to 600) and `jobsignature.go`. Nothing is fixed in this round. Each lead says whether it was run.

### Shown (a scratch test, run on this commit and then removed)

| What | Shown how | Status |
|---|---|---|
| **A job whose module panics returns nil, and the executor dereferences it.** `executeJob` recovers the panic in a deferred function and calls `fail(ret, ...)`, but its result is unnamed, so the function returns the zero value, a nil `*job.Return`. `executor.Run` reads `ret.Success` on the next line, in a goroutine nothing recovers. The comment above the recover says a panicking module "must produce a failed return rather than take the agent down". | a registered function that panics, run through `executeJob`: the log line is written and the result is `nil`. The dereference in `executor.Run` is by reading; the executor itself was not run | open; the most serious of the round |
| **A chain record edited and relabelled with an unknown schema verifies.** `Verify` checks a record's hash only when its schema is the one this build writes; for any other it checks the link and says "a newer halite, not a broken chain". The link compares the stored `hash` field, which the edit leaves alone, and the hub's anchors compare that same field, so neither sees it. `verify-evidence` prints "The chain holds: every record's contents match its hash". | three records written with `Log.Append`; the second one's detail changed to `cmd.run rm -rf` and its schema to `halite.evidence/99`: `OK()` true, no breaks, one unknown schema. The same edit with the schema left alone is caught (one break) | open |
| `dbus.decode` panics on a header or body signature that is empty or is a bare `(`, from the peer. | a header field with an empty variant signature: `index out of range [0] with length 0`; one with `(`: `slice bounds out of range [1:0]`. A bare `a` returned an error | open; low in practice, because the bus validates messages before it routes them to a client, which was not checked |

### Read, not run

- `executeJob`, in the same function: nothing else catches a panic in `executor.Run`, so the loss is the node process and not one job. The test file has no test of a panic (grep: none).
- `runConnect`: `hub_tries` is documented as "Reconnect attempts before giving up", and `attempt` is never reset, so a node with `hub_tries: 5` that stays connected for months stops after its fifth disconnection in total. The backoff only resets on a clean end of the stream, so after a few errors every reconnection waits 60 seconds, and there is no jitter. The comment above `runConnect` says "a job arriving here is logged and refused rather than run", which has not been true for a long time.
- `doctor`: the extension check reads `require_signed_extensions`, which no declared setting is named, with a default of false; the setting a node enforces is `extension_require_signature`, default true. On a default node an unsigned extension is reported "They will load" (a warning) when the node refuses to load it. The remedy text names `extension_trust` (the setting is `extension_trust_keys`) and the queue check names `queue_depth` and `workers` (`reactor_queue_depth`, `reactor_workers`). One report of a setting nothing declares and two of names that are not the settings.
- `nodeevidence.Log.Append`: a short write (a full disk, which `doctor` names as the usual reason) leaves partial bytes in the segment, and the next record is appended after them with no newline, so one failed record can cost the next good one too. `seal` closes the file before it renames, so a failed rename or an existing target leaves the log holding a closed file and every later `Append` fails until the process restarts.
- `winsec.Others` reports an account only if its entry grants one of a set of read and write bits: an entry that grants only `DELETE` or `FILE_APPEND_DATA` is not reported, though either lets the account alter or replace the file; an entry marked inherit-only, which does not apply to the object itself, is reported. `enablePrivilege` reads `GetLastError` after `AdjustTokenPrivileges` returned success, which Go may do on another thread, so `ERROR_NOT_ALL_ASSIGNED` can be missed. `Entries` reads the SID of an object ACE at the layout of a plain one. Not run: nothing here builds on Linux.
- `tracing`: a failed export is dropped with no counter and no log (`_ = t.Export.ExportSpans`), and `Dropped()` counts only the full queue, so a collector that is down shows zero. SPEC 26.2 asks for a counter behind every drop. One NaN attribute makes `json.Marshal` fail for the whole batch of up to 256 spans. A caller who sends `traceparent` with the sampled flag makes the hub record and export spans for its request whatever the configured ratio; that is parent-based sampling as designed, and is a decision, not a finding. With no exporter configured a flush dereferences nil.
- `dbus`: `decode` allocates `bodyLen` and the padded header length as the peer states them (up to 4 GiB; the specification's limit is 128 MiB), and `Hello` indexes the reply body without checking it has one. An array of an empty struct type never advances and would loop for ever. `x` and `d` decode to `uint64`, so a signed 64-bit value or a double is not what the caller is given. The auth line is read without a limit.
- `varlink`, `awsauth`, `metrics`: as in round five.
- `halite-hub relay`: an upstream written as a bare IPv6 address contains a colon and so is not given a port.

### Checked, no gap

`nodeevidence` receipts: the payload is one fixed line per field, every field is held to an alphabet with no newline, and the time must print back identically, as the comment says; `VerifyReceipt` checks the node named in the receipt against the chain it is filed in. `Digest` is stable under reordering and tags types. `winsec` permission masks are the Windows values for full control, modify, read and execute, read and write. `Restrict` and `RestrictDir` give the owner, SYSTEM and Administrators and nobody else, protected from the parent.

### Still unread

**Not read:** `internal/doctor/fips.go` and the `disk_*.go` files, `internal/tracing/span.go`, the rest of `internal/log/log.go`, and the `*_windows.go` and `*_other.go` files outside `winsec`. In `cmd/halite-hub`: `run.go`, `main.go`, `doctor.go`, `orch.go`, `jobs.go`, `runner.go`, `gitfs.go`, `s3fs.go`, `extpillar.go`, `extensions.go`, `extensioncmd.go`, `extensionrun.go`, `evidence.go`, and the rest of `serve.go`. In `cmd/halite-node` (`metrics.go` and `returner.go` were read in round seven, `extensions.go` and `schedule.go` in part): `main.go`, `commands.go`, `evidence.go`, `oneshot.go` apart from `writeInlineFile`, `trace.go`, `anchor.go`, `beacons.go`, `runtimeconfig.go`, `mine.go`, `rendersandbox.go`, `renewal.go`, `doctor.go`, and the rest of `execute.go` and `enroll.go`.

**Not verified in round six.** No Windows machine (so nothing in `winsec` was run), no real bus, no real node process (the panic was run through `executeJob`, not the executor), no hub with anchors, and `make check`.

## Round seven: the hub's `run` and the node's metrics, returner and schedule

On 2026-11-02, after the round-six fixes were opened. Read in full: `cmd/halite-hub/run.go`, `cmd/halite-node/metrics.go` (to the serve loop), `cmd/halite-node/returner.go`; in part: `cmd/halite-node/schedule.go` (to `runScheduled`) and `cmd/halite-node/extensions.go` (to `registerExtensionFunctions`). Nothing is fixed in this round.

### Shown (a scratch test, run and removed)

| What | Shown how | Status |
|---|---|---|
| **The node's `fun` label takes whatever name the hub sent.** `halite_node_jobs_total` and `halite_node_job_duration_seconds` are labelled with the job's function from the job, and a node counts a job for a function it does not have like any other (a failed return). The hub's own copy of the label was bounded by the name's shape (5.532); the node's is not. | 600 jobs named `junk.fnN` and then `test.ping` through `countJob`: 512 series, an `__overflow__` among them, and no series for `test.ping`. A fix (label is the name when `registry.Exec.Has` or a state function, else `other`) was written, passed the node's tests but for the one that pinned `fun="nosuch.function"`, and was not kept | open |

### Read, not run

- `run --ttl 500ms` is read by `seconds()` as `int(d.Seconds())`, which is 0, and 0 means the hub's default (15 minutes, and 15 minutes in the signed path): a sub-second or negative TTL is not refused and not applied. The other duration flags of `run` read the same function.
- `operatorClient` and `portOf` take the port from the last colon of `listen`, so a `listen` of `[::1]` (no port) gives `:1]`; an `--hub` that is a bare IPv6 address contains a colon and is read as host and port.
- `resolveTarget` reads a matcher flag's value that is `true`, `yes`, `1`, `on`, `false`, `no`, `0` or `off` as a switch and not a target, so `run -L 1 test.ping` takes `test.ping` for the target and has no function. A node or glob named like one of those words cannot be given to a matcher flag.
- The comment that opens `gather` sits above `seconds` and describes neither.

### Checked, no gap

`prepareSigning` builds the job from the request and passes it through `job.SigningPayload`, as the node does. The functions that run other functions later, `schedule.add` and `schedule.modify` with `schedule.run_job`, are marked `arbitrary_code`, so a node requiring signatures for that class refuses an unsigned one; `schedule.enable_job` is not, and enables an entry the operator wrote. The node's schedule has no pillar source (SPEC 20.1 lists one; DIVERGENCE records it), which is why a hub cannot schedule something the signature check would have refused. The node's metrics endpoint has no plaintext mode and verifies a scraper's certificate when `metrics_client_ca` is set.

**Not verified in round seven.** Prometheus, a real hub, and `make check`.

## Round eight: the file server backends, the node's environment controls, and the job cache commands

On 2026-11-02. Read in full: `cmd/halite-hub/gitfs.go`, `s3fs.go`, `extpillar.go`, `jobs.go` (to `export`), `cmd/halite-node/anchor.go`, `enroll.go`'s remaining handlers, and in `main.go` the identity resolution (lines 447 to 560) and `checkEnvPermitted`; with `internal/gitfs/backend.go`'s `Update` and `sweep`, which the first of them calls. Nothing is fixed in this round.

### Shown (a scratch test, run and removed)

| What | Shown how | Status |
|---|---|---|
| **One failed git remote drops the environments it served.** `Backend.Update` keeps the last good tree when every remote fails, and says so; when only some fail it replaces what is served with what the others returned. A hub with two remotes, one of which is unreachable for one update, stops serving the failed remote's branches (`staging`), and every node asking for them gets a missing tree, though the last good checkout is on disk. The error is "some gitfs remotes served nothing". | two local repositories, `main` in one and `staging` in the other; one update serves `[base staging]`; the second repository is removed and the next update serves `[base]` | open |
| **The node's environment allowlist and denylist use `path.Match`**, which the rest of the tree left for fnmatch (5.501) because its negation is `[^x]` and not `[!x]`. `[!b]*` as a denylist denies `base` and permits `dev`; as an allowlist `[!p]*` refuses `base` and permits `prod`: the opposite of what it says, in the control SPEC 28.3 asks for. And a malformed pattern is skipped with the error discarded, so a denylist entry that does not parse (`prod[`) denies nothing, where an allowlist one at least fails shut. | `checkEnvPermitted` against three configurations, as above | open |

### Read, not run

- `jobs active` lists the 200 most recent jobs and answers "nothing is in flight" when none of them is, so a batch older than that, still running, is reported as not there. (5.536's `AbandonRunnerJobs` has the same window, 5000, and says so.)
- `anchorProblem` logs a conflict, which is the signal that the evidence chain was rewritten, once at error level and then as debug lines for every later report until the process restarts, and there is no metric of it. `verify-evidence --anchors` finds it from the hub's file; nothing a monitor watches does.
- `openJobs` joins `state_dir` and `/jobs` as a string, where the hub joins with `filepath.Join`; the two agree on unix.

### Checked, no gap

`Backend.Update`'s all-failed and all-refused paths keep the last tree and do not sweep it (5.293). `s3Provider` and `s3Buckets` read per-bucket keys against a closed list and refuse a misspelt one. An external pillar source is hard-fail unless `ext_pillar_fail` is exactly `ignore`, so a typo fails in the safe direction. A receipt is checked against the head the node reported, and then against the CA it pins, before it is filed.

**Not verified in round eight.** A real git remote going away and returning over a network, a real S3 endpoint, and `make check`.
