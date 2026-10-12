# How halite is tested, and how much is known

<!-- abstract: The test layers, the evidence levels, the live and lab testing, the conformance harness, the release gate, and what each of them does and does not establish. -->
<!-- kind: concept -->
<!-- index: testing; test layers; unit tests; conformance; YAML test suite; Jinja corpus; differential test; property tests; fuzzing; chaos; live tests; TestLive; HALITE_SYSTEM_LIVE; fleet.yml; lab; evidence level; assumed; captured; hardware; release gate; conformance harness; broken on purpose; make check; race detector; coverage -->

A system that changes machines as root has to be able to say how it knows what
it claims. This chapter is how halite answers, and an honest account of where
the answer is thin. The authority for the current numbers is always
[the ledger](../DIVERGENCE.md) (section 5 is the test layers) and
[the evidence report](../evidence.md); this chapter is the structure around them.

## The principle: break it again on purpose

Every guard in this project was made by finding a defect and then asking *why
nothing noticed*. The practice that follows from that: **when you fix something,
break it again on purpose and watch the test fail.** A test that passes before
and after your fix was not testing your fix. This handbook's own checks were
built the same way: the renderer test for a wrapped sentence beginning "420."
failed against the original, and the `--out txt` fix has a test that prints the
Go struct it replaced when the fix is removed.

The corollary for documentation: **fixtures are captured, never written from
documentation.** A test built from a man page tests the man page. Real defects
have come from exactly this (`getfacl -s`, `snap list` and `ps` are three the
ledger tells), so a module's captured output comes from the real tool.

## The layers

SPEC 31 names fourteen layers. The ledger (section 5.2) states each one's real
status; in outline:

| Layer | What it does | Where |
|---|---|---|
| **Unit** | The default `go test`: every package, with the race detector in `make race` | everywhere |
| **Conformance, YAML** | The standard YAML test suite's 402 cases, vendored, each checked three ways; every disagreement has a row with a reason, enforced both directions | `internal/yaml/testdata` |
| **Conformance, templates** | Two corpora: cases mechanically extracted from Jinja's own tests, and cases written for Salt's added filters, strict undefined and the limits | `internal/template/testdata` |
| **Differential against Salt** | Ten trees compiled by both implementations; the low state compared chunk by chunk, against three Salt releases | `internal/saltdiff` |
| **Conformance, state modules** | A shared harness every state function must pass: test mode changes nothing, reports `None` exactly when a change would be made, and a second run changes nothing | `internal/states/conformance.go` |
| **Property** | Path containment never escapes a root; the topological sort is stable; requisite resolution terminates; the YAML parser never panics; targeting is monotonic under grain addition | `*/property_test.go` |
| **Fuzz** | The YAML parser and encoder, the template lexer and parser, the compound target parser (`make fuzz`) | |
| **Chaos** | Registered failure scenarios: hub restart mid-job, network partition, disk full, clock skew, certificate expiry mid-run, extension hang, event bus at its retention limit, reactor queue overflow | `internal/chaos`, `make chaos` |
| **Upgrade** | Skew between versions; a record from a newer hub is read but not rewritten | `internal/specaudit/upgrade_test.go` |
| **Scale** | The thirteen performance rows, two measured, the rest waiting on a harness (`tools/simfleet`) | `internal/perf`, `make perf` |
| **Security** | The dependency graph assertion; `make vuln` | `internal/buildpolicy` |
| **Reproducibility** | Every binary built twice from different paths; digests compared; the release workflow compares two builders | `make repro`, `release.yml` |
| **Integration** | A hub-and-nodes matrix across the tier-1 platforms. **Absent**: the last layer with nothing behind it | |
| **Live** | Tests that drive the real tools as root on real machines | below |

The ledger also states honestly what each layer does *not* establish, such as that
the Salt differential compares test-mode predictions and not the result of an
apply, or that a reproducibility check on one machine is not two independent
builders.

## Live tests and the lab

A unit test supplies a program's output, so it can only show that the parser reads
what the test author believed. Whether the belief was right needs the real tool,
and a module that mutates the machine needs a machine.

- Tests named `TestLive*`, gated on `HALITE_SYSTEM_LIVE=1` and usually root,
  drive the real package manager, firewall, filesystem tools and so on.
  **They skip with a message saying why**, and a test that skips is not a test
  that ran: when you rely on one, prove it executed.
- `.github/workflows/fleet.yml` runs the live legs on a schedule, on dispatch and
  on changes to the modules they cover: Debian and RPM-family containers, a
  Linux VM, an emulated **FreeBSD** machine, and macOS. If you add a live test, add
  it to the leg's `-run` filter and the trigger paths, or it will never run again.
- `contrib/tofu` raises throwaway cloud instances (seven Linux distributions and
  FreeBSD 14 and 15): `make lab-up`, `lab-wait`, `lab-facts`, `lab-test`,
  `lab-down`. **They bill until `make lab-down`**; tear them down in the same
  session you raised them.

FreeBSD carries most of production and so comes first, which has a trap in it. The
development host is FreeBSD, but an agent's shell there may be a Linux
compatibility layer, so `uname` lies. Anything platform-sensitive must be checked
on a real machine of that platform. The reverse trap is just as real: because the
development host is FreeBSD, FreeBSD legs *feel* covered when they are not (two
defects shipped because the machine in front of someone had a flag the platform in
general does not).

## Evidence levels

Every module declares how much anybody actually knows about it, in
`internal/builtin/evidence.go`:

| Level | Meaning |
|---|---|
| `assumed` | Tests written from documentation. The default, deliberately |
| `captured` | Tests hold output taken from the real tool, so parsing is checked; nothing has watched it *change* anything |
| `hardware` | The mutating path has been run against the real tool on a real system, and the note says which and when |

`sys.evidence` prints the table on any node, `docs/evidence.md` is generated from
it, the release notes are built from it, and `halite-node doctor` answers from it.
The note is part of the change that ran the thing: say which machine, which version,
and what you did **not** cover. A note that says only "runs on Linux" is worth little
at three in the morning, and the notes here are long for that reason.

### The release gate

`make release-gate` refuses a release while any module that changes the system as
root is still `assumed`. It runs once, where the decision is made, behind a build tag
so ordinary development is not blocked by it: a new module arrives `assumed`, which
is correct, and closing it means finding a machine. There are two honest ways past
it: demonstrate the module, or take it out of the build. Raising the level without
doing the work is the one option worse than not having the gate, because it
converts an unknown into a claim.

At the time of writing it passes. The handbook's chapter on
[the module library](11-modules.md) says how a user reads the levels.

## What a contributor does

- Write the test from a **captured** fixture, from the real tool.
- Run the layer that applies: `make check` locally; the live test on a machine of
  the platform you changed; the idempotency test for a new state.
- Break your fix on purpose and watch it fail.
- Update the evidence note in the same change when you ran something.
- Say, in the commit and the report, **what you did not verify.**

## Key ideas

- Tests exist to find out how the code could be wrong; **break the fix on
  purpose** and **capture fixtures from the real tool**.
- Fourteen layers; the ledger states each one's real state, including the one
  that is absent (integration) and the ones that are partial (scale,
  reproducibility).
- `TestLive*` need the real tool and skip loudly otherwise; the fleet workflow
  and the lab are where they run.
- Evidence levels are per module, public, generated, and gate the release.

Next: [how the handbook is built](60-how-the-handbook-is-built.md).
