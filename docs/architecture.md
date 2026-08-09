# Architecture

## Layout

```
cmd/halite/          CLI: local, key, fleet, and ssh commands
internal/yamlite/    zero-dep YAML-subset parser (ordered maps)
internal/sls/        template rendering, state compilation, requisite sort
internal/pillar/     targeted per-host data, deep-merged
internal/engine/     plan executor (requisites, gates, watch propagation)
internal/modules/    state modules + platform backends
internal/grains/     host fact collection
internal/ca/         the fleet CA: keys, CSRs, enrollment lifecycle
internal/transport/  mTLS HTTP/2 wire types, TLS config, JSON client
internal/archive/    tar.gz pack and safe unpack
internal/master/     control plane: registry, dispatch, handlers
internal/agent/      managed-host side: enroll, poll, execute, report
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
Revisit only if a feature genuinely cannot be done in stdlib. Through P2
none did: mTLS and HTTP/2 are stdlib, the CA is `crypto/x509`, archives are
`archive/tar` and `archive/zip`, and `halite ssh` drives the system ssh
rather than importing one (ADR-8). The open question is pillar encryption
at rest, where age would be a dependency and composing `crypto/ecdh` with
AES-GCM would be ours to get right.

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

### ADR-5: mTLS HTTP/2 transport

**Accepted (0.4).** Options considered: ZeroMQ-alike (cgo or fragile
pure-Go bindings — rejected), NATS embedded (excellent but violates ADR-1
and adds an operational surface), gRPC (violates ADR-1), stdlib HTTP/2 with
mTLS (chosen). Client certs replace Salt's minion key exchange — `halite
key` is a tiny CA. Everything is debuggable with curl and openssl.

TLS 1.3 only, with no configuration knob to lower it. The caller's role
(agent or operator) is an organizational unit in its certificate, so
authorization never depends on anything the caller asserts.

Job delivery is a long poll rather than a server-push stream: the control
plane holds a `GET /v1/jobs` open until there is work. Agents therefore
need no inbound connectivity and work from behind NAT, and there is no
custom framing to debug. A real event stream arrives with the event bus in
P3, where it earns its complexity.

### ADR-6: Backends are table-driven per platform

**Accepted.** `pkg` and `service` each define a small backend interface and
a detector (GOOS first, then LookPath). FreeBSD is first-class: pkg(8),
rc.d with `one*` verbs so states work before a service is enabled, and
`sysrc` for enabling. Adding a platform is one backend struct, not a new
module.

### ADR-7: Agents fetch the tree, the master does not compile

**Accepted (0.4).** The alternatives were a Salt-style per-file fileserver
with hash-based caching, or compiling the plan on the master and shipping
resolved states. Instead the control plane serves the whole tree as a
tar.gz and the agent's pillar as JSON, and the agent runs the same loader
and engine as `halite apply`.

Consequences: one rendering path instead of two, so a fleet run and a
masterless run cannot diverge; no cache-invalidation protocol; and the tar
work is shared with `archive.extracted`. The cost is bandwidth on large
trees — the whole tree ships on every state job. If that ever hurts,
conditional fetch on a tree hash is a small addition that does not change
the model. Extraction is treated as untrusted input regardless: entries
that escape the destination, and anything that is not a regular file or
directory, are refused.

### ADR-8: `halite ssh` shells out to ssh(1)

**Accepted (0.4).** A Go SSH client means `golang.org/x/crypto/ssh`, which
ADR-1 rules out. Driving the system `ssh` and `scp` is not a compromise: it
inherits ssh_config, agent forwarding, `ProxyJump`, certificates, and
whatever else the operator already has working, and there is no second
implementation of host-key handling to get wrong. `-o` passes options
through for the rest.

The remote side needs only `sh`, `tar`, and a writable `/tmp`. Pillar is
rendered operator-side and shipped as JSON (`apply -pillar-json`), so a
managed host never receives another host's data — the one place where
agentless has to differ from an agent, which fetches its own.

## Error and failure model

A state returns `{Ok, Changed, Comment, Changes}`. Requisite failure skips
dependents with an explicit comment (matching Salt). `apply` exits non-zero
if any state failed, so cron and CI can gate on it. `-test` is threaded
through the context and every module must honor it — a module that cannot
dry-run must say "would ..." and change nothing.

## Security posture

Masterless, the attack surface is the binary and the files it is told to
read. With the control plane running (see [fleet.md](fleet.md)):

* **mTLS 1.3 on every connection**, with no downgrade path. Both ends
  authenticate against the fleet CA and nothing else.
* **One unauthenticated endpoint**, `/v1/enroll`, which can do exactly one
  thing: file a signing request that an operator must then accept. All
  bodies are size-capped, enrolled or not.
* **Identity comes from the certificate.** An agent's id, and its role, are
  read from its client certificate; the request body cannot influence
  either. A reported `id` grain is overwritten with the enrolled identity,
  so an agent cannot make itself the target of someone else's job, and
  results are only accepted from agents a job was actually dispatched to.
* **Agents cannot dispatch.** Only an operator certificate can queue work
  or list the fleet.
* **JSON only.** Nothing is deserialized into behavior — no pickle, no
  msgpack, no code from the wire. The state tree arrives as an archive that
  is refused if any entry escapes the destination or is not a regular file
  or directory, and the agent renders it with the same templating it uses
  masterless.
* **Private keys never move.** An agent generates its own key; only the
  signing request travels. Every key file is written 0600.
