# Changelog

halite was rewritten against [SPEC.md](SPEC.md) starting 2026-08-19. The
proof of concept that preceded it — releases 0.1.0 through 0.12.0 — was
deleted rather than evolved: it had established what the shape should be,
and carrying its code forward would have meant carrying its assumptions
too. None of it remains, so its changelog has gone with it. `git log`
before `3aec717` is where that history lives if it is ever wanted.

Nothing here is released yet. Phases 0 and 1 of the delivery plan in SPEC
section 32 are complete, which makes a node that manages its own tree.
Versions resume at 1.0.0 when SPEC section 32's phase 6 exit criteria are
met.

## Unreleased

The state of the rebuild, by what it means rather than by commit.

### A node manages its own tree

`halite-node state apply --local` compiles and applies a tree with no hub:
top file, includes, extend, exclude, every requisite including `prereq`
and the `_in` and `_any` forms, ordering with `order` and `order: last`,
and the return schema of SPEC section 11.8. Errors from the whole
compilation are reported together rather than one at a time.

Verified against a real tree on a real machine, not only in tests: a
grain-matched top file with an include, a templated pillar loop, a
`salt://` source, `require` and `onchanges`, converging and then
reporting nothing to do on the second run and reconverging after a
hand-edit.

### The dialects, held to their own specifications

The **YAML 1.1 subset** parser of SPEC section 10.1, with ordered
mappings, anchors, aliases, merge keys, block scalars with correct
folding, and a node budget that stops an alias bomb. It runs the YAML
Test Suite on every `go test`: 328 of 402 cases agree, 34 disagree by
design under section 10.1.2, and 40 are recorded gaps.

The **Jinja-compatible template engine** of section 10.2, with the
statements, filters, and tests that section names, strict undefined by
default, and deterministic seeding so a `--test` run and the run after it
agree. It runs two corpora: 198 cases extracted from Jinja's own test
suite, and 123 written here for the surface those cannot reach.

Both corpora are enforced in both directions — an unrecorded disagreement
fails, and so does a recorded one that has been fixed — so neither can
quietly go stale.

### 42 execution modules, 20 state modules

209 execution functions and 54 state functions, against a specification
naming roughly 90 modules and 46. Every one is listed, with its
parameters, in [docs/modules.md](docs/modules.md); every gap is listed in
[docs/DIVERGENCE.md](docs/DIVERGENCE.md).

`x509` is worth calling out. Salt's needs M2Crypto or `cryptography`
compiled against OpenSSL headers, which is the most common reason a Salt
install fails; this one is `crypto/x509` and needs nothing. Its
`certificate_managed` also converges, where Salt's re-issues on every
highstate — a re-issued certificate has a new serial and a new expiry, so
it never matches what the last run left.

### The departures from Salt, each with a switch

Undefined template names are an error naming the file, line, and
identifier. `cmd.run` takes an argument vector and a shell is explicit
and logged. Command line arguments are strings unless the signature says
otherwise. Duplicate YAML keys are an error naming both lines. A
regular expression RE2 cannot express is refused by name rather than
quietly failing to match. `--test` is a contract enforced by a shared
harness every state module passes.

Each is described in the table in [README.md](README.md) with the
specification section that defines it, and each has a setting that
restores Salt's behaviour for a transition.

### Documentation

[Getting started](docs/getting-started.md), [writing
states](docs/states.md), [operations](docs/operations.md), and [migrating
from Salt](docs/migrating-from-salt.md), plus a configuration reference
and a module reference generated from the code and checked against it by
a test.

Service files for FreeBSD `rc.d` and systemd are in `contrib/`. The
periodic-highstate ones work today; the daemons wait on phase 2.

### The tests the specification asks for

Of SPEC section 31's fourteen layers: unit coverage on every package, the
YAML conformance suite, the template corpus, the state-module conformance
harness (stronger than specified — it also checks that test mode changed
nothing), all five named property tests, fuzzing of the YAML parser, the
template engine, and the target parser, the dependency-graph assertion,
and the FreeBSD half of the version-comparison differential.

Absent, and recorded as such: the differential against Salt itself, which
is named the primary correctness gate and needs a Salt installation to
run against; the integration, scale, upgrade, and chaos suites, which
need the hub; `govulncheck`; and reproducible-build verification, which
needs a second builder.

### What is not built

Phases 2 through 6. No transport, hub, enrollment, targeting over a wire,
beacons, reactors, orchestration, API, gitfs, or agentless mode.
`halite-hub` and `halite-api` exist as programs that parse arguments and
report which phase they need, which is deliberate: the alternative is a
program that appears to work.

FreeBSD is the only platform anything has run on. Linux, macOS, and
Windows compile and are otherwise unexercised, which means 60 of the 62
platform modules of SPEC section 15.3 and both non-FreeBSD package
providers are theory.

[docs/DIVERGENCE.md](docs/DIVERGENCE.md) is the full accounting, and it
is checked against the code by `internal/specaudit` rather than
maintained by hand.
