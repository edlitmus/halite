# Divergence and gaps

What halite does that SPEC.md does not say, what SPEC.md says that halite does
not yet do, and why in each case.

This file is checked mechanically. `internal/specaudit` parses the module
tables in SPEC.md sections 15.2 through 15.5, compares them against the
registries an actual build ships, and fails if a module is neither
implemented nor recorded here, or if a gap recorded here has since been
filled. A stale entry below is a test failure, not a documentation problem.

**Status as of this writing:** SPEC section 32 phases 0 and 1 are complete.
Phases 2 through 6 have not been started. The development and verification
host is FreeBSD 15.1 on amd64, and that is the only platform on which any of
this has been run.

---

## 1. Deliberate divergences from SPEC.md

These are places where the implementation does not match the specification
text and the implementation is believed to be right. Each needs either a
spec amendment or a reversal.

### 1.1 The single-letter YAML 1.1 booleans

**Spec:** Section 10.1.3's table lists `y`, `Y`, `n`, and `N` among the YAML
1.1 boolean spellings to recognise, with the rationale "PyYAML does this, so
Salt does this, so existing trees depend on it".

**Implementation:** those four are not recognised. `name: n` is the string
`"n"`.

**Why:** PyYAML does not do this. Its `bool` resolver matches
`yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF`
and stops there — the single letters are in the YAML 1.1 type specification
but not in PyYAML's implementation of it. Implementing the table as written
would make `n` a boolean in halite and a string in Salt, which breaks the
Salt compatibility that the section exists to preserve. The remaining twelve
spellings are recognised exactly as written.

**Risk if this is wrong:** a tree that genuinely relies on `y` coercing to
`true` under some other YAML 1.1 loader would behave differently. No such
loader is in the migration path.

**Where:** `internal/yaml/scalar.go`, `bool11`. Pinned by
`TestSingleLetterBooleansStayStrings`.

### 1.2 Include cycles are a warning, not an error

**Spec:** Section 11.2's pipeline step 3 says the compiler "reports the cycle
path" for include cycles. Steps 4 and 5 say "is an error" for their
respective failures.

**Implementation:** an include cycle is a warning; compilation continues with
the already-visited file skipped.

**Why:** the wording differs from the neighbouring steps in a way that reads
as deliberate, and Salt tolerates include cycles. Making it an error would
refuse to compile trees that Salt compiles today, which is a migration
blocker for a condition that is usually harmless.

**Risk if this is wrong:** a genuinely broken tree compiles with a warning an
operator may not read.

### 1.3 `--` does not end key=value parsing

**Spec:** silent.

**Implementation:** `--` ends option parsing, so `--foo` after it is
positional, but `key=value` after it is still a kwarg.

**Why:** a kwarg is an argument rather than an option, and POSIX `--` governs
options. The escape hatch for a literal that must not be read as a kwarg is a
key that is not an identifier, which is already how `./configure --prefix=/usr`
stays positional.

**Where:** `internal/cli/cli.go`, `Parse`.

### 1.4 `runas` is only usable on a module that declares it

**Spec:** section 11.7 lists `runas` among the per-state options, alongside
`umask`, `order`, and `failhard`. The list reads as options available on any
state.

**Implementation:** `umask` is consumed as an option and applies to every
command the state runs. `runas` is read as an option *and left in the
arguments*, so a state using it on a module that does not declare a `runas`
parameter fails to compile with "is not a parameter of this function".

**Why:** `runas` is genuinely both — `cmd.run` takes it as an argument — and
leaving it in place lets that module read it directly. The cost is that the
option is not uniformly available, and the error an operator gets names the
parameter rather than the option.

**What it should probably be:** consumed like `umask`, with modules that
want it reading it from the context. The context now carries both, so the
change is small; it is left undone because it changes the compile behaviour
of existing states rather than only adding to it.

**Where:** `internal/state/lowstate.go`, the comment at the `runas`
extraction. Pinned by `TestPerStateExecutionOptionsReachTheModule`.

### 1.5 Test-mode conformance is checked more strictly than specified

**Spec:** section 11.6 states the contract; section 31 requires a shared
harness asserting it.

