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
| `grains.items` | `halite grains [-json]` | done | os, os_family, osrelease, kernel, arch, num_cpus, mem_total, host, username |
| Highstate output | Salt-style block output + `-json` | done | |
| `state.highstate` / top.sls | `halite apply` (no target) | done | grain and hostname glob targeting; single merged environment |
| Pillar | pillar tree with its own top file | done | targeted, deep-merged, include-capable; exposed as `{{ .Pillar.x }}` in states and templated sources. Encryption at rest: P2 |
| Requisites: require, watch | require, watch | done | watch triggers service restart / cmd.wait |
| Requisites: onchanges, prereq | onchanges, prereq | done | prereq uses an automatic dry run of its target |
| unless/onlyif/creates as universal state args | universal gates | done | evaluated by the engine for every state |
| Jinja templating | Go `text/template` | done | grains in scope; funcs: default, contains, split, join, lower, upper, hasPrefix, hasSuffix |
| Includes (`include:`) | include: | done | dedup, cycle-safe, includes-first ordering |

## Master/minion architecture

| Salt 3008 | halite | Status | Notes |
|---|---|---|---|
| salt-master / salt-minion daemons | `halited` control plane + agent | P2 | single binary, mode by flag |
| ZeroMQ transport, AES key exchange | mTLS over HTTP/2 (stdlib) | P2 | long-lived streams for the event bus; no ZeroMQ, no custom crypto |
| Minion key accept/reject | TLS client-cert issuance (`halite key`) | done | CSR flow replaces Salt's key dance; see docs/pki.md |
| Event bus / reactor | event stream + reactor rules | P3 | |
| Beacons | agent-side watchers emitting events | P3 | |
| salt-ssh (agentless) | `halite ssh` pushing the static binary | P2 | trivially better than salt-ssh: copy one binary, exec, stream results |
| Syndic | | out | flat fleets over mTLS scale far enough |
| Multi-master | DNS/LB failover | P4 | stateless masters make this simpler than Salt's |

## State modules

| Salt | halite | Status |
|---|---|---|
| file.managed / directory / absent | done | ownership, line diffs, and templated sources (`template: true`) |
| pkg.installed / removed | done | backends: pkg(8), apt, dnf, yum, zypper, pacman, apk, brew, choco, winget. versions/repos: P1 |
| service.running / dead | done | rc.d (+sysrc enable), systemd, sysvinit, launchd (partial), Windows SCM |
| cmd.run / cmd.wait | done | unless, onlyif, creates, cwd, env |
| user.present/absent, group.present/absent | done | pw(8), useradd/usermod, sysadminctl (partial), net user (partial); drift repair for uid/shell/home/gecos/groups |
| cron.present/absent | done | crontab(1) with identifier markers; Windows scheduled tasks P3 |
| sysctl.present | done | runtime + persist (sysctl.conf / sysctl.d); FreeBSD, Linux, macOS-runtime |
| archive.extracted | P2 | stdlib tar/zip, no shelling out |
| git.latest | P2 | shells to git |
| mount.mounted | P2 | fstab handling per-OS |
| network.managed | out | too OS-entangled; use file + service |

## Execution modules (ad hoc)

`halite call` reuses state functions today. A read-only exec module set
(`status.*`, `disk.*`, `network.*`) lands with the transport in P2, since
their value is mostly fleet-wide queries (`halite '*' disk.usage`).

## Ecosystem features

| Salt 3008 | halite | Status | Notes |
|---|---|---|---|
| Salt Mine | P3 | periodic grain/exec publishes to master |
| Orchestration (state.orchestrate) | P3 | master-side ordered runs across minions |
| Returners | P3 | pluggable result sinks (file, webhook, Postgres) |
| salt-api (REST) | P2 | comes nearly free with the HTTP/2 master |
| Windows-specific (registry, DSC) | P4/out | registry: P4; DSC: out |
| Salt extensions / Python modules | out | custom modules are Go, compiled in; external process modules (exec JSON protocol) considered for P4 |

## Phases

* **P1 — masterless completeness**. **Complete as of 0.3**: top files and
  highstate, includes, onchanges/prereq, templated sources, user/group/
  cron/sysctl, file ownership + diffs, universal gates, JSON output,
  template funcs. halite now replaces salt-call --local for file/pkg/
  service/cmd/user/cron/sysctl workloads.
* **P2 — transport** (mTLS HTTP/2 master+agent, key issuance, targeting,
  `halite ssh`, REST API, pillar). Target: replace a small salt-master.
  **In progress**: pillar and the CA (0.4) are done; the daemons,
  `halite ssh`, the REST API, and pillar encryption at rest are next.
* **P3 — events** (event bus, beacons, reactor, mine, orchestration,
  returners).
* **P4 — long tail** (multi-master, Windows registry, external process
  modules).
