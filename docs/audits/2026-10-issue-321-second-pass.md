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
