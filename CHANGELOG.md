# Changelog

## 0.4.0 — 2026-08-09

First P2 (transport) increment: pillar.

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
* `sls.ResolveName` and `sls.MatchTop` are exported so the pillar tree
  reuses the state tree's name resolution and targeting verbatim.
* Example pillar tree under examples/pillar/, wired into examples/tree.

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
