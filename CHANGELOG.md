# Changelog

## Unreleased

New: an agent-side scheduler. A halite fleet converged only when something
poked the control plane, so a host that drifted at 02:00 stayed drifted
until somebody noticed.

* `halite agent -schedule FILE` runs highstates, `apply`, or `call` on an
  interval, with `splay`, `test`, and `at_start`. The splay delays the run
  rather than the tick, so the period stays what the config says while a
  fleet spreads out inside it — two hundred hosts pulling the state tree in
  the same second is a thundering herd.
* A scheduled run uses the same loader, engine, and modules as a dispatched
  one. It answers no dispatched job, so it is announced on the bus as
  `halite/schedule/<agent>/<name>` rather than returned — which means the
  reactor can act on it. A nightly `test: true` highstate reporting
  `changed > 0` is a drift alarm.
* Agents may now raise `halite/schedule/<their-own-id>/<name>` alongside
  their beacon tags, under the same rule: an agent speaks only for itself.
* A job that could never run — no interval, an unknown kind, an `apply`
  with no `sls`, a splay longer than the interval — is refused when the
  file is read, not discovered at 02:00. A missing schedule file is not an
  error.

New: compound targeting. One target language still serves top files,
`halite run`, `halite ssh`, the mine, and the reactor — it just says more.

* `and`, `or`, `not`, and parentheses combine patterns:
  `web* and not L@web9`, `(db* or cache*) and os_family:FreeBSD`. "The web
  hosts, except the one being rebuilt" had no single-pattern spelling.
* Salt's matcher prefixes are read where they mean something halite can
  do: `G@grain:glob`, `L@id,id`, `E@regex` on the id, `P@grain:regex`.
  `I@` (pillar), `S@` (subnet), `N@` (nodegroup) and `R@` (range) are not
  implemented.
* A target that does not parse is now an error where it is written — in a
  top file, in `halite parse`, and at dispatch on the control plane —
  rather than a silent non-match that looks like an empty fleet. Two
  patterns side by side are an error too, not an implied `and`.
* A grain holding a list matches when any entry does, so `roles:web`
  selects a host whose `roles` are `[web, cache]`.
* `halite parse` checks targets by asking the matcher itself, so its
  report tracks the target language instead of a copy of it.

New: the module set a real server fleet needs.

* `file.recurse` copies a directory from the state tree onto the host,
  with `file_mode`, `dir_mode`, ownership, `template: true`, and an
  optional `clean` that removes what the source does not have. Content,
  ownership, and modes are checked separately, so a byte-identical tree
  still reports drift when its permissions moved. The changes report caps
  at ten paths per category so a first run does not bury the output.
* `ssh_auth.present` / `ssh_auth.absent` manage one `authorized_keys`
  entry. The key body identifies it, so changing options, type, or comment
  rewrites that line instead of adding a second copy of the same key. The
  `.ssh` directory is created 0700 and the file 0600, owned by the user —
  sshd ignores them otherwise.
* `pkg.installed` takes `version` and `hold`. The version is compared
  against the installed one, so a downgrade is a change like any other,
  and `hold` drives the package manager's own lock (`pkg lock`,
  `apt-mark hold`, `versionlock`, `zypper addlock`, `brew pin`,
  `choco pin`). A pin the backend cannot express fails the state rather
  than installing what is current — that would be the wrong package,
  quietly.
* `pkgrepo.managed` / `pkgrepo.absent` write a repository definition for
  pkg(8), apt, dnf/yum, zypper, or apk, and refresh the metadata when the
  file changes. halite does not fetch signing keys: `signed_by`/`gpgkey`
  point at a key a `file.managed` put there first.
* `halite parse` knows the new modules and arguments, so a Salt tree using
  them no longer reports them as unsupported.

New: static custom grains. halite detects a fixed set of facts, so until
now `role:web` targeting — which the documentation's own examples use —
had no way to be fed.

