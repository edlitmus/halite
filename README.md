# halite

A replacement for SaltStack, written in Go against the standard library.

Halite keeps the parts of Salt that carry operational value — the state
system, pillar, the YAML and Jinja dialects, remote execution, reactors,
orchestration, beacons — and replaces the parts that carry risk: the Python
interpreter, the several-hundred-package dependency tree, the bespoke
encryption protocol, and the dynamic module loader that imports arbitrary
code from the file server.

[SPEC.md](SPEC.md) is the specification, in 33 sections. This README
describes what is built.

## Status

Delivery follows the phases in SPEC section 32.

| Phase | Contents | State |
|---|---|---|
| 0. Foundations | YAML parser, template engine, ordered model, module signatures, configuration and compatibility shim, migration report, build policy | **Done** |
| 1. Local state and pillar | Standalone `halite-node`: state compiler with all requisites, pillar, core modules, grains, `--local`, test-mode conformance harness | **Done** |
| 2. Hub, transport, enrollment | `halite-hub serve`, mutual TLS, targeting over the wire, job cache, file server, RBAC, event bus | Not started |
| 3. The automation loop | Beacons, scheduler, reactors, orchestration, runners, mine | Not started |
| 4. API and integration | `halite-api`, OIDC, LDAP, webhooks, returners, the bridge protocol | Not started |
| 5. Breadth | gitfs with signature verification, s3fs, Windows and macOS parity, agentless mode, relays, FIPS artifacts | Not started |
| 6. Hardening to 1.0 | Scale harness, chaos suite, external review, detached job signing, backtracking regex engine | Not started |

Phases 0 and 1 are done in the sense that their contents are implemented and
exercised, not that the module inventory of SPEC section 15 is complete: this
build ships 32 execution modules and 16 state modules against a specification
that names roughly 90 and 46. FreeBSD is the only platform anything has been
run on. **[docs/DIVERGENCE.md](docs/DIVERGENCE.md)** is the full accounting —
every module gap, every unexercised platform, every test layer SPEC section 31
requires that does not exist yet, and the handful of places the implementation
deliberately departs from the specification text.

That ledger is checked mechanically: `internal/specaudit` compares it against
both SPEC.md and the registries a build actually ships, and fails if a gap is
unrecorded, a recorded gap has been filled, or a function count has drifted.

## What works today

A node compiles and applies its own tree, from local roots, with no hub:

```
halite-node state apply --local \
    --file-root /srv/salt --pillar-root /srv/pillar --id web1.prod

halite-node state apply --local --test        # change nothing, predict everything
halite-node state show_lowstate --local       # the ordered run, before running it
halite-node pillar items --local --out yaml
halite-node grains get os_family --local
halite-node call test.ping
```

And a Salt tree can be measured before any of it is committed to:

```
halite-hub migrate /srv/salt --pillar-root /srv/pillar
```

The migration report exits non-zero on a blocking finding, so it gates CI
from the first day of a migration rather than at the end of one.

## Dependencies

```
$ go list -m all
github.com/edlitmus/halite
```

That is the whole graph. The allowlist in SPEC section 4.2 permits
`golang.org/x/sys` and `golang.org/x/term`, and neither has been needed
yet. `internal/buildpolicy` fails the test suite if anything else appears
at any depth, if `math/rand` is imported outside the deterministic template
seed, if the build recipe drops an integrity flag, or if a prohibited term
from the lexicon policy reaches source, configuration, logs, or fixtures.

## Building

```
make build      # the three binaries into bin/
make check      # fmt, vet, test, race, policy
make release    # vendored, offline, cgo off, as CI builds
make cross      # every supported platform into dist/
```

Go 1.25 or later. The Makefile is written for BSD make, which is what is
tested; it uses the `!=` assignment rather than GNU make's `$(shell ...)`,
and GNU make has supported `!=` since 4.0, so it should serve both. Only
the BSD path has actually been run.

## The three binaries

| Binary | Role | Replaces |
|---|---|---|
| `halite-node` | Endpoint agent and local executor | the Salt agent, the local caller, the proxy |
| `halite-hub` | Central service, file server, pillar compiler, and the operator command line | the Salt central service and its seven operator commands |
| `halite-api` | HTTP API service | the Salt API service |

## What is different from Salt, on purpose

These are the departures a tree will notice. Each has a switch, and each is
described in the specification section named.

| Behaviour | Salt | Halite | Section |
|---|---|---|---|
| Undefined template names | Render as empty string | Error naming file, line, and identifier | 10.2.6 |
| `cmd.run` | Shell by default | Argument vector by default; `shell: true` opts in and logs | 15.2 |
| Command line arguments | YAML-parsed, so `1.0` becomes a float | Strings unless the signature says otherwise | 9.2 |
| Duplicate YAML keys | Silent last-wins | Error naming both lines | 10.1.2 |
| Compilation errors | First one, then stop | All of them, together | 11.2 |
| Regular expressions | Python `re` | RE2, with unsupported constructs a hard error naming the construct | 10.4 |
| `test=True` | Unreliable for many modules | A contract, enforced by a shared conformance harness | 11.6 |
| Random in templates | Unseeded, so a test run and the real run disagree | Seeded per node and job | 10.2.4 |
| Pillar grain targeting | Any grain, including one a node made up | An allowlist; trusting a custom grain is a recorded decision | 12.4 |
| A templated YAML error | Reports the rendered position, or nothing | Reports the line in the `.sls` you wrote, and the rendered line | 10.1.4 |

## Layout

```
cmd/halite-node      the agent and local executor
cmd/halite-hub       the central service and operator command line
cmd/halite-api       the HTTP API service

internal/value       the nine-type ordered data model and its JSON codec
internal/yaml        the YAML 1.1 subset parser and encoder
internal/template    the Jinja-compatible engine
internal/render      the renderer pipeline and its source map
internal/regexcompat the RE2 limitation, made mechanical
internal/signature   machine-readable module signatures
internal/config      configuration loading and the Salt compatibility shim
internal/target      target expressions and the compound grammar
internal/state       the state compiler
internal/pillar      the pillar compiler
internal/fileserver  halite:// and salt:// resolution with path containment
internal/exec        the execution module surface
internal/states      the state module surface and the conformance harness
internal/builtin     the modules that ship
internal/runner      low state execution and the return schema
internal/grains      fact collection
internal/migrate     the Salt tree audit
internal/buildpolicy the specification's own build rules, as tests
internal/specaudit   SPEC.md and the gap ledger, held to what ships
```

## Licence

See [LICENSE](LICENSE).
