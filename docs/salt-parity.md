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
| Targeting (`salt '<tgt>' ...`) | `halite run <target> <kind>` | done | one target language shared with top files |
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
| pkg.installed / removed | done | backends: pkg(8), apt, dnf, yum, zypper, pacman, apk, brew, choco, winget. versions/repos: P1 |
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
| Windows-specific (registry, DSC) | P4/out | registry: P4; DSC: out |
| Salt extensions / Python modules | out | custom modules are Go, compiled in |
| External process modules | `_modules/` executables, JSON on stdin/stdout | done | ship with the state tree; see docs/external-modules.md |
| GPG pillar renderer (encryption at rest) | out | confidentiality is the directory mode; use sops/age/git-crypt to decrypt into the tree. ADR-9, docs/pillar-security.md |

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
* **P4 — long tail** (multi-master, Windows registry, external process
  modules).