* A static grains file (`/usr/local/etc/halite/grains` on FreeBSD,
  `/etc/halite/grains` on Linux, `$HALITE_GRAINS` or `-file` to override)
  is plain YAML, merged over the detected grains. Custom grains win: a
  site that sets `os_family` by hand means it. A file that does not parse
  is reported and skipped, because a typo in it must not stop a host from
  converging.
* `halite grains set role=web` and `halite grains unset role` write it —
  Salt's `grains.setval`. The file is ordinary YAML, so the fleet-wide way
  to set a grain is `file.managed` on the grains file.
* `yamlite.Plain` converts a parsed tree to plain Go maps; pillar's private
  copy of it is gone.

New: `halite parse` reads an existing Salt state and pillar tree and
reports what halite can use as written.

* It defaults to Salt's own `/srv/salt` with a pillar tree beside it,
  takes any other root with `-root`/`-pillar-root` or a single file as an
  argument, and changes nothing: each SLS file goes through the same
  render, parse, and compile the engine uses, and the report says where
  that stops. Findings carry a severity — an error is something halite
  will not run as written, a warning is something it loads and ignores, a
  note is a supported construct with a caveat — and each one carries the
  translation it needs. `-json` for scripting, `-errors` to see only
  blockers, and a non-zero exit while any error remains, so it works as a
  CI gate during a conversion.
* What it reports: Jinja statements, comments, expressions and filters
  against Go `text/template`; renderer shebangs including `#!py` and GPG
  pillar; the YAML features the parser leaves out (block scalars,
  anchors, aliases, merge keys, flow collections, multiple documents,
  tags, tabs); Salt's undotted `pkg:`/`- installed` declaration; state
  modules and requisites halite does not implement; arguments it would
  ignore, with the ones that change the result (`pkg.installed: version`,
  `file.managed: source_hash`) raised to errors; `salt://` and remote
  sources; `template: jinja`; unresolvable includes and top-file SLS
  names; compound targets; and Salt's Python extension directories.
* A file that does not render is read again with its template constructs
  stripped, so the report still holds its state inventory. Those findings
  are marked approximate, because what a Jinja loop would have expanded
  to is not knowable without Jinja.
* Files are rendered with the grains of the host `parse` runs on and the
  pillar halite itself can read, so a tree that already works comes back
  clean — the example tree is a test of exactly that.
* `yamlite.StripComment` and `yamlite.SplitKV` are exported so the
  checker reads a line the same way the parser does.
* See docs/migration.md for every finding code and its translation.

Fixes from a full-repo review. Correctness:

* Grain targeting (`key:pattern`) no longer matches hosts that do not
  have the grain at all — `'role:*'` used to select the entire fleet,
  including its pillar, because a missing grain stringified to `<nil>`
  and matched the glob.
* A failed `prereq` state now blocks its target, matching Salt: if
  draining the load balancer fails, the deploy no longer proceeds.
* `halite ssh <hosts> call ... -test` was silently mutating: the flag was
  dropped for the `call` kind. It is now forwarded, and `halite call`
  itself accepts `-test`.
* A pillar file's own keys now deep-merge over its includes instead of
  replacing their subtrees; overriding one leaf keeps included siblings.
* Event and job IDs are strictly increasing (timestamp + sequence) —
  they were timestamp + random tail, so same-microsecond events sorted
  randomly and `halite events` history could disagree with ID order.
* `mount.mounted` no longer fails forever on `UUID=`/`LABEL=` device
  specs (the kernel reports the resolved device); `service.running` with
  `enable: true` probes enablement instead of re-enabling and reporting a
  change every run; `sysctl.present` compares multi-value sysctls with
  normalized whitespace; `file.directory` enforces `mode` on existing
  directories and survives umask on creation; `file.absent` removes
  dangling symlinks.
* yamlite: `''` now escapes a quote inside single-quoted scalars, an
  apostrophe in a plain scalar no longer swallows a trailing comment,
  and non-empty flow collections and duplicate keys are parse errors
  instead of silent misparses.
