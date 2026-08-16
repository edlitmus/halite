# Changelog

## Unreleased

Changed: **the control plane's default port is 5617, not 4506.** halite
took salt's port when it took salt's shape, and on a host running both
that is one socket with two owners. The failure it produces is not the
one an operator expects to debug: whichever daemon binds the wildcard
address answers every client that resolves anywhere else, so an agent
pointed at a name that resolves to loopback gets salt's ZeroMQ socket
and reports `tls: first record does not look like a TLS handshake` —
a TLS error for a problem that has nothing to do with TLS.

* 5617/tcp is unassigned by IANA and carries no conventional service,
  so nothing else expects it.
* `-addr` on the master and a `host:port` in the agent's `-master`
  still override it, which is the upgrade path for a fleet that cannot
  move both sides at once: pin `-addr :4506` on the master and keep the
  agents as they are.
* **Both sides must agree.** An agent from an older release defaults to
  4506 and will not find a master on 5617. Upgrade the master, then the
  agents, or pin the old port until both have moved.

## 0.11.0 — 2026-08-16

A small release with one subject: the two daemons learn to say how much
they say, and where. Everything a fleet has printed since the control
plane existed went to stderr unlabelled, which is exactly right under
`daemon(8)` or journald and no help at all on a host running neither.
Every existing message was classified rather than moved wholesale, the
default level prints what it always printed, and the line's shape did
not change — so an upgrade neither quiets a working host nor breaks
anything already reading these logs. The documentation audit that
followed is here too.

New: **the daemons take a log level and a log file.** Until now both
printed every line they had to stderr and nothing else, which is right
under `daemon(8)` or journald and no help at all on a host running
neither.

* `-log-level error|warn|info|debug` on `halite master` and
  `halite agent`, `info` by default — the lines the fleet has always
  printed, so an upgrade does not quiet a host that was working.
* Every existing message was classified rather than moved wholesale:
  `error` is work that was lost, `warn` is something refused or retried
  that the daemon carried on from, `info` is the fleet working. Nothing
  is ever silent — a daemon that says nothing when it drops a result
  would be worse than a noisy one.
* `debug` adds what was missing when an agent is not getting what it
  asks for: one line per authenticated request the control plane serves,
  and what each poll came back with.
* net/http's own errors are now `warn` rather than unlabelled. They are
  worth the level: the listener finishes the handshake for a caller that
  offers no certificate and turns it away in the handlers, so what
  reaches that sink is a caller that offered one and failed
  verification — a foreign or expired certificate being tried.
* `-log-file PATH` writes there instead, creating the directory,
  appending rather than truncating, at mode `0640`. Starting by hand
  still prints one line to stderr saying where the log went.