**Implementation:** the harness additionally asks each module the same
question twice in test mode with no setup in between, and accepts an optional
`Probe` that reads the managed system state directly. A module that quietly
applies its change during test mode is caught by either.

**Why:** the original harness checked the *shape* of what test mode returns
but never that test mode left the system alone, which is the one promise the
section exists to make. This is a strengthening, not a conflict.

**Where:** `internal/states/conformance.go`. See also 5.3.

---

## 2. Module coverage

The build ships **32 execution modules / 127 functions** and **16 state
modules / 46 functions**.

Section 15's inventory is roughly 90 execution modules across all tiers and
46 core state modules. The tables below are the full accounting. `functions`
counts what this build registers, not what the spec lists for that module —
per-module function depth is section 3.

Everything marked *not implemented* is a phase 2 or later item unless a
different reason is given.

### 2.1 Core execution modules (SPEC 15.2)

30 of 56 present.

| Module | Status | Functions | Note |
|---|---|---|---|
| `archive` | implemented | 2 | tar, tar.gz, zip; entries are refused if they escape the destination |
| `cmd` | implemented | 7 | |
| `config` | implemented | 3 | |
| `cron` | implemented | 2 | |
| `disk` | implemented | 1 | |
| `dnsutil` | implemented | 2 | |
| `environ` | implemented | 3 | |
| `event` | implemented | 1 | local only until the hub exists |
| `file` | implemented | 14 | |
| `git` | implemented | 5 | through the system `git` binary |
| `grains` | implemented | 6 | |
| `group` | implemented | 1 | |
| `hashutil` | implemented | 9 | |
| `hosts` | implemented | 3 | |
| `mine` | implemented | 2 | local only until the hub exists |
| `mount` | implemented | 1 | read-only; `mount`/`umount`/`fstab` not written |
| `network` | implemented | 5 | |
| `pillar` | implemented | 5 | |
| `pkg` | implemented | 6 | FreeBSD `pkg` provider only; see 2.5 |
| `random` | implemented | 3 | `crypto/rand` |
| `saltutil` | implemented | 5 | stubs that name the phase that will implement them |
| `service` | implemented | 8 | FreeBSD rc provider only; see 2.5 |
| `ssh_auth` | implemented | 1 | registered as `ssh.auth_keys` |
| `status` | implemented | 4 | |
| `sys` | implemented | 8 | |
| `sysctl` | implemented | 3 | |
| `sysrc` | implemented | 3 | FreeBSD; SPEC lists it as core |
| `test` | implemented | 5 | |
| `timezone` | implemented | 1 | read-only; `set_zone` not written |
| `user` | implemented | 3 | reads through `os/user`, writes through `pw` or `useradd` |
| `at` | not implemented | 0 | |
| `acl` | not implemented | 0 | POSIX ACL reading needs `acl_get_file`, which is cgo on FreeBSD; needs the `getfacl` binary path instead |
| `apparmor` | not implemented | 0 | Linux only; no host to verify on |
| `beacons` | not implemented | 0 | phase 3 |
| `blockdev` | not implemented | 0 | |
| `data` | not implemented | 0 | |
| `firewall` | not implemented | 0 | |
| `hostname` | not implemented | 0 | |
| `http` | not implemented | 0 | needs the address denylist of 15.2 before it is safe to ship |
| `kernelpkg` | not implemented | 0 | |
| `locale` | not implemented | 0 | |
| `logrotate` | not implemented | 0 | |
| `nfs` | not implemented | 0 | |
| `pkgrepo` | not implemented | 0 | |
| `ps` | not implemented | 0 | process enumeration is per-platform; FreeBSD needs `kvm` or `sysctl kern.proc` |
| `reboot` | not implemented | 0 | |
| `schedule` | not implemented | 0 | phase 3 |
| `selinux` | not implemented | 0 | Linux only; no host to verify on |
| `shadow` | not implemented | 0 | |
| `state` | not implemented | 0 | reachable as `halite-node state`, not as a callable module function |
| `sudo` | not implemented | 0 | |
| `swap` | not implemented | 0 | |
| `system` | not implemented | 0 | |
| `tls` | not implemented | 0 | |
| `tmpfs` | not implemented | 0 | |
| `x509` | not implemented | 0 | sizeable and entirely `crypto/x509`; a good standalone unit of work |

