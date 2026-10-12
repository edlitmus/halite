# The source tree at a glance

<!-- abstract: How the repository is organised, what rules the code lives under, how it is built and released, and where to start reading. -->
<!-- kind: concept -->
<!-- index: source code; repository layout; cmd; internal; ext; tools; contrib; vendor; dependency allowlist; golang.org/x/sys; reproducible build; Makefile; make check; BSD make; build flags; trimpath; CGO_ENABLED; no cgo; comments; DIVERGENCE; SPEC; where to start -->

This part of the handbook is for someone who wants to read, audit, change or
extend halite itself. It assumes you know what halite does (Parts I to VII) and
tells you where each thing lives. The package-by-package reference, generated
from the source, follows the three walkthroughs in this part.

## What is here

```
cmd/             the programs: halite-node, halite-hub, halite-api, and three reference extensions
ext/             the public package an extension author imports (the only non-internal package)
internal/        everything else: some seventy packages
tools/           release and documentation machinery that ships in no binary
contrib/         what ships beside the binaries: service files, man pages, examples, the lab (tofu)
docs/            the documentation, including this handbook (docs/handbook)
vendor/          the one third-party dependency, vendored
SPEC.md          the specification: the design, in 33 sections
plan.md          the ranked list of what is worth doing next, with reasons
CHANGELOG.md     what an operator sees change
```

At the time of writing that is roughly 175,000 lines of Go in the product
and tools and 140,000 lines of tests, in about 1,070 files. The proportion of
test to product code is the first thing to know about how this project is built.

## The rules the code lives under

**One third-party dependency.** `golang.org/x/sys`, vendored, pinned, and
policed by `internal/buildpolicy`, which fails the build if anything outside
a closed allowlist (SPEC 4.2) appears in the build graph. Everything a
conventional project would import is written here: a YAML parser, a Jinja
engine, an LDAP client, a D-Bus client, a WebSocket server, SigV4 request
signing, a Prometheus text writer, a CycloneDX writer. Adding a dependency is
a SPEC change and almost certainly the wrong answer; the project's rule is to
drop scope before breaking the constraint.

**No cgo, no plugins, no interpreter.** The binaries are static. Release builds
use `CGO_ENABLED=0`, `-trimpath`, and a `SOURCE_DATE_EPOCH` taken from the commit,
so two builders produce the same bytes, and the release workflow refuses to
attest until two builders agree (`make repro` checks the half of that one
machine can).

**Vocabulary.** The words that name the old role pair, and a short list of
others, are banned from source, configuration, logs, metrics and documentation
(SPEC 2.3), and a test enforces it. Quoting the old tool where it is genuinely
unavoidable is marked on the line with an allow comment, which a reviewer can see.

**Claims are checked.** The one rule everything else follows from, in the
project's own words: *a claim nobody demonstrated is a defect, whoever wrote it.*
It applies to code, comments, documentation and a contributor's report of
their own work. The tests that hold the prose to the code are described in
[how the handbook is built](60-how-the-handbook-is-built.md) and the audit
packages are in [the audits chapter](gen-code-audits.md).

**The shape of the commonest bug here is two paths that must agree**: a parser
and the tool it parses, a document and the code it describes, a fixture and a
real machine, a state's "is it converged?" and its "make it converge". When
hunting, look for the pair rather than the symptom. The second commonest is an
exculpatory conclusion ("this is fine, because…"), which ends an investigation and
deserves more scrutiny than an accusatory one.

**Comments explain why.** They are unusually long, and they explain reasoning,
especially why an obvious alternative is wrong. A comment that restates the
code is a defect. The generated package chapters quote them verbatim, and reading
them is the quickest way to learn the design.

## How it is built and tested

