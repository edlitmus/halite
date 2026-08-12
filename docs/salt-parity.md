# Salt 3008 feature parity and roadmap

This document maps Salt 3008.2 functionality to halite: what exists today,
what is planned, and what is deliberately out of scope. "Where possible"
means: the workflow is reproduced; the wire format and Python internals are
not.

## Legend

* **done** — implemented in v0.1
* **P1–P4** — planned phase
* **out** — deliberate non-goal

## Core execution model

| Salt 3008 | halite | Status | Notes |
|---|---|---|---|
| `salt-call --local state.apply` | `halite apply file.sls` | done | masterless is the v0 foundation |
| `salt-call --local <mod>.<fn>` | `halite call module.fn k=v` | done | |
| `test=True` | `halite apply -test` | done | every module implements dry-run |
| `grains.items` | `halite grains [-json]` | done | id, os, os_family, osrelease, kernel, arch, num_cpus, mem_total, host, username |
| Static grains file, `grains.setval` | `/etc/halite/grains` (or `$HALITE_GRAINS`), `halite grains set/unset` | done | plain YAML, merged over the detected grains; no grains modules |
| Highstate output | Salt-style block output + `-json` | done | |
| `state.highstate` / top.sls | `halite apply` (no target) | done | grain and id glob targeting; single merged environment |
| Pillar | pillar tree with its own top file | done | targeted, deep-merged, include-capable; exposed as `{{ .Pillar.x }}` in states and templated sources |
| Requisites: require, watch | require, watch | done | watch triggers service restart / cmd.wait |
| Requisites: onchanges, prereq | onchanges, prereq | done | prereq uses an automatic dry run of its target |
| unless/onlyif/creates as universal state args | universal gates | done | evaluated by the engine for every state |
| Jinja templating | Go `text/template` | done | grains in scope; funcs: default, contains, split, join, lower, upper, hasPrefix, hasSuffix |
| Includes (`include:`) | include: | done | dedup, cycle-safe, includes-first ordering |

## Master/minion architecture

| Salt 3008 | halite | Status | Notes |
|---|---|---|---|
| salt-master / salt-minion daemons | `halite master` + `halite agent` | done | single binary, mode by subcommand |
| ZeroMQ transport, AES key exchange | mTLS over HTTP/2 (stdlib) | done | TLS 1.3 only, long-poll job delivery; no ZeroMQ, no custom crypto |
| Minion key accept/reject | TLS client-cert issuance (`halite key`) | done | CSR flow replaces Salt's key dance; see docs/pki.md |
| Targeting (`salt '<tgt>' ...`) | `halite run <target> <kind>` | done | one target language shared with top files: globs, grain matches, G@/L@/E@/P@, and and/or/not |
| Event bus | tagged event stream (`/v1/events`, `halite events`) | done | in-memory, glob tag matching; see docs/events.md |
| Reactor | rules matching tags to jobs | done | templated actions, loop guard, rate limit; see docs/events.md |
| Beacons | agent-side watchers emitting events | done | disk, service, file; edge triggered; tags constrained to the agent's own id. See docs/events.md |
| salt-ssh (agentless) | `halite ssh` pushing the static binary | done | copies one binary, ships the tree, collects JSON; pillar rendered operator-side. See docs/agentless.md |
| Syndic | | out | flat fleets over mTLS scale far enough |
| Multi-master | agent failover across a list | done | masters share a CA and nothing else; failover, not a cluster (ADR-11) |

## State modules

| Salt | halite | Status |
|---|---|---|
| file.managed / directory / absent | done | ownership, line diffs, and templated sources (`template: true`) |
| file.recurse | done | whole-tree copy with modes, ownership, templating, and optional `clean` |
| pkg.installed / removed | done | backends: pkg(8), apt, dnf, yum, zypper, pacman, apk, brew, choco, winget. `version` and `hold` where the backend can express them; a pin it cannot express fails rather than installing the current version |
| pkgrepo.managed / absent | done | repository file per platform (pkg(8), apt, dnf/yum, zypper, apk) plus a metadata refresh; halite does not fetch signing keys |
| ssh_auth.present / absent | done | one authorized_keys entry per state, identified by the key body |
| service.running / dead | done | rc.d (+sysrc enable), systemd, sysvinit, launchd (partial), Windows SCM |
| cmd.run / cmd.wait | done | unless, onlyif, creates, cwd, env |
| user.present/absent, group.present/absent | done | pw(8), useradd/usermod, sysadminctl (partial), net user (partial); drift repair for uid/shell/home/gecos/groups |
| cron.present/absent | done | crontab(1) with identifier markers; Windows via schtasks under \halite\ (translation unit tested, unverified on a real Windows host) |
| sysctl.present | done | runtime + persist (sysctl.conf / sysctl.d); FreeBSD, Linux, macOS-runtime |
| archive.extracted | done | tar, tar.gz, zip; local or http(s) source with a required sha256 for remote; traversal-safe extraction |
| git.latest | done | shells to git; refuses dirty checkouts and foreign remotes unless forced |
| mount.mounted / unmounted | done | fstab per-OS; FreeBSD `mount -p`, Linux /proc/self/mounts, macOS `mount` |
| network.managed | out | too OS-entangled; use file + service |