### 2.2 Core state modules (SPEC 15.5)

15 of 46 present, plus `sysrc`, which the section does not list.

| Module | Status | Functions | Note |
|---|---|---|---|
| `archive` | implemented | 1 | |
| `cmd` | implemented | 2 | |
| `cron` | implemented | 2 | |
| `file` | implemented | 13 | |
| `git` | implemented | 1 | |
| `group` | implemented | 2 | |
| `host` | implemented | 2 | |
| `module` | implemented | 2 | |
| `pkg` | implemented | 3 | |
| `service` | implemented | 4 | |
| `ssh_auth` | implemented | 2 | |
| `sysctl` | implemented | 1 | |
| `sysrc` | implemented | 2 | not in SPEC 15.5; FreeBSD's equivalent of the `hostname`/`service`-enable states |
| `test` | implemented | 5 | |
| `user` | implemented | 2 | |
| `zfs` | implemented | 2 | `filesystem_present`, `absent`; no `zpool` state |
| `acl` | not implemented | 0 | see 2.1 |
| `apparmor` | not implemented | 0 | |
| `at` | not implemented | 0 | |
| `beacon` | not implemented | 0 | phase 3 |
| `environ` | not implemented | 0 | |
| `firewall` | not implemented | 0 | |
| `gem` | not implemented | 0 | |
| `hostname` | not implemented | 0 | |
| `iptables` | not implemented | 0 | Linux only |
| `kernelpkg` | not implemented | 0 | |
| `locale` | not implemented | 0 | |
| `logrotate` | not implemented | 0 | |
| `lvm` | not implemented | 0 | Linux only |
| `mac_defaults` | not implemented | 0 | macOS only |
| `mount` | not implemented | 0 | the exec side is read-only, so the state has nothing to build on |
| `nftables` | not implemented | 0 | Linux only |
| `npm` | not implemented | 0 | |
| `pip` | not implemented | 0 | |
| `pkgrepo` | not implemented | 0 | |
| `pro` | not implemented | 0 | Ubuntu only |
| `reboot` | not implemented | 0 | |
| `schedule` | not implemented | 0 | phase 3 |
| `selinux` | not implemented | 0 | Linux only |
| `ssh_known_hosts` | not implemented | 0 | |
| `sudo` | not implemented | 0 | |
| `timezone` | not implemented | 0 | the exec side is read-only |
| `win_dacl` | not implemented | 0 | Windows only |
| `win_task` | not implemented | 0 | Windows only |
| `win_wua` | not implemented | 0 | Windows only |
| `x509` | not implemented | 0 | |
| `zpool` | not implemented | 0 | the exec side reads; no state writes |

`file.accumulated`, which SPEC 15.5 requires, is not implemented.

### 2.3 Platform modules (SPEC 15.3)

2 of 62 present. This is the largest single gap and it is a direct
consequence of having one host to develop on.

| Platform | Present | Absent |
|---|---|---|
| Common Linux | `zfs`, `zpool` | `systemd_service`, `journald`, `iptables`, `nftables`, `lvm`, `mdadm`, `quota`, `udev`, `modprobe`, `pam`, `openssl_cert`, `authselect` |
| FreeBSD | none under these names | `freebsdpkg`, `freebsd_service`, `freebsd_sysctl`, `pf`, `jail` |
| Debian, Ubuntu | none | `aptpkg`, `debconf`, `dpkg`, `debbuild`, `apt_key`, `ufw`, `netplan`, `apparmor`, `snap`, `pro` |
| RHEL family | none | `yumpkg`, `dnfpkg`, `rpm`, `firewalld`, `subscription_manager`, `dnf_module`, `chattr` |
| SUSE | none | `zypperpkg` |
| Windows | none | `win_pkg`, `win_service`, `win_file`, `win_dacl`, `win_task`, `win_useradd`, `win_groupadd`, `win_shadow`, `win_network`, `win_firewall`, `win_registry`, `win_disk`, `win_system`, `win_timezone`, `win_wua`, `win_certutil`, `win_dsc`, `win_lgpo` |
| macOS | none | `mac_brew_pkg`, `mac_service`, `mac_user`, `mac_group`, `mac_shadow`, `mac_power`, `mac_softwareupdate`, `mac_defaults`, `mac_keychain`, `mac_assistive` |

