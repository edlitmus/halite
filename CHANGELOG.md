# Changelog

## 0.4.0 — 2026-08-09

P2 (transport): pillar, the fleet CA, an mTLS control plane with agents,
and the remaining P2 state modules.

### Fleet mode

* `halite master` serves an mTLS HTTP/2 control plane; `halite agent`
  enrolls, reports grains, waits for work on a long poll, and executes it
  with the same loader and engine as `halite apply`;
  `halite run <target> <kind>` dispatches and collects results;
  `halite agents` lists the fleet. Targets use the top-file language
  (`'*'`, `os_family:FreeBSD`, `web*`). Job kinds: state.highstate,
  state.apply, call, grains, pillar. See docs/fleet.md.
* Agents fetch the state tree as a tar.gz and their pillar as JSON, so a
  fleet run and a masterless run share one rendering path (ADR-7).
* TLS 1.3 only. Roles are stamped into certificates as an organizational
  unit: agents may not dispatch work, only operators may. An agent's
  identity comes from its client certificate, so a reported `id` grain
  cannot be used to target-spoof, and results are refused from agents a
  job was not dispatched to. `/v1/enroll` is the only unauthenticated
  endpoint and can only file a request for an operator to accept.
* Queued work expires (five minutes by default), so an agent that was
  down does not replay stale intent when it returns.
* New `id` grain, Salt's: the hostname masterless, the enrolled identity
  under a control plane. A bare target glob matches it.

### Keys and certificates

* `halite key`: a stdlib-only CA replacing Salt's minion key dance with a
  CSR flow — `init`, `server`, `admin`, `gen`, `submit`, `list`, `accept`,
  `reject`, `remove`, `show`. P-256 keys, private keys written 0600, agent
  certificates scoped to client auth and server certificates to server
  auth. The CA takes the identity from the operator's decision, not from
  the request; a second key for a known identity is refused; CSR signatures
  are verified; identities are constrained to safe filename characters.
  PKI directory via `-pki` or `$HALITE_PKI`. See docs/pki.md.

### Pillar

* Pillar tree: `<root>/../pillar/top.sls` targets hosts with the same
  matcher as a state top file, and the matched pillar SLS files are
  deep-merged (later files win on leaves, sibling keys survive, lists are
  replaced whole). Pillar files render with grains in scope and support
  `include:`. Root via `-pillar-root` or `HALITE_PILLAR_ROOT`; a missing
  tree yields an empty pillar rather than an error.
* Pillar data is available as `{{ .Pillar.x }}` in SLS files, in the state
  and pillar top files, and in `file.managed` sources with
  `template: true`.
* New command: `halite pillar [-json]` (Salt: `pillar.items`).
* Example pillar tree under examples/pillar/, wired into examples/tree.

### State modules

* archive.extracted: tar, tar.gz, and zip; a local or http(s) source, with
  a sha256 required for remote ones and verified before anything is
  unpacked. Extraction refuses entries that escape the destination and
  anything that is not a regular file or directory.
* git.latest: clone and fast-forward, refusing dirty checkouts and foreign
  remotes unless forced.
* mount.mounted / mount.unmounted: fstab handling per-OS, reading
  FreeBSD `mount -p`, Linux /proc/self/mounts, or macOS `mount`.

### Execution modules

* Read-only queries now live in their own registry, reachable from
  `halite call` and `halite run '*' call ...`: `disk.usage`,
  `status.uptime`, `status.loadavg`, `network.interfaces`. They take no
  requisites and change nothing. disk.usage is not implemented on Windows.

### Agentless

* `halite ssh <hosts> <kind> [args]` manages hosts with no agent: it probes
  the platform, pushes a matching static binary, ships the state tree, runs
  it, collects JSON, and removes its working directory. Hosts come from a
  comma list or a globbed `-roster`, run `-jobs` at a time. `-o` passes
  options through to ssh and scp.
* Pillar is rendered on the operator's machine from the host's reported
  grains and shipped as JSON, so a managed host never receives another
  host's data. `halite apply -pillar-json FILE` is the flag that makes that
  possible and is useful on its own.

### Protecting pillar data

* Pillar is deliberately not encrypted: its confidentiality is the pillar
  tree's directory mode, plus an external tool (sops, age, git-crypt) that
  decrypts into the tree before a run. Recorded as ADR-9 and documented in
  docs/pillar-security.md, with the reasoning and what to do instead.
* halite warns when it opens a pillar tree that group or others can read —
  from `apply`, `pillar`, `ssh`, and at control plane startup.
* `halite ssh` writes the remote pillar.json under `umask 077`, inside the
  0700 working directory it already used.

### Internals

* New packages: internal/ca, internal/transport, internal/archive,
  internal/master, internal/agent.
* `sls.ResolveName`, `sls.MatchTop`, and `sls.TargetMatch` are exported so
  the pillar tree and fleet targeting reuse the state tree's resolution and
  matching verbatim.
* Subcommand flags may now appear after positional arguments
  (`halite apply web.nginx -test`).

## 0.3.0 — 2026-08-09

P1 (masterless completeness) is done.

* Highstate: `halite apply` with no target reads `<root>/top.sls` and
  applies SLS names matched by grain (`os_family:FreeBSD`), hostname glob
  (`web*`), or `*`. Root via `-root`, `HALITE_ROOT`, or the platform
  default (/usr/local/etc/halite/states on FreeBSD, /etc/halite/states on
  Linux). Dotted SLS names (`halite apply web.nginx`) resolve to
  `<root>/web/nginx.sls` or `.../nginx/init.sls`.
* `include:` in SLS files — included states run first, files load at most
  once, cycles are tolerated, duplicate state declarations across the
  merged plan are a compile error with file attribution.
* New requisites: `onchanges` (run only when a referenced state changed)
  and `prereq` (run before the target, only if the target would change,
  via automatic dry run).
* file.managed: `template: true` renders `source:` files through
  text/template with grains.
* Executor moved to internal/engine (shared with the future agent daemon).
  Relative sources now resolve against each state's own SLS directory,
  which matters for multi-file plans.
* Example state tree with top.sls under examples/tree/.

## 0.2.0 — 2026-08-09

P1 progress (see docs/salt-parity.md for the remaining items).

* file.managed / file.directory: `user`/`group` ownership with drift
  detection; -/+ line diffs in Changes (`show_diff: false` to suppress).
* Universal gates: `creates`, `unless`, `onlyif` now work on every state,
  evaluated by the engine (previously cmd.* only).
* New modules: user.present/absent, group.present/absent (pw(8),
  useradd/usermod, sysadminctl, net user), cron.present/absent
  (identifier-marker managed crontab entries), sysctl.present
  (runtime + sysctl.conf/sysctl.d persistence).
* `halite apply -json` for machine-readable results.
* Template funcs: default, contains, split, join, lower, upper,
  hasPrefix, hasSuffix.
* yamlite: double-quoted scalars now process \n/\t/\r escapes per YAML
  semantics; single quotes remain literal.
* Test-mode fix: file ownership referencing a user/group created by an
  earlier state no longer fails during `-test`.

## 0.1.0 — 2026-08-09

Initial release: masterless engine (`apply`, `call`, `grains`), yamlite
parser, text/template rendering, require/watch requisites, file/pkg/
service/cmd modules, cross-platform backends.