## Execution modules (ad hoc)

`halite call` reuses state functions, and a read-only execution module set
sits beside them in its own registry: `disk.usage`, `status.uptime`,
`status.loadavg`, `network.interfaces`. Their value is mostly fleet-wide
(`halite run '*' call disk.usage`). More will follow as they earn their
place; the registry makes adding one a single function.

## Ecosystem features

| Salt 3008 | halite | Status | Notes |
|---|---|---|---|
| Salt Mine | done | periodic exec-module and grain publishes; read as `{{ .Mine }}` in states or with `halite mine`. See docs/events.md |
| Orchestration (state.orchestrate) | done | `halite orchestrate <name>`; steps are SLS states, so requisites and gates are the engine's own. See docs/orchestration.md |
| Returners | done | file (NDJSON) and webhook sinks on the control plane; no database driver under ADR-1 — point a webhook at something that owns the DB |
| salt-api (REST) | done (mTLS) | the control plane's JSON API is the REST API; authentication is client certificates, not tokens. A token/browser front door is P3 if it is ever wanted |
| Windows-specific (registry, DSC) | registry done, DSC out | reg.present/absent via reg.exe (translation unit tested, unverified on a real Windows host); DSC remains a non-goal |
| Salt extensions / Python modules | out | custom modules are Go, compiled in |
| External process modules | `_modules/` executables, JSON on stdin/stdout | done | ship with the state tree; see docs/external-modules.md |
| GPG pillar renderer (encryption at rest) | out | confidentiality is the directory mode; use sops/age/git-crypt to decrypt into the tree. ADR-9, docs/pillar-security.md |

## Checking an existing tree

The tables above and the gaps below are the general answer; `halite parse`
is the answer for one specific tree. It walks a state and pillar root,
renders and compiles every SLS file the way `halite apply` does, and
reports each construct halite does not take — with the translation it
needs and the line it is on. It exits non-zero while anything in the tree
is still unsupported.

```sh
halite parse                                  # Salt's own /srv/salt, /srv/pillar
halite parse -root ./states -pillar-root ./pillar
halite parse -errors -json
```

See [docs/migration.md](migration.md) for what every finding means.

## Known gaps

The tables above map the workflows halite set out to reproduce, and read
generously: halite covers Salt's *architecture* at close to full breadth,
but Salt's *content* — the module library, the targeting language, the
Jinja ecosystem, fifteen years of operational conveniences — is a small
fraction covered. This section is the honest list of what a Salt 3008
operator will reach for and not find. None of it is promised; items move
out of here by earning a phase, the way P1–P4 did.

### Targeting and grains

* Pillar (`I@`), subnet (`S@`), nodegroup (`N@`), and range (`R@`)
  matchers have no equivalent; a target using one is refused rather than
  silently matching nothing. Grain, list (`L@`), and regex (`E@`, `P@`)
  matchers and boolean combinations are implemented.
* Custom grains are static only: a YAML file merged over the detected
  facts (`halite grains set`, or `file.managed` on the file). Salt also
  has grains modules — code that computes a grain at run time — and
  targeted `grains.setval` across a fleet; halite has neither.

### State language

Missing from the SLS dialect: the `_in` reverse requisites (`require_in`,
`watch_in`, …), `onfail`, `listen`/`listen_in`, `order:`, `failhard`,
`retry:`, `parallel: true`, `check_cmd`, and `- names:` expansion. There
are no debugging equivalents of `state.show_sls`, `state.sls_id`, or
`state.single`. Most existing Salt trees will not compile without at
least `_in` and `names:`.