* `/etc/fstab`, sysctl conf files, and managed file content are written
  via temp-file-and-rename, so a crash mid-write cannot truncate them.
* Fleet `call` reaches external modules in `_modules/`, as local
  `halite apply`/`call` already did.

Hardening and operations:

* The control plane bounds its job and orchestration records (oldest
  evicted) instead of growing until OOM under a busy reactor, and the
  mine stops serving facts for hosts that have not checked in for an
  hour — a decommissioned backend no longer feeds its address into other
  hosts' templates forever.
* Enrollment refuses new identities over a pending-request cap (503,
  default 512) so an unauthenticated client cannot fill the disk, and
  bounds its body read so it cannot hold connections open. Agent-posted
  event timestamps are server-stamped.
* Each returner gets its own queue and goroutine — a black-holed webhook
  no longer starves the file returner — the file returner fsyncs, the
  webhook retries transient failures, and shutdown drains the queues
  before the process exits.
* `halite ssh` refuses roster entries that parse as ssh options
  (`-oProxyCommand=...`), passes `--` before destinations, strips
  terminal escape sequences from remote output, and keeps the `-json`
  fleet report valid when one host emits garbage. `git.latest` refuses
  option-shaped `name`/`rev`/`depth` values.
* Reactor rate limiting is per rule, so one looping rule cannot starve
  the others. Beacon check errors no longer flip the alert edge: a
  transient `systemctl` failure is not a stop plus a phantom recovery.
  External module output is capped at 8 MiB per stream.
* An agent that lost its CSR sidecar rebuilds it from the key it holds
  instead of silently rolling a new identity.

Docs: salt-parity.md gains a "Known gaps" section — the honest list of
what a Salt 3008 operator will reach for and not find (custom grains,
compound targeting, `_in` requisites, file.recurse, a scheduler, eauth,
and more), with a priority-ordered sketch of a P5. `halite parse` reports
those same gaps against a specific tree; docs/migration.md is the
translation table.

## 0.6.0 — 2026-08-09

P4 (long tail) complete, and with it the roadmap.

* External modules: an executable in the state tree's `_modules/`
  directory provides `<name>.*` state functions. halite runs it with the
  function as its argument, writes a JSON request on stdin, and reads a
  JSON result from stdout. The directory ships to agents with the tree,
  executable bit intact, so a module works masterless, under a control
  plane, and over `halite ssh`. Built-ins always win, so a module cannot
  shadow `file.managed`. A module that exits 0 without writing a result
  fails rather than silently skipping the state. See
  docs/external-modules.md.
* Multi-master: `halite agent -master a,b,c` uses one control plane at a
  time and tries the list from the top on every reconnection, so it
  prefers the first and returns to it once it is back. Masters sharing a
  CA need no re-enrolment on failover. This is failover, not a cluster —
  masters share no state, so an operator commands the master its agents
  are connected to (ADR-11).
* The agent's control plane connection is now behind a lock, since
  beacons and the mine publish through it from their own goroutines while
  a failover may be replacing it.
* Windows registry: `reg.present` and `reg.absent` drive `reg.exe`.
  DWORDs and QWORDs are compared numerically, because `reg query` prints
  them in hex and a text comparison would report a change on every run.
  Removing a whole key needs `delete_key: true`. As with scheduled tasks,
  the parsing and comparison are unit tested but the `reg.exe` calls have
  not been run on a real Windows host.

### Tooling

* `make race` runs the suite under the race detector, and `make check`
  runs vet, tests, and race — what to run before calling a change done.
  Kept separate from `make test` because the detector is slower and is
  not available on every platform Go builds for.

### Documentation

* getting-started now points at the other two ways to run — agentless and
  fleet — instead of ending at masterless.
* writing-states covers `{{ .Mine }}` and external modules.
* Corrected claims that had gone stale as features landed: the event bus
  and returners are no longer "P3" in fleet.md, Windows scheduled tasks
  are no longer "planned" in states.md, and ADR-1/ADR-4/ADR-5 describe
  what happened rather than what was expected.