Two notes on this table:

- `zfs` and `zpool` are filed under "Common Linux" in SPEC 15.3. They were
  implemented and verified on FreeBSD, where ZFS is native. The spec's
  placement is wrong and section 15.3 should move them to a shared row.
- The FreeBSD row reads as entirely absent but is not, functionally: the
  FreeBSD behaviour that `freebsdpkg`, `freebsd_service`, and
  `freebsd_sysctl` would provide is implemented inside the virtual `pkg`,
  `service`, and `sysctl` modules, which is where SPEC 15.2 says provider
  selection belongs. Whether the named per-platform modules should also exist
  as aliases is an open question. `pf` and `jail` are genuinely absent.

### 2.4 Language and runtime modules (SPEC 15.4)

0 of 9 present: `pip`, `virtualenv`, `npm`, `gem`, `cargo`, `go`, `composer`,
`cpan`, `maven`. None of the Extended container modules (`docker`, `podman`,
`kubernetes`, `helm`) are present either.

These are the cheapest remaining breadth: each wraps one binary with a
machine-readable output mode, and none needs a platform this host lacks.

### 2.5 Provider depth for the virtual modules

`pkg` and `service` are specified as virtual modules with one provider per
platform family. Both have exactly one provider implemented and verified:

| Module | Providers specified | Implemented | Verified |
|---|---|---|---|
| `pkg` | apt, dnf, yum, zypper, apk, pacman, pkgng, brew, macpkg, winrepo, choco | pkgng (FreeBSD) | yes, on this host |
| `service` | systemd, sysvinit, upstart, openrc, launchd, freebsd_rc, smf, windows | freebsd_rc | yes, on this host |

Neither `pkg.version_cmp` nor the Debian/RPM version comparison it requires
is implemented, so the differential test SPEC 31 requires against
`dpkg --compare-versions` and `rpmdev-vercmp` has nothing to run against.

The D-Bus client SPEC 15.2 specifies for talking to systemd is not written.
The `service` module would fall back to `systemctl` on a Linux host, and that
fallback has never been executed.

---

## 3. Function depth within implemented modules

Module presence is not function parity. Where SPEC 15.2 enumerates a
module's functions, this is the shortfall.

### `file` — 14 exec functions of the ~50 enumerated

Present: `append`, `contains`, `copy`, `directory_exists`, `file_exists`,
`get_diff`, `get_hash`, `prepend`, `read`, `remove`, `replace`, `search`,
`stats`, `write`. The state side additionally covers `managed`, `directory`,
`symlink`, `touch`, `line`, `blockreplace`, `comment`, `uncomment`, `absent`.

Absent: `move`, `readlink`, `hardlink`, `chown`, `chgrp`, `chmod`, `access`,
`find`, `patch`, `sed`, `check_hash`, `grep`, `mkdir`, `makedirs`, `rmdir`,
`set_selinux_context`, `get_selinux_context`, `extract_hash`,
`apply_template_on_contents`, `join`, `basename`, `dirname`, `is_link`,
`list_backups`, `restore_backup`, `truncate`, `seek_read`, `seek_write`.

`list_backups` and `restore_backup` are worth calling out: the backup
mechanism they manage is itself not implemented, so a state asking for a
backup copy before overwriting a file silently gets none today. Salt spells
that option with a role name SPEC section 2.3 prohibits, so the replacement
spelling is itself an open question.

### `cmd` — 7 of 13

Present: `run`, `run_all`, `run_stdout`, `run_stderr`, `retcode`, `which`,
`has_exec`. Absent: `script`, `script_retcode`, `shell`, `exec_code`,
`run_chroot`, `run_bg`.

The security-relevant parts of the spec's `cmd` paragraph *are* implemented:
argv by default, `shell=True` as the opt-in, and `runas` through setuid and
setgid with the full supplementary group set rather than `su -c`.

### `pkg` — 6 of 26

