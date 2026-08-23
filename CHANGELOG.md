# Changelog

halite was rewritten against [SPEC.md](SPEC.md) starting 2026-08-19. The
proof of concept that preceded it — releases 0.1.0 through 0.12.0 — was
deleted rather than evolved: it had established what the shape should be,
and carrying its code forward would have meant carrying its assumptions
too. None of it remains, so its changelog has gone with it. `git log`
before `3aec717` is where that history lives if it is ever wanted.

Nothing here is released yet. Phases 0, 1, and 2 of the delivery plan in
SPEC section 32 are complete: a node that manages its own tree, and a
fleet driven from a hub. Phase 3 has started. Versions resume at 1.0.0
when SPEC section 32's phase 6 exit criteria are met.

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

### A fleet is enrolled and driven from a hub

`halite-hub serve` is the control plane of SPEC section 6: one mutual-TLS
port, an enrollment CA of its own, and NDJSON over a stream the node
opens outward. A node generates its key, asks to enrol, and waits; an
operator compares the fingerprint out of band and accepts. `halite-node
enroll`, `renew`, and `connect` are the other half. There is no
pre-shared secret and no key an operator has to copy.

`halite-hub run '<target>' <fun>` resolves the target against the grains
each node reported, records the job with its expected respondents
*before* delivering it, and gathers the returns from the job cache. A
missing return is therefore detectable rather than invisible, which is
the difference between "it said no" and "it said nothing" — and the
command exits 1 and 3 for those two things.

An operator edits the tree on the hub and the fleet converges to it,
which is the exit criterion SPEC section 32 names for the phase. A node
compiles against the hub's tree, caches what it fetched, and asks
conditionally afterwards, so redeploying an identical tree costs a round
trip and no transfer. Pillar is compiled on the hub, per node, from the
identity on the certificate — so a node holds no other node's secrets
and has no way to ask for them.

`--batch` belongs to the hub rather than to the terminal. In Salt it
lives in the CLI, so closing the terminal abandons the run with half the
estate updated and no record of where it stopped; here the group has its
own record, `jobs active` says what is in flight, `jobs resume` picks up
a batch a hub restart interrupted, and a safe limit stops the rest of the
estate getting the same broken change.

Every submission is authorized against one policy file that denies by
default — including when the file is absent, which the hub says at
startup rather than treating as permission. A wildcard never grants a
function that runs arbitrary code, and the set of those comes from the
signatures the build ships rather than from a list, so a function marked
in a later build is covered without anyone remembering.

The event bus is a durable segmented log, not Salt's in-memory one. A
subscriber resumes from an offset, so a restart is lossless and an
incident can be reconstructed afterwards — which is exactly what a Salt
estate discovers it cannot do during one. A node's events are namespaced
under its own ID whatever tag it asks for: in Salt a minion can fire any
tag onto the master's bus, and a reactor listening on that tag turns that
into fleet-wide execution. <!-- lexicon:allow -->

Verified with real processes rather than only in tests: a hub and two
nodes, a highstate driven across both, applied, and run again to
convergence. [DIVERGENCE 5.11](docs/DIVERGENCE.md) says what that
established, what it did not, and the defects it found that the tests had
not.

### Functions that run on the hub

`halite-hub runner <module.function>` is the old `salt-run`, and the
start of phase 3. It is a request to the hub even when it is typed on the
hub, because an operator authenticates with a certificate and being
logged in is not one.

A runner is granted by the `runners:` list of a role rather than by
`functions:`. Permission to ask the hub a question and permission to run
a command on every node are different permissions, and Salt's
`external_auth` conflating them is how a `@runner` grant turns out wider
than it looked. A runner that then reaches the fleet —
`saltutil.refresh_pillar` does — is authorized a second time as the job
it dispatches, so the narrower grant cannot become the wider one.

Forty-two functions across `jobs`, `manage`, `key`, `nodegroups`,
`pillar`, `cache`, `fileserver`, `event`, `saltutil`, `survey`, and
`error`. Every call gets a jid, is filed in the job cache with the
principal that asked for it, and puts `halite/run/<jid>/new` and its
return on the bus — so "who asked the hub to accept that key, and when"
has an answer on disk.

The other forty names in SPEC 19.2's inventory are registered and answer
with the phase they arrive in, and `halite-hub runner list` prints the
whole inventory either way. Leaving a name out of the registry would make
"orchestration is not written yet" and "you have mistyped
`state.orchestrate`" the same message at the terminal, and an operator
cannot tell those apart.

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
suite, of which 157 agree and 26 are outside the subset by design, and
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
periodic-highstate ones and the `halite-hub` and `halite-node` daemons
work today; `halite-api` waits on phase 4.

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

The rest of phase 3, and phases 4 through 6. No beacons, no scheduler, no
reactors, no orchestration, no mine, no API, no OIDC or LDAP, no
webhooks, no returners, no bridge protocol, no gitfs, no s3fs, no
agentless mode, no relays, no FIPS artifact set, no detached job signing,
and no backtracking regex engine.

Two things inside phase 2 are still absent: `halite-hub files`, the push
in the other direction from `salt-cp`, and external pillar.

`halite-api` exists as a program that parses arguments and reports which
phase it needs, which is deliberate: the alternative is a program that
appears to work.

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