### Rendering

Go `text/template` with a handful of funcs stands in for Jinja with
hundreds of filters, macros, imports (the `map.jinja` pattern), and
`{{ salt['module.fn']() }}` cross-calls from templates. There is no
renderer pipeline (`#!py`, `#!jinja|yaml`) — one fixed render path. With
the YAML subset (no anchors, no multi-line scalars), porting a nontrivial
Salt tree is a rewrite, not a transliteration.

### Module breadth

Salt 3008 ships roughly 470 state and 500 execution modules; halite has
27 state functions and 4 execution modules. Raw counts flatter Salt —
much of it is niche — but these are everyday Salt with no halite answer:
`file.symlink`, `file.replace`/`blockreplace`/`line`,
`timezone`/`locale`/`hostname`, `kmod`, `alternatives`, firewall states,
`selinux`, `lvm`, `x509`, container states, Windows updates and ACLs. The
`_modules/` escape hatch exists, but it is per-site effort, not a
library.

### Environments and fileservers

One merged environment from one local directory. No `saltenv`/`pillarenv`
(base/dev/prod), no gitfs or s3fs, no `ext_pillar` (vault, git_pillar,
consul) — decrypt-into-the-tree (ADR-9) is the only external-secrets
story.

### Scheduling and job management

* No minion-side scheduler: Salt's "highstate every 30 minutes, splayed"
  has no equivalent — beacons and mine intervals are the only periodic
  machinery, and neither runs states. A halite fleet does not converge on
  its own; something external must poke the control plane.
* The job cache is in-memory, bounded, and lost on master restart;
  returners are the durable record and nothing queries them. No
  `--batch-size`, splay, or subset execution for rolling changes.

### Authorization

An admin certificate is omnipotent. Salt has eauth (PAM/LDAP), tokens,
`publisher_acl`, per-user function and target ACLs, and the wheel API.
halite's roles are exactly two: agent and admin.

### Events

Three polling beacons (disk, service, file digest) against Salt's ~30
beacon types — no inotify, process, load, memory, network, or login
watchers. The reactor can dispatch the five job kinds and nothing else;
Salt's reactor can invoke runners, wheel, and orchestrations. A halite
event cannot trigger an orchestration.

### Subsystems with no analogue

salt-run and the runner library, salt-cloud, salt-proxy, salt-virt, peer
publishing (minion-to-minion), mine ACLs (`allow_tgt`), the external job
cache. Some of these are close cousins of the listed non-goals; they are
recorded here so the boundary is explicit rather than implied.

### If there is ever a P5

In priority order, judged by what blocks a real Salt migration first:

1. ~~Static custom grains~~ — **done**: a YAML file merged over the
   detected facts, with `halite grains set/unset`.
2. ~~`file.recurse`, `ssh_auth.present`, pkg pinning + `pkgrepo`~~ —
   **done**: the minimum module set for real server fleets.
3. ~~Compound targeting~~ — **done**: `and`/`or`/`not`, parentheses, and
   the `G@`/`L@`/`E@`/`P@` matchers.
4. An agent-side scheduler, so the fleet converges without external cron.
5. `_in` requisites and `names:` — most Salt trees need both to compile.

## Phases

* **P1 — masterless completeness**. **Complete as of 0.3**: top files and
  highstate, includes, onchanges/prereq, templated sources, user/group/
  cron/sysctl, file ownership + diffs, universal gates, JSON output,
  template funcs. halite now replaces salt-call --local for file/pkg/
  service/cmd/user/cron/sysctl workloads.
* **P2 — transport** (mTLS HTTP/2 master+agent, key issuance, targeting,
  `halite ssh`, REST API, pillar). Target: replace a small salt-master.
  **Complete as of 0.4**: pillar, the CA, the mTLS control plane and agent,
  fleet targeting, `halite ssh`, the archive/git/mount state modules, and
  the read-only execution modules. halite now replaces a small salt-master.
  Pillar encryption at rest was dropped rather than built (ADR-9):
  confidentiality is the directory mode plus an external tool.
* **P3 — events**. **Complete**: event bus and streaming, returners,
  reactor, beacons, mine, orchestration.
* **P4 — long tail**. **Complete**: external process modules, multi-master
  failover, Windows registry. The roadmap's four phases are done; what
  remains are the deliberate non-goals and the known gaps above, and
  whatever real use turns up.