Present: `install`, `remove`, `version`, `latest_version`, `list_pkgs`,
`refresh_db`. Absent: `purge`, `upgrade`, `available_version`,
`upgrade_available`, `hold`, `unhold`, `list_holds`, `list_upgrades`,
`info_installed`, `owner`, `file_list`, `file_dict`, `mod_repo`, `del_repo`,
`list_repos`, `list_downloaded`, `download`, `autoremove`, `version_cmp`.

### `service` — 8 of 18

Present: `start`, `stop`, `restart`, `reload`, `status`, `enable`, `disable`,
`enabled`. Absent: `force_reload`, `disabled`, `available`, `missing`,
`get_all`, `mask`, `unmask`, `masked`, `execs`.

`mask` and `unmask` are systemd concepts with no FreeBSD equivalent, so they
are blocked on a Linux host rather than on effort.

---

## 4. Platform coverage

Every platform-conditional path other than FreeBSD's is **written but never
executed**. It compiles, and that is all that is known about it.

| Platform | Compiles | Unit tests run | Verified against a real system |
|---|---|---|---|
| FreeBSD amd64 | yes | yes | yes — grains, highstate, drift reconvergence, requisites |
| Linux amd64 | yes (`GOOS=linux go build`) | no | no |
| Linux arm64 | yes | no | no |
| macOS | yes | no | no |
| Windows | yes | no | no |

Concretely, on Linux the following have never run: the `useradd`/`groupadd`
branch of `user` and `group`, `/etc/sysctl.conf` handling that differs from
FreeBSD's `/etc/sysctl.conf` semantics, the Linux branch of every grain that
reads `/proc` or `/sys`, `systemctl` service handling, and the apt/dnf
branches of `pkg` — which do not exist at all.

Grain collection is the sharpest edge here. It was verified against this host
and returns 63 grains including correct hardware detail. On Linux it will
take entirely different code paths, none exercised.

`internal/exec/credential_other.go` refuses `runas` off unix rather than
ignoring it, and `umask` refuses on Windows for the same reason: it is
implemented by execing through a POSIX shell. Both are the correct failure
for an unimplemented platform, and neither refusal has been observed on the
platform it applies to.

---

## 5. Test coverage against SPEC 31

### 5.1 Branch coverage

SPEC 31 holds the YAML parser, the template engine, the state compiler, and
the targeting matcher to **branch coverage above 90%**. Go's tooling measures
statement coverage, not branch coverage, so the numbers below are not the
same metric and are, in general, more forgiving than the bar asked for.

These figures are a snapshot taken with `make cover` at the commit that
introduced this file. Unlike the module tables above, they are not machine
checked, because measuring coverage requires running the suite that would be
doing the checking.

| Package | Statement coverage | SPEC 31 bar |
|---|---|---|
| `internal/regexcompat` | 100.0% | — |
| `internal/yaml` | 96.5% | >90% branch — met on statements, unmeasured on branches |
| `internal/cli` | 93.4% | — |
| `internal/target` | 92.8% | >90% branch — met on statements, unmeasured on branches |
| `internal/state` | 90.7% | >90% branch — met on statements, unmeasured on branches |
| `internal/buildpolicy` | 90.7% | — |
| `internal/value` | 89.6% | — |
| `internal/fileserver` | 89.4% | — |
| `internal/states` | 86.1% | — |
| `internal/pillar` | 84.7% | — |
| `internal/render` | 83.7% | — |
| `internal/runner` | 83.0% | — |
| `internal/exec` | 81.3% | — |
| `internal/template` | 79.8% | **>90% branch — not met** |
| `internal/signature` | 79.3% | — |
| `internal/config` | 77.8% | — |
| `internal/grains` | 75.3% | — |
| `internal/migrate` | 69.4% | — |
| `internal/builtin` | 36.3% | — |
| `internal/specaudit` | n/a | it tests documents, not code |
| `cmd/halite-node`, `cmd/halite-hub`, `cmd/halite-api` | 0% | — |
| `internal/version` | 0% | — |

Whole tree: 70.8%.

`internal/template` is the one correctness-core package still short of the
bar on either metric. It is also the largest: roughly 130 filters, the
expression grammar, inheritance, and macros. Closing it is a matter of
volume rather than difficulty.