| Target | Does |
|---|---|
| `make build` | Builds the three programs into `bin/` |
| `make check` | The local gate: `fmt-check vet build-all test race policy fips-test` |
| `make test` | `go test -count=1 ./...` (the count matters: several audits read the whole repo and the test cache does not notice a file that did not exist when a result was recorded) |
| `make policy` | The lexicon, dependency allowlist and import rules |
| `make release-gate` | Refuses a release while any root-mutating module is merely `assumed` |
| `make dist` | Cross-builds every platform, assembles reproducible archives, SBOMs and the checksum manifest |
| `make handbook` | Builds the HTML edition of this handbook |
| `make perf`, `make chaos`, `make fuzz`, `make simfleet` | The scale, failure, fuzz and simulated-fleet layers |
| `make lab-up` … `make lab-down` | Throwaway cloud instances for live tests (they bill until torn down) |

The `Makefile` is **BSD make**: no `$(shell …)`, use `!=`; recipe lines are
separate shells. On macOS `/usr/bin/make` is GNU make 3.81, which ignores `!=`
silently, so the build and install targets refuse to run under it
(`bmake` or `gmake`).

Live tests are named `TestLive*`, are gated on `HALITE_SYSTEM_LIVE=1` and
usually root, and skip with a message saying why: a test that skips is not a
test that ran. CI (`.github/workflows/ci.yml`) runs unit, race, formatting,
policy, cross-builds, reproducibility, the Salt differential and FIPS on
Linux, macOS, Windows and FreeBSD; the `fleet.yml` workflow runs the live legs
that drive real tools as root.

## Where to start reading

In this order, which is roughly the order the data takes:

1. **`internal/value`**, the data model: nine types and an ordered map. Everything
   passes through it.
2. **`internal/yaml`** and **`internal/template`**, the two parsers written for
   this project, with their conformance corpora in `testdata/`.
3. **`internal/state`**, the compiler: top file, includes, extends, requisites,
   ordering, validation. [Chapter 52](52-life-of-a-highstate.md) walks it.
4. **`internal/runner`**, which executes a low state, and `internal/builtin`,
   where the modules are.
5. **`internal/transport`** and **`internal/hub`**, the control plane.
   [Chapter 51](51-life-of-a-job.md) walks a job through them.
6. **`internal/policy`**, small and worth reading in full.
7. **`SPEC.md`** next to the code, and **`docs/DIVERGENCE.md`**, the ledger of
   every place reality differed from what somebody assumed. A dozen recent
   sections are the project's accumulated experience, and they are written to
   be read.
8. **`internal/builtin/evidence.go`** for how much is actually known about each
   module, in its own words.

## The documents that are code

`SPEC.md`, `plan.md`, `README.md`, `CHANGELOG.md` and everything under `docs/`
are held to the code *mechanically*, by tests that fail CI:

| Package | Holds |
|---|---|
| `internal/specaudit` | The spec's module lists, totals and worked examples against the registry; the ledger's numbering, ordering and citations |
| `internal/docsaudit` | The generated documents against the code, counts quoted in prose, links, the command reference, the manual pages |
| `internal/buildpolicy` | The toolchain pin, the dependency allowlist, build flags, workflow and `go.mod` agreement, the vocabulary |
| `internal/handbook` | This handbook's structure, links, examples, index and generated chapters |

So a change to a module is usually also a change to the ledger, sometimes to the
spec, and often to a count in three files you did not expect. Run the audits;
they say exactly which. Never hand-edit a generated page: `docs/modules.md`,
`docs/configuration.md`, `docs/evidence.md` and the `gen-*` chapters here are
produced by `go run ./tools/gendocs docs` and `go run ./tools/handbook gen`.

## Key ideas

- Some seventy internal packages, three programs, one dependency, no cgo; **tests are
  about as large as the product**.
- Read in data order: `value`, `yaml`, `template`, `state`, `runner`,
  `builtin`, `transport`, `hub`, `policy`.
- The comments say *why*; the ledger says what surprised somebody.
- The documents are code: they are checked, and the generated ones are
  regenerated, never edited.

Next: [the life of a job](51-life-of-a-job.md).
