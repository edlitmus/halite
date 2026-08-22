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
Test Suite on every `go test`: 331 of 402 cases agree, 34 disagree by
design under section 10.1.2, and 37 are recorded gaps. The dialect SPEC
10.1 actually asks for is PyYAML's rather than the standard's, so 114
documents also go to PyYAML itself and the *resolved type* is compared
as well as the value — the two agree on every character of `mode: 0644`
and can still disagree on whether it is a string or the integer 420.
That found SPEC 10.1.3 stating PyYAML's behaviour incorrectly twice
more, for `0o17` and `1e3`.

The **Jinja-compatible template engine** of section 10.2, with the
statements, filters, and tests that section names, strict undefined by
default, and deterministic seeding so a `--test` run and the run after it
agree. It runs two corpora: 198 cases extracted from Jinja's own test
suite, of which 153 agree and 27 are outside the subset by design, and
123 written here for the surface those cannot reach.

Both corpora are enforced in both directions — an unrecorded disagreement
fails, and so does a recorded one that has been fixed — so neither can
quietly go stale.

### 42 execution modules, 20 state modules

209 execution functions and 56 state functions, against a specification
naming roughly 90 modules and 46. Every one is listed, with its
parameters, in [docs/modules.md](docs/modules.md); every gap is listed in
[docs/DIVERGENCE.md](docs/DIVERGENCE.md).

`x509` is worth calling out. Salt's needs M2Crypto or `cryptography`
compiled against OpenSSL headers, which is the most common reason a Salt
install fails; this one is `crypto/x509` and needs nothing. Its
`certificate_managed` also converges, where Salt's re-issues on every
highstate — a re-issued certificate has a new serial and a new expiry, so
it never matches what the last run left.

An existing `#!yaml|gpg` pillar works. SPEC 12.6 fixes the shape and this
follows it: shell out to the system gpg, link no OpenPGP library, take
the binary, home directory, and timeout from configuration, and pass the
ciphertext on standard input and never on a command line. Salt's
`gpg_keydir` maps onto `gpg_home` through the compatibility shim.

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
states](docs/states.md), [operations](docs/operations.md), [migrating
from Salt](docs/migrating-from-salt.md), and a [command
reference](docs/command-reference.md) giving every Salt command and what
to type instead — plus a configuration reference and a module reference
generated from the code and checked against it by a test.

The prose is checked too, as far as prose can be. A test runs every
command the matrix presents as working and confirms that every command
it promises in a later phase is one the binary already knows the name
of; another reads any sentence stating how many functions ship and
compares it with the registries. Example configurations live in
`contrib/examples/`, and a test loads each as the program it is written
for and fails on any warning.

Service files for FreeBSD `rc.d` and systemd are in `contrib/`. The
periodic-highstate ones work today; the daemons wait on phase 2.

### The tests the specification asks for

Of SPEC section 31's fourteen layers: unit coverage on every package, the
YAML conformance suite, the template corpus, the state-module conformance
harness (stronger than specified — it also checks that test mode changed
nothing), all five named property tests, fuzzing of the YAML parser, the
template engine, and the target parser, the dependency-graph assertion,
and the FreeBSD half of the version-comparison differential.

The **differential against Salt** — SPEC 31's primary correctness gate —
runs, against Salt 3006.25 and 3008.2. It compiles a corpus of trees
with both implementations and compares all three things the section asks
for: the low state, the pillar, and the state results, the last as
test-mode predictions rather than as an apply, since applying needs
somewhere to apply. `HALITE_SALTDIFF_TREES` points it at a real estate's
tree, and doing that to one found ten defects in an hour — among them a
`names:` entry whose own arguments were dropped, which on
`file.managed` meant seven scripts would have been overwritten with
empty files.

Absent, and recorded as such: the comparison of what an apply actually
does, which needs the containerised harness; the integration, scale,
upgrade, and chaos suites, which need the hub; `govulncheck`; and
reproducible-build verification, which needs a second builder.

### What is not built

Phases 2 through 6. No transport, hub, enrollment, targeting over a wire,
beacons, reactors, orchestration, API, gitfs, or agentless mode.
`halite-hub` and `halite-api` exist as programs that parse arguments and
report which phase they need, which is deliberate: the alternative is a
program that appears to work.

FreeBSD is the platform this is verified on. `make test-linux`
cross-compiles the suite and runs it under this host's Linux compat
layer — 23 of 25 packages pass, and the `/proc` grain collector returns
the same 63 keys as the FreeBSD one with every hardware fact agreeing —
but that layer has no apt, dnf, systemctl, or useradd, so the providers
that need them, 60 of the 62 platform modules of SPEC section 15.3, and
macOS and Windows entirely, remain compiled and unexercised.

The filesystem layout follows the platform rather than SPEC 27.3's FHS
paths: a BSD keeps configuration under `/usr/local/etc` and durable
state in `/var/db`, and following the text literally put files in three
places no BSD administrator looks. DIVERGENCE 1.5 records it.

[docs/DIVERGENCE.md](docs/DIVERGENCE.md) is the full accounting, and it
is checked against the code by `internal/specaudit` rather than
maintained by hand.