`internal/builtin` at 36.3% is structurally limited rather than neglected: a
large share of its statements need root, a package manager with a writable
database, or a service manager with services to stop. Raising it honestly
means the containerised integration suite of SPEC 31, which is phase 5 work.

The three `cmd` packages are argument dispatch over tested libraries. They
are exercised by hand and by the lab run, not by `go test`.

### 5.2 The fourteen test layers

Every layer SPEC 31 requires, and where it stands. Two of the fourteen
are present, one is present and stronger than specified, one is partial, and
ten are absent.

| Layer | Status |
|---|---|
| Conformance, YAML | **absent.** The YAML test suite is not vendored and not run. The expected-failure set for constructs 10.1.2 rejects is not recorded. This is the single largest correctness gap in the project. |
| Conformance, templates | **absent.** No Jinja corpus with expected output. |
| Differential against Salt | **absent.** This is named the primary correctness gate and it has never been run. There is no Salt installation to run it against on this host. |
| Differential, version comparison | **absent**, and blocked: `pkg.version_cmp` is not implemented. |
| Conformance, state modules | **present** and stronger than specified — see 1.4. Covers 6 of the 46 state functions. |
| Property | **present** for all five named properties, each checked over generated input rather than a fixed corpus: path containment never escapes a root (`internal/fileserver/property_test.go`, 23000 generated paths plus the symlink cases), the topological sort is stable, requisite resolution terminates, and a requisite genuinely orders its target (`internal/state/property_test.go`, over random requisite graphs including cycles), the YAML parser never panics (`internal/yaml/property_test.go`, 50000 generated documents), and targeting is monotonic under grain addition (`internal/target/property_test.go`, 20000 expression and node pairs). Negation is asserted as the documented exception to monotonicity rather than left implicit. |
| Fuzz | **present** for three of the eight named targets: the YAML parser and its encoder, the template lexer and parser, and the compound target parser. `make fuzz` runs all seven functions; `make fuzz FUZZTIME=30m` is a campaign. The first run found four defects, listed in 5.3 below. Still absent: the wire message decoder, the cron parser, the roster parser, and the bridge protocol decoder, all of which belong to phases that have not started. |
| Integration | **absent.** No containerised hub-plus-nodes harness. Blocked on phase 2. |
| Scale | **absent.** Blocked on phase 2. |
| Upgrade | **absent.** Nothing to upgrade from. |
| Chaos | **absent.** Blocked on phase 2. |
| Security | **partial.** The dependency-graph assertion of 4.2 is implemented and enforced (`internal/buildpolicy`, `make policy`). `govulncheck` is not wired in. No static analysis beyond `go vet`. No external review. |
| Reproducibility | **unverified.** One builder, one platform. Two independent builders producing identical digests has never been attempted. |

### 5.3 What fuzzing found

Recorded because "we added fuzzing" is worth less than what it caught. Four
defects, all reachable from a `.sls` file, all of which had passed the
hand-written suite:

- **A block scalar could be parsed with a negative indent**, panicking in
  `strings.Repeat`. `blockIndent == 0` doubled as "not yet detected", but
  zero is a legitimate detected indent for a block scalar at the top of a
  document, where the parent indent is -1. Detection therefore ran a second
  time on a later, deeper line and raised the indent after shallower lines
  had already been accepted below it.
- **A quote anywhere on a line hid the mapping colon after it.**
  `lineIsMappingEntry` treated any `'` or `"` as opening a quoted scalar and
  scanned for its close, so an unpaired one swallowed the rest of the line
  and `a"b: 1` parsed as a plain scalar, then failed on the colon. PyYAML
  reads it as a mapping with the key `a"b`, so this was also a Salt
  compatibility defect. Quotes now open a token only where a token can
  start.
- **`{%}` panicked in the template lexer.** The default block delimiters
  `{%` and `%}` overlap on the `%`, so searching for the closing delimiter
  from the start of the opening one found the opener's own second byte and
  produced an end offset before the start of the tag body.
- **`x[]` panicked in the template evaluator.** An empty subscript produced
  an `ItemExpr` with a nil index, which the evaluator then dereferenced.
  Python and Jinja both reject it; now so does this.

After those fixes: 800000 executions against the YAML parser, 2780000
against the template engine, and a four-minute campaign against the compound
target parser, all clean. The corpora are committed under each package's
`testdata/fuzz/`.

