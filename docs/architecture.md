# Architecture

## Layout

```
cmd/halite/          CLI entry point (grains, apply, call, pillar, version)
internal/yamlite/    zero-dep YAML-subset parser (ordered maps)
internal/sls/        template rendering, state compilation, requisite sort
internal/pillar/     targeted per-host data, deep-merged
internal/engine/     plan executor (requisites, gates, watch propagation)
internal/modules/    state modules + platform backends
internal/grains/     host fact collection
```

## Pipeline

```
pillar tree
  → pillar.Load     match top.sls, render, deep-merge  ─┐
                                                        │
file.sls                                                ↓
  → sls.Render      text/template, grains and pillar in scope
  → yamlite.Parse   ordered tree of maps/lists/scalars
  → sls.Compile     flatten args, extract require/watch, topo-sort
  → engine.Run      run states in order, gate on failed requisites,
                    propagate watch triggers, return results
```

Pillar loads first because state files template against it. The executor
lives in `internal/engine` so the agent daemon can drive it directly.

## Design decisions

### ADR-1: Zero external dependencies

**Accepted.** Everything is Go stdlib. This is the point of the project:
Salt's operational pain is overwhelmingly dependency and packaging pain.
Consequences: we wrote a YAML-subset parser (~250 lines) instead of
importing one, and templating is `text/template` instead of a Jinja port.
Revisit only if a P2 feature genuinely cannot be done in stdlib (none
identified; mTLS + HTTP/2 are stdlib).

### ADR-2: YAML subset, not full YAML

**Accepted.** SLS files in practice use nested block mappings, block lists,
scalars, quotes, and comments. yamlite implements exactly that, preserves
key order (Salt semantics depend on declaration order), rejects tabs, and
keeps colons inside values intact. Anchors, flow collections, and
multi-line scalars are errors, not silent misparses. Trade-off: some
existing SLS files need mechanical edits. Benefit: the parser is small
enough to audit in one sitting and has no YAML-spec landmines
(Norway problem: everything is a string until a module interprets it).

### ADR-3: Go text/template instead of Jinja

**Accepted.** Reimplementing Jinja is a project-killer. `text/template` is
in every Go binary already and covers conditionals, loops, and variable
substitution. `{{ grains['os_family'] }}` becomes
`{{ .Grains.os_family }}`. Missing keys render as zero values
(`missingkey=zero`) so probing optional grains doesn't error.

### ADR-4: Masterless first

**Accepted.** `salt-call --local` is the semantic core; transport is an
add-on. Shipping a correct local engine first means the P2 master/agent is
"move the executor behind a stream", not a rewrite. It also means the tool
is useful on day one for image builds, jails, and cron-driven convergence
without any infrastructure.

### ADR-5: mTLS HTTP/2 transport (planned, P2)

**Proposed.** Options considered: ZeroMQ-alike (cgo or fragile pure-Go
bindings — rejected), NATS embedded (excellent but violates ADR-1 and adds
an operational surface), gRPC (violates ADR-1), stdlib HTTP/2 with mTLS
(chosen). Long-lived server-push streams carry the event bus; request
/response carries job dispatch and returns. Client certs replace Salt's
minion key exchange — `halite key` acts as a tiny CA. Everything is
debuggable with curl and openssl s_client.

### ADR-6: Backends are table-driven per platform

**Accepted.** `pkg` and `service` each define a small backend interface and
a detector (GOOS first, then LookPath). FreeBSD is first-class: pkg(8),
rc.d with `one*` verbs so states work before a service is enabled, and
`sysrc` for enabling. Adding a platform is one backend struct, not a new
module.

## Error and failure model

A state returns `{Ok, Changed, Comment, Changes}`. Requisite failure skips
dependents with an explicit comment (matching Salt). `apply` exits non-zero
if any state failed, so cron and CI can gate on it. `-test` is threaded
through the context and every module must honor it — a module that cannot
dry-run must say "would ..." and change nothing.

## Security posture

No network listener exists in v0.1 — attack surface is the binary and the
files it is told to read. When the transport lands: mTLS everywhere,
no unauthenticated ports, no pickle/msgpack deserialization of untrusted
data (JSON only), and the master never executes minion-supplied code.
