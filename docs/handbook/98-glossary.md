# Glossary

<!-- abstract: Every term the handbook uses, defined in a sentence or two, with where to read more. -->
<!-- kind: reference -->
<!-- index: glossary; terminology; vocabulary -->

Terms in alphabetical order. Where a term is the old name of something, it is not
here: the [vocabulary table](41-migrating.md) translates those.

**Accepted** (key state). A node whose enrolment an operator, or a token, approved
and which holds a certificate. See [chapter 25](25-trust-and-identity.md).

**ALPN.** The protocol identifier, `halite/1`, offered in the TLS handshake. A peer
that does not offer it is refused. See [chapter 12](12-hub-and-nodes.md).

**API.** `halite-api`: the HTTP interface, a client of the hub with its own
certificate. See [chapter 27](27-http-api.md).

**Argument vector.** A program and its arguments as a list, run without a shell, so
nothing can be reinterpreted. `cmd.run` with `shell: false`. See
[chapter 11](11-modules.md).

**Batch.** Running a job on part of the target at a time, with a safe limit. The
hub owns it. See [chapter 14](14-remote-execution.md).

**Beacon.** A watcher on a node that fires an event when something changes. See
[chapter 19](19-beacons.md).

**Bootstrap token.** A single-use, scoped, short-lived secret that admits a node
without an operator. See [chapter 25](25-trust-and-identity.md).

**Bridge.** The host side of the extension protocol; also "bridged", meaning
provided by an extension. See [chapter 29](29-extensions.md).

**CA (enrollment CA).** The certificate authority the hub runs; every node and
operator certificate is issued by it. See [chapter 25](25-trust-and-identity.md).

**Canary.** A small, randomly chosen part of a target changed first. `--subset`.

**Captured.** An evidence level: tests hold output taken from the real tool. See
[chapter 59](59-testing-and-evidence.md).

**Chunk.** One executable unit of the low state. See
[chapter 52](52-life-of-a-highstate.md).

**Compound target.** A boolean expression over typed terms: `G@os:Ubuntu and web*`.
See [chapter 13](13-targeting.md).

**Convergence.** The state of a machine that matches its description; a run is
converged when nothing changed. See [chapter 4](04-idempotency.md).

**Correlation.** An identifier meant to link causally related events. See
[chapter 18](18-events.md).

**Declarative.** Describing the end state rather than the steps. See
[chapter 4](04-idempotency.md).

**Dedupe window.** A reactor control: the first event with a key fires, later ones
in the window are dropped. See [chapter 20](20-reactors.md).

**Doctor.** `halite-* doctor`: the diagnostic that checks a program and says what
to fix. See [chapter 12](12-hub-and-nodes.md).

**Drift.** Any difference between a machine and its description that appeared after
the last apply. See [chapter 4](04-idempotency.md).

**Drop-in.** A fragment in a program's `.d/` directory, merged over its
configuration. See [chapter 33](33-configuration.md).

**Enrollment.** The one-time introduction of a node to a hub. See
[chapter 25](25-trust-and-identity.md).

**Environment.** A named copy of the tree (`base` by default). See
[chapter 10](10-top-and-files.md).

**Event.** A tagged message on the hub's bus. See [chapter 18](18-events.md).

**Evidence.** The per-module level of what is known (`assumed`, `captured`,
`hardware`), and the node's hash-chained record of what it did. See
[chapter 28](28-hardening.md) and [chapter 59](59-testing-and-evidence.md).

**Execution function.** A function run now, with a result you read:
`halite-hub run web1 service.restart nginx`. See [chapter 11](11-modules.md).

**Extension.** A signed, separate program that adds a module, pillar source, grain
provider, returner or signer. See [chapter 29](29-extensions.md).

**File server.** The part of the hub that serves the tree over the node's
authenticated connection. See [chapter 10](10-top-and-files.md).

**Fingerprint.** The SHA-256 digest of the CA certificate, the trust anchor given
to a node out of band. See [chapter 25](25-trust-and-identity.md).

**Fleet (estate).** All the nodes one hub manages.

**FIPS build.** The parallel artifact set whose cryptography runs through the Go
Cryptographic Module. See [chapter 28](28-hardening.md).

**Formula.** A directory of states for one concern, with its files and map. See
[chapter 10](10-top-and-files.md).

**Function (state or execution).** `module.function`. See
[chapter 11](11-modules.md).

**Glob.** A shell-style pattern, the default target type. See
[chapter 13](13-targeting.md).

**Grains.** Facts a node discovers about itself. See [chapter 8](08-grains.md).