### 5.4 What the lab run does cover

Not a substitute for the above, but recorded so the gaps are not read as
"nothing was verified". On this host, against a real state tree: a
grain-matched top file, an `include`, a templated pillar loop, a `salt://`
source, `require` and `onchanges` requisites. `state.apply` converged with 5
changes; a second run reported 0 changes and exit code 2; a hand-edited file
reconverged; `onchanges` fired only on the run where its target changed.

Real reads verified against the host: 63 grains including chassis and disk
detail, `zpool` health, `git` revision, uptime.

---

## 6. Everything else not started

### 6.1 Delivery phases

Phases 2 through 6 of SPEC 32 are untouched. That means: no transport, no
enrollment CA, no hub, no targeting over a network, no job cache, no RBAC, no
event bus, no beacons, no scheduler, no reactors, no orchestration, no
runners, no mine over the wire, no API, no OIDC or LDAP, no webhooks, no
returners, no bridge protocol, no gitfs, no s3fs, no agentless mode, no
relays, no FIPS artifact set, no detached job signing, no signed state trees,
and no backtracking regex engine.

`halite-hub` and `halite-api` exist as binaries that parse arguments and
report that their phase has not landed, which is deliberate: the alternative
is a binary that appears to work.

### 6.2 Build and release

- `make release` has never been run. There is nothing to vendor, since the
  dependency count is zero, so the vendoring step is untested.
- Cross-compilation is verified for the four target platforms; nothing beyond
  compilation is verified.
- The Makefile is written for BSD make and uses `!=` rather than
  `$(shell ...)`. GNU make handles `!=` from 4.0 onward, so it should work,
  but it has not been run under GNU make.
- Reproducible builds: see 5.2.

### 6.3 The compatibility shim and the migration tool

The migration report of SPEC 28 runs and exits non-zero on blocking findings.
It has been run against synthetic trees only — never against a real Salt tree
of any size, which is phase 0's stated exit criterion. That criterion is
therefore **not** met in substance, only in mechanism.

---

## 7. SPEC 33 open questions

Section 33's nine questions were answered by taking the spec's own default,
per the instruction to flag rather than block. Where an answer is embedded in
code, it is recorded here so that reversing it is a search rather than an
excavation.

| # | Question | Taken as | Where it bites |
|---|---|---|---|
| 1 | Project name | Halite, unchanged | module path, binary names, `HALITE=1` in the child environment, the `#HALITE_CRON_IDENTIFIER:` marker in managed crontabs |
| 2 | Compatibility horizon | no date set | the config shim has no removal path |
| 3 | `cmd.run` default | argv, per 15.2 | `cmd_default_shell` is read but a tree relying on shell parsing breaks at migration |
| 4 | Strict undefined | strict, per 10.2.6 | `--permissive` exists as the transition |
| 5 | PAM | dropped | no local account authentication; phase 4 concern |
| 6 | Detached job signing | not implemented | phase 6 |
| 7 | `golang.org/x/sys` | allowed but unused | the allowlist permits it and `golang.org/x/term`; `go list -m all` returns only this module, so the zero-dependency property currently holds outright |
| 8 | The regex gap | deferred to phase 6, per 10.4 | `internal/regexcompat` refuses unsupported constructs by name; the migration report counts them, so the scheduling decision has its data |
| 9 | Windows scope | assumed as written | 18 Windows modules, none started |

---

## 8. Suggested order for closing this

Ranked by correctness value per unit of work, given one FreeBSD host:

1. **Vendor and run the YAML test suite.** Now the largest single
   correctness gap, and it needs no host but this one.
2. **Language and runtime modules.** Nine modules, each wrapping one binary,
   all runnable here.
3. **`x509`.** Self-contained, entirely `crypto/x509`, no platform
   dependency.
4. **Function depth in `file`, `cmd`, `pkg`, and `service`.** Mechanical, and
   it is what a real tree actually hits.
5. **A Linux host.** Everything in section 4 is blocked on this, and it is
   the point at which the apt and systemd providers stop being theoretical.
6. **A Salt installation to run the differential gate against.** Named the
   primary correctness gate; currently unrun.