* **`SIGHUP` reopens it**, which is the handshake `newsyslog(8)` and
  logrotate expect; without it a rotated log keeps growing on a file
  nobody can find. Both are configured in
  [docs/service.md](docs/service.md#rotation). A daemon on stderr owns
  nothing to reopen, so `SIGHUP` there does nothing rather than failing.
* Both are settings in `master.conf` and `agent.conf` like every other
  flag, and the level token goes in front of the message rather than
  changing the line's shape, so anything already reading these logs
  keeps working.

Verified by starting a real control plane and agent, enrolling, running
jobs through them at each level, and rotating the file underneath the
running master with `mv` and `kill -HUP`. Not exercised: Windows, where
`SIGHUP` does not arrive and `-log-file` is the only way to keep a log.

Fixed, from auditing the documentation against the code afterwards:

* `docs/fleet.md` said both daemons shut down on SIGINT and SIGTERM and
  stopped there; SIGHUP now matters. It also gained a `Log` row beside
  the other ports-and-files defaults.
* `docs/pillar-security.md` said halite warns **on stderr** about a
  readable pillar tree — true for a one-shot command, no longer the whole
  story for a daemon with a log file. Its list of everywhere a pillar
  value can surface also had no entry for `-log-file`, which is a new
  on-disk destination; an agent logs the error *text* of a failed job,
  which can quote a template that did not render.
* `examples/README.md` never listed `master.conf` or `agent.conf` at all,
  though `docs/service.md` points readers at them. Long-standing, not new
  in this release.
* The `fleet mode` usage block, the README's two pointers to
  `docs/service.md`, and the README's `Status:` line, which still said
  v0.9.0 one release after the fact.

The pillar-tree warning lost its literal `warning: ` prefix, which read as
`WARN  warning: ...` once a level was in front of it. The one-shot command
that has no level to carry it adds the word itself.

## 0.10.0 — 2026-08-15

The release where a fleet stops having a one-year fuse. Agent
certificates renew themselves, a host that was switched off past its
expiry can get back in, and an operator can deny one before it expires —
none of which existed a release ago, when every agent enrolled on the
same day would have stopped connecting on the same day.

The rest is two security audits and the fixes they produced: the first
over the control plane, the CA, and the service files, the second over
the state modules, the external-module runner, and the parsers. Both are
summarised below with what was verified and how. Also here: config files
and init scripts for both daemons, ZFS states, and documentation that
`make check` now enforces rather than trusts.

New: **agents renew their own certificates.** An agent certificate is
good for a year, and until now nothing replaced it — a fleet enrolled on
one day stopped connecting exactly a year later, with no way back but
re-enrolling every host by hand.

* An agent watches its own expiry and, with 45 days left, asks
  `POST /v1/renew` for another year. No operator, nothing to enable.
* Renewal is authenticated by the certificate it replaces, and **cannot
  change the key**: the CA refuses a request for any key but the one on
  file, because changing keys is an enrollment somebody has to approve.
  There is no id in a renewal request — it comes from the certificate.
* The agent verifies what it is given before keeping it: the certificate
  has to parse, chain to the CA it trusts, carry its own name, and match
  the key on disk. `agent.crt` is then replaced atomically.
* A failed renewal is a log line and another attempt an hour later, not
  an exit — it starts weeks before anything breaks. The control plane
  raises `halite/key/<id>/renewed`.
* A certificate that has **already** expired cannot renew — there is no
  authenticated connection left to renew over — so the agent goes back to
  `/v1/enroll` with the key it already has, and the control plane reissues
  from the request in its store. No operator, no re-enrollment by hand for
  a host that was switched off too long.
* That reissue is deliberately narrow: it signs the CSR already on file
  (never the caller's), only when the stored certificate has expired, and
  a request for a different key is refused exactly as it is at any other
  time. Anyone else who tries receives a certificate for a private key
  they do not hold. It raises `halite/key/<id>/reissued`.
* `halite key remove <id>` is still how a host is taken out for good: it
  deletes the request, so the next enrollment waits for an operator.

New: **`halite key revoke <id>`** — denying a host without waiting for
its certificate to expire, the other half of the audit's certificate
findings.

* The identity is refused from the next request onward, on every
  authenticated route and on enrollment. The control plane reads the
  store per request, so a revocation lands on a running fleet without a
  restart.
* The certificate stays cryptographically valid: there is no CRL to
  distribute and no OCSP responder, and the only thing an agent
  certificate opens is this control plane, so the check belongs at the
  door. A revoked host can still prove who it is and can do nothing with
  it.
* Revoking moves the certificate and the request into `<pki>/revoked/`,
  so nothing is left to renew or reissue from, and a fresh enrollment is
  refused rather than filed — `key accept -all` cannot let a revoked host
  back in. `halite key remove` is still the way to let one start over,
  and it starts over needing an operator. `-all` is refused for `revoke`,
  because it collects the *pending* ids.
* A revoked agent keeps retrying, so the refusal is logged and raised as
  `halite/key/<id>/refused` at most once every five minutes per identity.
* Not covered, and documented as such: an open long poll finishes, and
  the host stays listed as online until its last contact goes stale.

Security fixes from an audit of the control plane, the CA, and the
service files. Nothing here needs a state tree change; upgrading the
binary and reinstalling the rc.d scripts is the whole migration.

* **The rc.d master script took ownership of `/var/run`.** `install -d -o
  ${halite_master_user}` applies its owner and mode whether or not it had
  to create the path, so pointing `halite_master_user` at an unprivileged
  account — the documented recommendation — handed that account every pid
  file and socket in `/var/run`, and with them a way back to root. Both
  scripts now use `/var/run/halite` and create it only when it is missing.
  **Reinstall `contrib/rc.d/halite_master` and `halite_agent`**, and check
  `ls -ld /var/run` on any host that ran the previous ones.
* **Enrollment answered `accepted` without checking the key.** `Submit`
  compared the CSR for a pending or rejected identity but not for an
  accepted one, so any caller who guessed an id received that agent's
  certificate from the one route that answers before authentication —
  a fleet-enumeration oracle. The comparison now covers every state.
* **A webhook returner had to be told to use https.** Results carry the
  run's changes, which hold whatever a state templated out of pillar. An
  `http://` endpoint is now refused unless it is on the loopback, and a
  redirect to another host fails the delivery instead of re-sending the
  record there.
* **`docs/pillar-security.md` promised more isolation than the code
  gives.** The control plane pins an agent's id from its certificate but
  renders pillar through the grains that agent reported, so a host that
  claims `role: db` receives whatever `role:db` selects. Pillar tops
  should target the id; the page now says so and explains why grain
  targets are fine in a state top and not in a pillar top.

New: **bounds on the one route that answers before authentication.**

* `-enroll-rate` (default 60 a minute) is a token bucket per source
  address on `/v1/enroll`, checked before the body is read, because
  verifying a CSR signature is the most expensive thing the control plane
  does for a stranger. The address comes from the connection and never
  from a header. Over the limit is `429` with a `Retry-After`, which
  agents already treat as "come back later", plus
  `halite/enroll/throttled` at most once every five minutes per source.
* A full bucket holds a minute's worth, so a fleet coming up together is
  not turned away for arriving at once, and a pending host — which
  retries every ten seconds — uses a tenth of it. Several behind one NAT
  gateway still fit.
* `-max-pending` (default 512) finally reaches `MaxPendingEnrollments`,
  which had been declared and read by the handler since the control plane
  landed, and assigned by nothing. Neither flag accepts zero: turning off
  a bound is not something to do by typing a number.
* A new check in `internal/docs` fails if any control plane setting is
  reachable from nowhere in `cmdMaster`, which is the mistake that hid
  this one. Settings computed from another are listed as such.

Security fixes from an audit of the state modules, the external-module
runner, and the parsers — the surface the first audit explicitly left
alone. Every finding below was reproduced before it was fixed.

* **Mode and ownership are no longer applied through a symlink.** `chmod`
  and `chown` follow one, so a path an unprivileged user could
  pre-create — a file under an app-owned directory — let a root state
  widen or take ownership of any file on the host. A state that sets only
  `mode`, `user`, or `group` on a symlink now fails naming the link;
  `follow_symlinks: true` is the opt-in. Writing *content* was already
  safe and is unguarded: the rename replaces the link and leaves its
  target alone. Covers `file.managed`, `file.directory`, `file.recurse`,
  the edit-style states, and `x509`.
* **`ssh_auth.present` refuses a symlinked `.ssh` or key file.**
  `MkdirAll` is satisfied by a link to an existing directory and `chown`
  follows it, so `ln -s /etc ~/.ssh` had root hand `/etc` to the account
  the key was being added for. That path belongs to the account by
  definition, so there is no opt-in here.
* **A group- or world-writable external module is refused**, as is one in
  a writable directory. `_modules/` programs run with the agent's
  privileges and the state tree's permission bits survive to an agent's
  cache, so a single `chmod 777` in the tree meant local root on every
  managed host.
* **`pkgrepo.managed` honours `show_diff`.** A repository URL routinely
  carries a token, and the diff went to the control plane, the returners,
  and the event bus with no way to suppress it — while
  `pillar-security.md` names `show_diff` as the mitigation.
* **The mine's trust boundary is documented.** Agent *names* are
  authenticated from the publishing certificate; the values are claims
  from another host. `docs/events.md` now says which uses are sound.

Clean, and worth recording: every module runs commands as argv rather
than through a shell (`cmd.run` and the `unless`/`onlyif` gates are the
deliberate exceptions), `halite ssh` quotes every interpolated value,
`archive.extracted` refuses a remote source without a `source_hash`,
private keys are written 0600 before the rename, no state puts a secret
on a command line, and the SLS parser took 5000-deep nesting and 200k
keys without a crash.

New: config files and FreeBSD rc.d scripts for both daemons, so running
halite at boot does not mean keeping a command line in `rc.conf`.

* `halite master` and `halite agent` read
  `/usr/local/etc/halite/{master,agent}.conf` (or `/etc/halite/...` on
  Linux), or the file `-config` names. Every setting is a flag without its
  dash, and a repeatable flag is a list. A missing file is not an error:
  running entirely on flags keeps working.
* Precedence is **flag, then environment, then file, then platform
  default** — the most specific thing somebody typed wins. The environment
  only outranks the file for the four settings that have a variable at
  all (`$HALITE_ROOT`, `$HALITE_PILLAR_ROOT`, `$HALITE_PKI`,
  `$HALITE_MASTER`).
* A setting the daemon does not have is an error naming it, not a warning.
  A config file that quietly did nothing would surface as a fleet behaving
  oddly rather than as a daemon saying why.
* The rc.d scripts name the binary `halite_{master,agent}_binary`, **not**
  `_program`: rc.subr reserves `${name}_program` and uses it to replace
  `$command`, so that spelling made it run halite with `daemon(8)`'s
  arguments (`unknown command "-S"`). For the same reason the scripts
  move `${name}_flags` aside before `run_rc_command`, since rc.subr
  splices it in as `$command $rc_flags $command_args` — daemon's flag
  position, not halite's. Two checks in `internal/docs` now fail on
  either mistake.
* `contrib/rc.d/halite_master` and `halite_agent` wrap the daemon in
  `daemon(8)` with `-S`, so output goes to syslog. Neither supervises by
  default — FreeBSD's rc does not, and adding it silently would surprise
  whoever reads the script — but `halite_*_daemon_args="-r"` asks for it.
  Sample configs are in `examples/`, and `docs/service.md` documents every
  sysrc variable.
* `contrib/systemd/halite-master.service` and `halite-agent.service` do
  the same on Linux. The control plane runs as an unprivileged `halite`
  account under a read-only filesystem, with `/etc/halite/pki` and
  `LogsDirectory=` writable; the agent runs as root with **no** sandbox,
  because a restriction there surfaces as a highstate failing halfway
  through. `HALITE_MASTER` comes from an optional
  `/etc/halite/agent.env`, the systemd counterpart of
  `halite_agent_master`. Not verified against systemd itself — this was
  developed on FreeBSD, and `systemd-analyze verify` runs only where
  there is one.
* Three more checks in `internal/docs`: every sysrc variable a script sets
  is documented, both scripts parse under `sh -n` and are executable, and
  every setting in the sample configs is a real flag of that daemon. Each
  was verified by breaking what it catches.
* Three more again for the units: every flag in an `ExecStart` is a real
  flag of that daemon and its `-config` is the path the daemon would have
  read anyway, each unit is INI-shaped with a `Description`, `ExecStart`,
  and `WantedBy`, and every account and file path a unit names is
  documented in `docs/service.md`.

New: ZFS states — `zfs.filesystem_present`, `zfs.filesystem_absent`,
`zfs.snapshot_present`, `zfs.snapshot_absent`. They carry Salt's names,
because Salt has the same states, and they pair with the jails: a jail's
filesystem is a dataset, and a snapshot is what makes an upgrade
reversible.

* Properties are compared and set on a dataset that already exists, so
  the state owns them rather than applying them once at creation.
* **Sizes are compared as sizes.** A state asking for `quota: 10G` does
  not fight zfs over whether it reports `10G`, `10.0G`, or the byte count
  back — a text comparison would set the property, and report a change, on
  every run forever. `none` and `0` are the same absence of a limit.
* `zfs.filesystem_absent` refuses a dataset that has snapshots or children
  unless `recursive: true` says to take them too. `zfs destroy -r` is the
  most expensive typo in the module.
* Verified against zfs itself: `zfs create -n` validates the dataset name
  and every property without creating anything, and that is now a test
  which runs wherever zfs exists. Drift detection was checked against this
  host's real pools under `-test` — an existing dataset with a matching
  property reports no change, and a differing one reports
  `compression lz4 -> zstd, quota none -> 10G` without touching it.

Documentation is now part of the definition of done rather than a habit.
`internal/docs` holds no code — only checks that read the tree from disk
and hold this repository's prose to it:

* every state function appears in `docs/states.md`, and every heading
  there names a state that exists;
* every CLI command and flag is documented in the README or `docs/` — the
  changelog does not count, since a flag mentioned only in a release note
  is one nobody can look up;
* every internal markdown link resolves, and `docs/architecture.md` lists
  every package;
* every example compiles, checked under FreeBSD, Linux, and Windows grains
  so a platform guard cannot hide one from the check;
* no state function is written without the colon that makes it a mapping
  key — the mistake that reads correctly and does not parse;
* the counts quoted in the README and the parity map match the registry.

They run inside `make test`, so `make check` — what the README calls the
definition of done — now fails on a change that leaves a doc behind.
`make docs` runs them alone while editing prose. Each check was verified
by breaking the thing it exists to catch.

Fixed while writing them: `docs/states.md` abbreviated one combined
heading (`### alternatives.install / remove / set`), which named neither
`alternatives.remove` nor `alternatives.set` in a form anybody could grep
for.

Known and not fixed, carried forward from the audits: `halite key gen`
overwrites an existing `agent.key` instead of refusing like the CA store
does; pillar loader errors reach the agent with server-side paths in
them; and archive extraction still preserves a world-writable mode from
the state tree — only *running* a module in that state is refused.

## 0.9.0 — 2026-08-14

P6 continues, and reaches the two things a FreeBSD fleet runs workloads
in: jails, which Salt has no states for, and OCI containers, which it
spells differently. Plus x509, so a host's TLS material is a state rather
than a script.

New: FreeBSD jail states — `jail.present`, `jail.absent`, `jail.running`,
`jail.stopped`. Containers are where halite and Salt diverge rather than
lag: Salt has no jail states, so nothing here ports from a Salt tree, and
nothing has to be translated either.

* The split matches `file.managed` + `service.running`: `jail.present`
  writes the configuration, `jail.running` starts it, and a `watch`
  between them restarts a jail whose block changed. `jail.running` and
  `jail.stopped` go through `service jail start|stop`, so a jail halite
  starts is started exactly as the host starts it at boot — including
  which configuration file rc.d/jail decides to read.
* The file is `/etc/jail.conf.d/<name>.conf`, where rc.d/jail looks for a
  named jail, leaving `/etc/jail.conf` and its global settings alone.
  Parameters are written in a fixed order: a block that reordered itself
  would report a change on every run.
* `params` takes any jail parameter. `true` writes the bare flag form, a
  list writes a comma-separated value, anything else is quoted, and an
  empty value drops the parameter — which is how the three defaults
  (`exec.start`, `exec.stop`, `mount.devfs`) are overridden. There is no
  translation for switching a boolean *off*: jail.conf spells that by
  prefixing the last component with `no`, and guessing where that prefix
  goes is how a state writes a file that means something else.
* `jail.absent` stops the jail, removes the file, and takes it out of
  `jail_list` — and leaves the filesystem alone. A jail root is somebody's
  data.
* Verified against jail(8) itself: `jail -f <file> -e ';'` parses what
  halite writes and reports back the parameters intended, and that check
  is now a test which runs wherever jail(8) exists.
* `modules.Map` reads a nested mapping argument, which arrives as the
  parser's own `*yamlite.Map` rather than a Go map. `halite show` and the
  external-module JSON flatten the same way — both would otherwise have
  printed `&{[keys] map[...]}` at whoever was reading.

New: OCI container states — `container.image_present`,
`container.image_absent`, `container.running`, `container.stopped`,
`container.absent` — driving `docker` or `podman`, whichever the host has.
They take the same subcommands for everything used here, so one backend
covers both: podman on the FreeBSD hosts halite targets, docker on the
Linux ones.

* They carry halite's names rather than Salt's, because `docker_container`
  is the wrong word for something driving podman. `halite parse` reports
  a Salt tree's `docker_container.running` with the name to rename it to.
* **Drift is one comparison.** The arguments are hashed into a
  `halite.spec` label at creation, and each run compares that against the
  hash of what the state says now. A port, an environment value, the
  command, the resolved image id — anything that differs recreates the
  container, including arguments this module grows later. A container
  carrying no such label was made by something else, and says so before
  being replaced.
* A changed container is replaced rather than adjusted: the runtimes
  cannot change most of these on a live container, and a half-applied one
  would be worse. `watch` restarts without recreating.
* The runtime's own parser checks the command line halite builds, in a
  test that runs wherever docker or podman exists. It uses an image
  reference that cannot resolve, so the run fails after the flags are
  parsed and before anything is created — a test must not leave a
  container behind.

New: `x509.private_key_managed` and `x509.certificate_managed`. An
internal CA and the certificates under it, from `crypto/x509` — the same
standard library the fleet CA uses, so there is no openssl to install and
what the states write is what openssl reads.

* Keys are `ec` (P-256) or `rsa`, written 0600. An existing key of the
  right kind is left alone: rotating one invalidates every certificate
  signed from it, so it takes `new: true` or a changed `algo`/`bits`. A
  key found group-readable is chmodded back without being rotated.
* Certificates are self-signed, or signed by a CA the state names with
  `signing_private_key` and `signing_cert`. `ca: true` issues a signing
  certificate rather than a serving one, which is the other half of
  running an internal CA from a state file.
* A certificate is reissued when it is missing, inside the
  `days_remaining` window (28 days by default), no longer matches its
  private key, or its common name, alternative names, or `ca` flag differ
  from the state. That window is what makes a converging fleet renew
  itself. Shortening `days_valid` does not reissue a certificate that is
  still outside the window: that would be churn, not convergence.
* A server certificate with no `subject_alt_names` gets its common name as
  one, because nothing modern accepts a certificate without.
* Verified end to end: `openssl verify -CAfile ca.crt site.crt` returns
  OK for what `examples/tls.sls` produces.

Fixed: `halite parse` was blind to the arguments of twenty state modules —
every file-editing state, `host`, `kmod`, `timezone`, `locale`, `selinux`,
and `alternatives`. Their argument tables had never landed, so a typo in
one of their arguments was accepted in silence. The tables are there now,
and two tests keep the checker and the module registry from drifting apart
again: every registered state must have a table, and every table must name
a registered state.

Documentation and examples, audited against the code rather than read
over.

* Six new example files, and every one of them compiles — `halite parse`
  reports no errors and `halite show` prints its plan. `sshd.sls` edits a
  config the OS package owns, `identity.sls` sets what a host has one of,
  `pyapp.sls` deploys a Python application, `repo.sls` pins a package to a
  third-party repository, `provisioning.sls` fetches content and schedules
  work, and `linux-hardening.sls` and `windows.sls` show platform-guarded
  states. `examples/README.md` indexes them.
* The example tree now shows the language features it did not: `names:`
  expansion, a `watch_in` reaching a state another file declares, compound
  targeting, and a static grains file for the grain that targeting needs.
* Doc examples that wrote a state function bare (`timezone.system` with no
  colon) do not parse, and now say so correctly. Found by running them.
* `host.present` no longer reads its own `names` argument: `names:` is the
  state compiler's expansion, as it is in Salt, and the module sees one
  name per call. The result in `/etc/hosts` is the same line either way,
  which is what the test now pins.
* `halite parse` recognises `halite.run`, so an orchestration file no
  longer reports three unsupported modules.
* Flags that existed but were undocumented: `-poll-timeout`,
  `-orch-timeout`, `-retry`, `-dist`, and `key admin -out`. The
  architecture layout gained `internal/schedule` and `internal/compat`,
  and its pipeline now mentions where `show` and `parse` stop.

## 0.8.0 — 2026-08-14

P6: module breadth and legibility. Twenty-six state functions, taking the
set from 27 to 53, and `halite show` — which answers "what does this tree
actually compile to" without running any of it.

New: `halite show` prints the compiled plan without running any of it —
Salt's `state.show_sls` and `state.show_highstate`.

* It takes the same targets `apply` does (nothing for a highstate, a file
  path, or dotted SLS names) and prints the states in the order they would
  run, with their arguments, requisites, and the file each came from.
  `-json` for a script.
* It is not `apply -test`. A dry run calls every module to ask what it
  would change, which reads the host and takes as long as the run does;
  `show` stops after the compile. It answers the question you have when a
  highstate does something surprising: what did my templates, includes,
  `_in` requisites, and `names:` expansions actually produce, and in what
  order?
* `apply` and `show` now share one target-to-plan resolution, so a file
  path, a dotted name, and a highstate mean the same thing to both.

New: the file states that edit part of a file rather than owning all of
it. `file.managed` is the wrong tool for a file something else also
writes to, and until now there was no right one.

* `file.symlink` — links are repointed when they aim somewhere else; a
  real file or directory in the way fails the state unless `force: true`,
  because deleting something that was not a link is not a thing a run
  should do on its own. Ownership applies to the link, not its target.
  Not implemented on Windows, where symlinks need a privilege most
  services do not hold.
* `file.copy` — copies a file already on the host, with `preserve` for its
  ownership. The source is a host path; `file.managed` is the one that
  reads from the state tree.
* `file.append` / `file.prepend` — ensure lines are present, adding only
  what is missing. A line already somewhere in the file stays where it is.
* `file.line` — `ensure` (present exactly once, replacing matches and
  dropping duplicates), `replace`, `delete`, and `insert`, with `before`,
  `after`, and `location` anchors. `match` is a substring, as Salt's is;
  `file.replace` is the regular-expression state. Salt spells this state's
  action `mode`, which is permission bits everywhere else in halite, so
  `file.line` takes the Salt meaning and leaves the file's permissions
  alone.
* `file.replace` — Go regexp with `$1` expansion, `count`,
  `append_if_not_found` / `prepend_if_not_found`, and `ignore_if_missing`.
  `^` and `$` match at line boundaries: Salt's file.replace defaults to
  MULTILINE and nearly every pattern written for it anchors a line, so a
  ported `^#?PermitRootLogin` has to match the second line of the file
  rather than silently matching nothing.
* `file.blockreplace` — owns the text between two markers and leaves the
  rest of the file alone. A `marker_start` with no `marker_end` after it
  fails rather than guessing where the block ends. Multi-line bodies come
  from `source`, since the YAML subset has no block scalars.
* `file.comment` / `file.uncomment` — by regular expression, skipping
  lines that are already commented so a second run is a no-op.
* All of them share one edit path: read, transform, write atomically,
  report a line diff. It keeps the file's existing permissions **and its
  ownership** — an atomic write renames a file this process created, so
  without restoring the owner, editing one line of a user's dotfile as
  root would have handed the file to root.
* `halite parse` knows the new states and their arguments.

New: the system states — the settings a host has one of.

* `host.present` / `host.absent` manage `/etc/hosts`. Names for one
  address land on one line, comments and unrelated entries survive
  verbatim, and a line left with no names goes. `clean` also takes a name
  off other addresses, which is not the default because removing an entry
  somebody else put there is destructive.
* `kmod.present` / `kmod.absent` load and unload kernel modules —
  `modprobe` on Linux, `kldload` on FreeBSD — and with `persist` add or
  remove one line in `/etc/modules-load.d/halite.conf` or
  `/boot/loader.conf`, leaving the rest of a file that belongs to the host.
  Module names fold dashes to underscores, the two spellings of the same
  module.
* `timezone.system` sets the zone through `timedatectl` where it exists,
  and otherwise installs the zoneinfo file and records the name. A zone
  with no file under `/usr/share/zoneinfo` fails the state: a typo that
  quietly left the host on UTC would be worse.
* `locale.system` sets `LANG` (or another key) through `localectl`,
  `/etc/default/locale`, or `/etc/locale.conf`, keeping the other keys.
  Linux only — FreeBSD has no single system locale, and writing a file
  nothing reads would be a lie.
* `alternatives.install` / `remove` / `set` drive `update-alternatives`
  (or `alternatives`). Setting a path the group does not offer fails
  rather than installing it: choosing is a different intent from adding.

New: the boot-configuration states.

* `service.enabled` / `service.disabled` set whether a service starts at
  boot without starting or stopping it now — the case `service.running`
  with `enable: true` cannot express. A backend that cannot report
  enablement (launchd, sysvinit) fails the state rather than acting
  blindly: without a probe every run would report a change, and being
  idempotent about boot configuration is the point.
* `network.system` sets the hostname, applying it now *and* recording it
  for the next boot (`hostnamectl`, or `hostname` plus `sysrc`, `scutil`,
  or `/etc/hostname`). It reports drift when the running name and the
  recorded one disagree, because a hostname that reverts on the next
  reboot is the failure the state exists to prevent. The rest of Salt's
  network.system — the RHEL-era /etc/sysconfig/network switches — is not
  implemented: interface configuration is a stated non-goal, and half a
  state would be worse than none.

New: the Python states. `pip.installed`, `pip.removed`, and
`virtualenv.managed` are what a Salt tree deploying a Python application
reaches for, and had no equivalent.

* `bin_env` names a virtualenv (or a pip inside one), so a state installs
  into an application's environment rather than the system's.
  `virtualenv.managed` creates the environment with `python3 -m venv` and
  hands its requirements to the same code path.
* An exact `==` pin is compared against `pip freeze`, so a downgrade is a
  change like any other. Anything looser is left for pip to judge:
  reimplementing PEP 440 to second-guess it would be worse than asking.
  Names fold the way pip folds them, so `zope.interface` and
  `zope_interface` are one package.
* A `requirements` file is pip's to read. The state runs pip and reports
  the difference between the freeze before and after, which is also how
  the transitive installs a requirement pulled in get reported.

New: `selinux.mode` and `selinux.boolean`, for the RHEL fleets where
every other state fails until SELinux is set right.

* The running mode and the configured one are set together: they can
  differ, and changing only one would report success for a host that
  reverts on reboot. Crossing `disabled` cannot take effect at run time,
  so the state writes the configuration and says a reboot is needed
  instead of pretending. `SELINUXTYPE` and the comments in
  `/etc/selinux/config` survive.
* `selinux.boolean` persists by default, since `setsebool` without `-P` is
  lost on the next reboot — rarely what a state file means.

## 0.7.0 — 2026-08-13

P5: the five gaps that blocked a real Salt migration, closed in the order
they block one. Plus `halite parse`, which reads an existing Salt tree and
says what halite can do with it, and a full-repo review pass.

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

New: the `_in` requisites and `- names:` expansion — the two pieces of the
SLS dialect most existing Salt trees will not compile without.

* `require_in`, `watch_in`, `onchanges_in`, and `prereq_in` are resolved
  onto the states they name before ordering, so the result is exactly what
  writing the plain requisite on the other state would have produced. It
  is the only way to attach a requisite to a state another SLS file
  declares, which is why Salt trees lean on it. An `_in` naming a state no
  loaded file declares is a compile error, not a silent no-op.
* `names:` declares the same state once per name. Each expansion gets its
  own arguments with `name` set, and an id of `install_tools (vim)` so the
  output says which one did what. A requisite pointing at the declaration
  reaches every expansion: it runs after all of them, and a `watch` fires
  if any of them changed.
* `halite parse` no longer reports either as unsupported.

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
  ignore, with the ones that change the result (`file.managed:
  source_hash`, `cmd.run: runas`) raised to errors; `salt://` and remote
  sources; `template: jinja`; unresolvable includes and top-file SLS
  names; targets that do not parse; and Salt's Python extension
  directories.
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