**Hardware.** An evidence level: the mutating path was run against the real tool on
a real system. See [chapter 59](59-testing-and-evidence.md).

**Highstate.** Everything the top file assigns to a node, applied. See
[chapter 2](02-concepts.md).

**Hub.** `halite-hub`: the central server. See [chapter 12](12-hub-and-nodes.md).

**ID (state ID).** The unique name of a state declaration. See
[chapter 5](05-states.md).

**Idempotent.** Having the same effect applied twice as once. See
[chapter 4](04-idempotency.md).

**Job.** One command sent to a target, with a JID. See [chapter 15](15-jobs.md).

**Job cache.** The hub's record of every job and return. See
[chapter 15](15-jobs.md).

**JID.** The job identifier, `YYYYMMDDThhmmssffffff` in UTC.

**Jinja.** The template language of SLS files and delivered files. See
[chapter 7](07-templates.md).

**Ledger.** `docs/DIVERGENCE.md`: the record of every place reality differed from
what was assumed, and of what is not built.

**Low state.** The flat, ordered list of resolved states that will run. See
[chapter 52](52-life-of-a-highstate.md).

**Mine.** The hub-held notice board nodes publish to and read from. See
[chapter 22](22-runners-and-mine.md).

**Module.** A group of functions compiled into the node (`file`, `pkg`, …). See
[chapter 11](11-modules.md).

**Nodegroup.** A named target expression defined in `hub.yaml`. See
[chapter 13](13-targeting.md).

**Node.** A managed machine running `halite-node`. See
[chapter 2](02-concepts.md).

**Node ID.** The permanent name of a node. See [chapter 8](08-grains.md).

**Offline policy.** What to do about nodes that are not connected: `skip`, `queue`,
`require`. See [chapter 14](14-remote-execution.md).

**Operator.** A person or automation that instructs the fleet, identified by a
certificate. See [chapter 26](26-authorization.md).

**Orchestration.** A multi-step procedure that runs on the hub across nodes. See
[chapter 21](21-orchestration.md).

**Pillar.** Data you provide for nodes, compiled per node on the hub. See
[chapter 9](09-pillar.md).

**Policy.** The one deny-by-default authorization file. See
[chapter 26](26-authorization.md).

**Principal.** A named identity that makes a request: `cert:CN=ed`,
`node:web1`, `reactor:watch`. See [chapter 26](26-authorization.md).

**Provider.** The platform-specific implementation behind a virtual module
(`aptpkg` behind `pkg`). See [chapter 11](11-modules.md).

**Reaction.** The SLS file a reactor runs. See [chapter 20](20-reactors.md).

**Reactor.** A rule mapping an event pattern to a reaction. See
[chapter 20](20-reactors.md).

**Relay.** A hub that serves a segment and presents itself upstream as one client.
See [chapter 38](38-scaling.md).

**Renewal.** Replacement of a certificate at half its life. See
[chapter 37](37-certificates.md).

**Requisite.** A dependency between states: `require`, `watch`, `onchanges`,
`onfail`, `prereq`, `use`, `listen`. See [chapter 6](06-requisites.md).

**Return.** One node's answer to a job. See [chapter 15](15-jobs.md).

**Returner.** A destination for results or events. See
[chapter 23](23-scheduler-and-returners.md).

**Roster.** The target list for agentless mode. See
[chapter 17](17-agentless.md).

**Runner.** A function that runs on the hub. See
[chapter 22](22-runners-and-mine.md).

**Scheduler.** Timed jobs on a node. See
[chapter 23](23-scheduler-and-returners.md).

**SLS.** A file of states, pillar data or a top file. See
[chapter 5](05-states.md).

**State.** A statement of how one thing should be. See
[chapter 5](05-states.md).

**Splay.** A random delay added to a scheduled job so a fleet does not act at once.

**Target.** The set of nodes a command, state or rule applies to. See
[chapter 13](13-targeting.md).

**Template.** Jinja inside an SLS or a delivered file. See
[chapter 7](07-templates.md).

**Test mode.** A rehearsal: nothing changes; each state reports what it would do. See
[chapter 4](04-idempotency.md).

**Top file.** `top.sls`: the index assigning SLS files to nodes. See
[chapter 10](10-top-and-files.md).

**Tree.** The states and pillar directories together, kept in version control.

**Trusted grains.** The grains a pillar top file may target on. See
[chapter 9](09-pillar.md).

**Virtual module.** A module with one name and several platform providers
(`pkg`, `service`). See [chapter 11](11-modules.md).