* **Corrected a promise that was never kept**: `pkg.installed` version
  pinning and alternate repositories were listed as landing in P1 and
  never implemented. Both states.md and the parity table now say so
  plainly instead.

## 0.5.0 — 2026-08-09

P3 (events) complete.

* Event bus: the control plane publishes tagged events for enrollment,
  agent hellos, job dispatches, and returns; agents post their own events
  upward, with the source taken from their certificate so nobody can speak
  for another host. Tags are slash-delimited with `*` inside a segment and
  `**` across segments.
* `GET /v1/events` streams newline-delimited JSON, flushed per event and
  held open, with `?tag=` filtering and `?history=N` replay. Operator
  certificates only — an agent may raise events but not read the fleet's.
* `halite events [-tag PATTERN] [-history N] [-json]` tails it.
* A subscriber that stops reading loses events rather than blocking the
  control plane; the loss is counted.
* Returners: `halite master -returner file:PATH` and
  `-returner webhook:URL` (repeatable) write every finished job result
  somewhere durable, since the bus deliberately is not. Records carry the
  job and the full result. Delivery is queued, so a slow sink delays only
  itself; a full queue drops and logs. The file sink is mode 0600 because
  results can contain diffs, and webhook URLs are redacted in logs.
* Reactor: `halite master -reactor FILE` loads rules mapping tag patterns
  to jobs, with the event's fields available as templates
  (`{{ .Source }}`, `{{ .Data.x }}`). A template referencing absent data
  fails the rule rather than dispatching a blank target. Reacted work is
  marked in both its dispatch and its return events so the reactor cannot
  feed itself, and reactions are rate limited to 60/min as a backstop.
* Beacons: `halite agent -beacons FILE` runs agent-side watchers that
  raise events — `disk` (usage threshold), `service` (stopped), and `file`
  (created, changed, or removed, compared by digest). Beacons are edge
  triggered: a condition that stays true fires once, not once per check,
  so a reactor rule keyed on a full disk cannot dispatch a job a minute. A
  beacon that panics is logged without taking the agent down.
* The control plane now constrains the tags an agent may raise to
  `halite/beacon/<its-own-id>/<name>`. Event sources were already taken
  from the certificate, but reactor rules match on the tag, so a forged
  tag could fire a rule written for another host.
* yamlite: list items may now carry several keys (`- mount: /var` followed
  by an indented `threshold: "90"`), which is ordinary YAML the parser was
  rejecting. Single-pair items, the SLS argument convention, are
  unaffected.
* Mine: `halite agent -mine FILE` publishes execution-module output and
  grains to the control plane on a schedule; states read the fleet's facts
  as `{{ .Mine.<function>.<agent> }}` and operators with `halite mine`.
  Function names are validated at startup, so a typo fails loudly instead
  of never publishing. Entries are keyed by the certificate identity and
  carry the time they were published.
* Orchestration: `halite orchestrate <name>` runs ordered fleet-wide
  steps from `<orch-root>/<name>.sls`. Steps are `halite.run` states, so
  requisite ordering, failure gating, and the universal gates are the
  state engine's own code rather than a second implementation (ADR-10).
  A step waits for every agent it matched; a step matching none fails
  rather than silently succeeding. Runs happen on the control plane,
  detached from the request, with progress on the event bus.
  See docs/orchestration.md.
* engine.RunWith lets a caller supply how state functions are resolved,
  which is what makes the above possible.
* cron on Windows: `cron.present` and `cron.absent` now drive `schtasks`
  under a `\halite\` folder instead of failing. Cron fields that the
  task scheduler cannot express — months, lists, ranges, both daymonth
  and dayweek — are refused by name rather than approximated. The
  translation is unit tested; the schtasks calls themselves have not been
  run on a real Windows host.
* See docs/events.md and docs/orchestration.md.

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
