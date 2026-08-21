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

### 1.4 Test-mode conformance is checked more strictly than specified

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

The build ships **42 execution modules / 168 functions** and **20 state
modules / 54 functions**.

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
| `pkg` | implemented | 8 | FreeBSD `pkg` provider only; see 2.5. `version_cmp` implements the Debian and RPM orderings directly and asks pkg(8) for FreeBSD's |
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
| `x509` | implemented | 8 | key and CSR generation, certificate creation self-signed or CA-signed, inspection, expiry, and signature verification |

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
| `gem` | implemented | 2 | install and remove, comparing against the tool's own listing |
| `hostname` | not implemented | 0 | |
| `iptables` | not implemented | 0 | Linux only |
| `kernelpkg` | not implemented | 0 | |
| `locale` | not implemented | 0 | |
| `logrotate` | not implemented | 0 | |
| `lvm` | not implemented | 0 | Linux only |
| `mac_defaults` | not implemented | 0 | macOS only |
| `mount` | not implemented | 0 | the exec side is read-only, so the state has nothing to build on |
| `nftables` | not implemented | 0 | Linux only |
| `npm` | implemented | 2 | install and remove, comparing against the tool's own listing |
| `pip` | implemented | 2 | install and remove, comparing against the tool's own listing |
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
| `x509` | implemented | 2 | private_key_managed and certificate_managed, both of which converge on a second run |
| `zpool` | not implemented | 0 | the exec side reads; no state writes |

`file.accumulated`, which SPEC 15.5 requires, is not implemented.

The `x509` states are worth a note. Salt's own re-issue a certificate on
every highstate, because a re-issued certificate carries a new serial and
a new expiry and so never matches what the last run left. These read what
is on disk first and re-issue only when the certificate is missing, no
longer matches its private key, was not signed by the configured CA, or
has fallen inside the renewal window — and the comment says which. A
second run leaves the bytes alone, which the tests assert.

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

All 9 present. Each wraps a system binary and parses its machine-readable
output; no language runtime is embedded and no library is linked, so the
node inherits the operating system's patching cadence for the tool.

| Module | Status | Functions | Note |
|---|---|---|---|
| `cargo` | implemented | 4 | install, uninstall, list, version |
| `composer` | implemented | 4 | install, require, list, version |
| `cpan` | implemented | 3 | install, version, module_version; the last asks perl directly, since `cpan -D` opens a session and reaches the network |
| `go` | implemented | 3 | install, env, version |
| `gem` | implemented | 4 | install, uninstall, list, version |
| `maven` | implemented | 2 | run, version |
| `npm` | implemented | 4 | install, uninstall, list, version |
| `pip` | implemented | 5 | install, uninstall, list, freeze, version |
| `virtualenv` | implemented | 2 | create, version |

Verified against the real binary on this host: `npm`, `cargo`, `go`,
`cpan`. Exercised only through the recording runner, because the host has
no such binary: `gem`, `composer`, `maven`, and `pip` — a `pip` script
exists here but no importable pip for the system python, so its output
parsing is tested against captured text rather than a live tool.
`virtualenv` is in the same position as pip.

One thing only the real tool showed: `npm ls --json` omits the `version`
key for a package it cannot resolve. A state pinning a version against one
of those would reinstall on every run and never converge, and never say
why. It now refuses and names the package; an unpinned request is still
satisfied by the package's presence. That is the shape of defect a
recorded fixture would not have produced.

The output each parses is the tool's own machine-readable form:
`pip list --format=json`, `npm ls --json --depth=0`, `composer show
--format=json`, `cargo install --list`, `go env`, and `gem list --local`.
Where the format is text rather than JSON the parser is written against
the real output, not against a guess, and the two text parsers have their
own tests.

None of the Extended container modules (`docker`, `podman`, `kubernetes`,
`helm`) is present. SPEC 15.4 puts them in a later tier.

### 2.5 Provider depth for the virtual modules

`pkg` and `service` are specified as virtual modules with one provider per
platform family. Both have exactly one provider implemented and verified:

| Module | Providers specified | Implemented | Verified |
|---|---|---|---|
| `pkg` | apt, dnf, yum, zypper, apk, pacman, pkgng, brew, macpkg, winrepo, choco | pkgng (FreeBSD) | yes, on this host |
| `service` | systemd, sysvinit, upstart, openrc, launchd, freebsd_rc, smf, windows | freebsd_rc | yes, on this host |

`pkg.version_cmp` is implemented for all three orderings: Debian and RPM
transcribed from dpkg and rpmvercmp, FreeBSD's asked of pkg(8). Doing the
first two directly rather than shelling out matters twice — it works on a
node that has neither tool, which is every node when the hub decides
whether an upgrade is needed, and it is one process rather than one per
comparison, which a `pkg.latest` over a few hundred packages notices.

The live differential against `dpkg --compare-versions` and
`rpmdev-vercmp` still needs a host that has them. See 5.2.

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

A per-state `runas` or `umask` governs the commands a state runs. On a
state that runs none — `file.managed` writes through the Go runtime rather
than through a program — the option is accepted and has no effect, silently.
Whether a module shells out is not visible to the compiler, so warning about
it would need the signature to declare it. Salt behaves the same way, so
this is a shared limitation rather than a divergence, but it is the same
silent-no-op shape as the defects in 5.3 and is worth closing eventually.

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
| `internal/yaml` | 96.1% | >90% branch — met on statements, unmeasured on branches |
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
| `internal/template` | 81.9% | **>90% branch — not met** |
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
eight are absent.

| Layer | Status |
|---|---|
| Conformance, YAML | **present.** All 402 cases of the suite's `data` branch run on every `go test`, vendored under `internal/yaml/testdata/yaml-test-suite/`. Each case is checked three ways: a document the suite calls invalid must be refused, one it calls valid must parse, and where the suite supplies `in.json` the parsed tree must match. Every disagreement has a row in a table giving its reason, enforced in both directions so a stale row fails as loudly as an unrecorded one. Standing: 328 of 402 agree, 34 disagree by design, 40 are gaps — see 5.4. |
| Conformance, templates | **present.** Two corpora under `internal/template/testdata/jinja-corpus/`, run on every `go test`. 198 cases are extracted mechanically from Jinja's own pytest suite, carrying each case's environment options; disagreements have a row apiece with a reason, enforced in both directions. 123 more are written here for what Jinja's tests cannot cover: Salt's added filters, the strict undefined of 10.2.6, the limits of 10.2.8, and the refusals the subset owes an operator — those carry no deviation table, because a case that fails there is one this project got wrong. Standing: 146 of 198 agree, 27 are outside the subset, 25 are gaps — see 5.5. |
| Differential against Salt | **absent.** This is named the primary correctness gate and it has never been run. There is no Salt installation to run it against on this host. |
| Differential, version comparison | **partial.** `pkg.version_cmp` exists, with the Debian and RPM orderings implemented directly and FreeBSD's asked of pkg(8), since libpkg is its own specification. The FreeBSD half of the differential is real and runs here: 14 pairs go to `pkg version -t` and to halite and must agree, and the test skips loudly rather than passing quietly where pkg(8) is absent. The Debian and RPM halves need a Debian or RHEL host for `dpkg --compare-versions` and `rpmdev-vercmp`; until then they are tested against those projects' own published vectors, which are the cases the algorithms are known to get wrong. |
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

### 5.4 Where YAML conformance stands

Running the suite for the first time put the parser at 228 of 402, with
140 defects. Twenty fixes took it to **328 and 40**. Statement coverage of
`internal/yaml` rose to 96.1% along the way, but the suite is the thing
actually measuring correctness here.

What the fixes were, and why each mattered beyond the score:

- **A document beginning on the `---` line was thrown away.** The marker
  line was skipped whole, so `--- |` lost the `|` and everything under it
  was reparsed as a plain scalar. A block scalar written that way silently
  lost its style and its chomping — `--- |` over ` ab` gave `"ab"` rather
  than `"ab\n"` — which is a file that differs from the one the state
  describes and a state that reports a change on every run. 15 cases.
- **Folding was wrong in three ways.** A break next to a more-indented
  line is preserved rather than folded, and that is *in addition* to the
  newlines the blank lines contribute, so halite produced one fewer
  newline every time an indented block sat inside a folded scalar. A line
  beginning with a tab is more-indented too. And a blank line's own
  indentation is content. SPEC 10.1.1 names the more-indented rule as the
  one naive implementations get wrong; it was wrong. 7 cases.
- **A multi-line plain scalar as a mapping value was cut at its first
  line**, and the continuation was then read as a stray over-indented
  entry. One parameter carried two meanings: where a node starts, and
  where a continued scalar ends. 7 cases, two of which had been
  misrecorded as deliberate tab rejections.
- **Anchors, tags, and aliases on mapping keys were not read**, so
  `&anchor key: 1` had the literal key `"&anchor key"` and `!!str 1: x`
  had the integer 1. Behind it, a mapping whose first key carries
  properties starts where those properties do, not where the key text
  does. 10 cases, four of them previously misrecorded.
- **`%YAML` and `%TAG` directives were parsed as content**, then, once
  consumed, were not validated: a directive with no document, two `%YAML`
  lines, extra words, and a malformed version all passed. 17 cases.
- **An escaped tab at a folded line break was dropped.** After unescaping,
  `\t` is the same byte as a layout tab and folding trimmed both. 8 cases.
- **An explicit `? ` key with no `:` line was refused**, though that is how
  a set is written; so were a block scalar key and a key on the lines
  below the `?`. 5 cases.
- **White space between a key and its colon was refused** — `'key' : v`
  and `key\t: v` — and a tab after the colon was not a separator. 2 cases.
- **A plain scalar inside a flow collection was cut at the line break**,
  and with it came a bound that was missing: a key in a flow sequence must
  sit on one line. 1 case.
- **An empty block scalar was an error.** `strip: >-` with the next key at
  the mapping's own column is a key whose value is the empty string. 1
  case.
- **Three round-trip defects, found by re-fuzzing rather than by the
  suite.** `\xNN` emitted the raw byte instead of the code point, so
  anything above 0x7F produced a string that is not valid UTF-8 and the
  parser refused its own encoder's output. An unbalanced `]` drove the
  mapping-entry lookahead's flow counter negative, hiding the colon after
  it. And a mapping key needs stricter quoting than a value, since it has
  to survive that lookahead: `b[1]` is a fine plain scalar as a value and
  breaks the entry as a key. 1 suite case, and three shapes the suite does
  not cover — which is the argument for running both.

Two of those eleven were caught only because the deviation table is
enforced in both directions: earlier fixes moved cases along, and what had
been recorded as a deliberate refusal turned out to be a defect wearing
the wrong reason. Seven rows in total have been reclassified that way.

Fixes after the first twelve, each of which the suite found:

- **Flow sequence and flow mapping keys bound differently.** A key in a
  sequence must fit on one line; in a mapping it may take its colon on the
  next. halite had one rule for both, and an earlier attempt at this
  traded two fixes for two regressions by not making the distinction. 3
  cases.
- **Tabs after a document marker or a sequence dash**, and **node
  properties spanning lines** — an anchor and a tag on separate lines are
  properties of the same node. Crossing that break needs two bounds: only
  for the kind the node has not got yet, and only when indented past the
  parent. 10 cases.
- **An over-indented blank line is content**, not a blank line, so a
  block scalar kept the leftover spaces and their break instead of losing
  both. And a **quoted key in a flow sequence** may be followed by white
  space before its colon. 5 cases.
- **Malformed flow collections were read as data**: an empty entry between
  commas, a `#` with no white space in front of it, and a bare `-`, which
  is a block indicator with nothing to indicate inside flow. 6 cases.
- **A blank line before a block scalar's content may not be deeper than
  the content**, and **an alias carries no properties** — it is a
  reference, not a node, so `&b *a` names an anchor pointing at nothing.
  5 cases.
- **A block mapping key must be on one line**, which a folded quoted
  scalar can otherwise slip past. 2 cases.

Fuzzing alongside the suite found three more the suite does not cover, one
of them a regression this work introduced: a quoted scalar read byte by
byte had each byte converted as if it were a code point, so `"café"` came
back as `"cafÃ©"`; a string holding bytes that are not UTF-8, which
cmd.run can return, was written out as a document the parser then refused,
and is now tagged binary; and an unbalanced `]` drove the mapping-entry
lookahead's nesting count negative. The suite is not a substitute for the
fuzzer or the other way round.

What remains, largest first:

| Class | Cases | What it is |
|---|---|---|
| `gapLenient` | 23 | halite parses a document the suite requires to be an error. Accepting too much is the safe direction for an existing tree, which is why it ranks last. What is left here is mostly tabs in odd positions, document markers inside quoted scalars, and under-indented continuations. |
| `gapFlow` | 4 | complex keys in flow, which SPEC 10.1.2 refuses on purpose but with a message about the wrong thing, and an explicit `? ` key inside flow. |
| `gapAfterDocument`, `gapExplicitKey`, `gapOther`, `gapPlainScalar`, `gapValueOther` | 10 | five classes of two. |
| `gapChomping`, `gapDirective`, `gapMappingKey` | 3 | singletons. |

There is no cluster left to take. From here it is one case at a time, and
the value per fix is lower than anything else on the list in section 8.

Of the 34 deliberate disagreements, 20 are tags outside the nine types, 7
are tabs used for indentation, 6 are complex keys, and 1 is a duplicate
key. Those are SPEC 10.1.2 working as specified, and they are excluded
from the conformance figure, since halite does not claim to be YAML 1.2
there.

The value comparison runs only where the suite supplies `in.json` and
halite parses the document, so a case that fails to parse is counted once,
as a rejection, and its value is never checked.

### 5.5 Where template conformance stands

146 of 198 of Jinja's own extractable cases, 27 of the rest outside the
subset by design and 25 gaps. `internal/template` rose to 81.9% statements
on the way, still the one correctness-core package under the SPEC 31 bar.

Writing the second corpus found a crash on the first run.
`{% macro m() %}{{ m() }}{% endmacro %}{{ m() }}` overflowed the goroutine
stack and killed the process — a template could crash a node. Nothing
counted macro calls against the recursion limit of 10.2.8, and the
renderer could not: a macro is called through the renderer it was
*defined* in, whose depth never changes however deep the call gets. The
counter now lives on the budget, the one thing every sub-renderer shares.

Three other fixes came out of it:

- **`tojson` rendered `{"a":1}` where Jinja renders `{"a": 1}`.** Python's
  json.dumps spaces its separators and Jinja inherits that, so a tree
  writing JSON into a file through the filter produced spaced output under
  Salt. Compact output here would make every such file differ on the first
  run after a migration — a change the tree did not ask for.
- **`{% filter upper|replace('a','b') %}` read only the first filter.**
- **The `+` whitespace marker was not parsed**, so `{%+ if x %}` failed.
  It is the explicit opposite of `-`, keeping whitespace that
  `trim_blocks` or `lstrip_blocks` would eat, which a tree templating a
  file with meaningful indentation needs.
- **A tuple rendered as a list.** `(1, 2)` printed `[1, 2]` and `(1,)`
  printed `[1]`, losing the trailing comma that tells a one-element tuple
  from a parenthesised expression. A tuple is now its own type inside a
  render: it prints with parentheses and behaves as a sequence in every
  other way — iteration, unpacking, indexing, slicing, membership, length,
  concatenation, the sequence tests, and every filter. It exists only
  inside the render, since the nine-type model of SPEC 6.4 has no tuple
  and nothing may put one into pillar or a state argument; by the time a
  value leaves the engine it is text, and `tojson` writes a list.
- **`{% set %}` assigned into an enclosing scope.** It walked outward to
  the innermost scope already holding the name, so an assignment inside a
  loop survived to the next iteration and escaped the loop entirely.
  Jinja assigns in the current scope and nowhere else, which is the whole
  reason `namespace()` exists: without that rule there would be no need
  for it. A tree written against Salt's behaviour and relying on the leak
  would have rendered differently here, silently. `if` introduces no
  scope, so a set inside one is still visible after it; `for`, `with`,
  and a macro body each do, and all four boundaries are pinned.

The corpus itself had three defects worth recording, because a conformance
suite that lies is worse than none. A case's environment options were
dropped in extraction, so a `lstrip_blocks` test ran against the default
environment and its difference was recorded as a defect here — about a
dozen rows were that. Collecting every template before matching the
assertions paired each assertion with the *last* template of its name.
And Jinja's `Environment` takes its delimiters positionally as well as by
keyword, so a test setting `<%` and `%>` that way had its flags captured
and its delimiters silently dropped, leaving the case to run against the
wrong syntax; those cases are now dropped instead, along with the ones
setting a line-statement prefix halite does not have. All three are fixed,
and the numbers above are the honest ones.

What remains, largest first:

| Class | Cases | What it is |
|---|---|---|
| `gapRendering` | 9 | what is left is float and dict spelling, and cases needing a custom code generator. |
| `gapScoping` | 4 | what remains needs a `test` callable those Jinja tests register on the context, which the extractor cannot carry; they are scoping cases in name only. |
| `gapOther` | 4 | unclassified. |
| `gapFilterBehaviour`, `gapNumericAttribute`, `gapTestArgument` | 6 | a filter differing from Jinja's, the Django-style `a.0` subscript, and a test taking an argument in a position the parser does not reach. |
| `gapCallResult`, `gapWhitespaceControl` | 2 | calling the result of a filter, and one remaining whitespace case. |

Of the 27 outside the subset: 9 are markup and i18n filters, 7 are Python
string and dict methods, 6 are autoescape, and 5 are the strict undefined
of 10.2.6 — each of which renders correctly under `undefined: permissive`,
which is the transition a Salt tree migrates through, and the harness
checks that specifically rather than taking it on trust.

### 5.6 What the lab run does cover

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

Ranked by correctness value per unit of work, given one FreeBSD host.

The two conformance suites have moved to the bottom. Both are running and
both are down to isolated cases — 40 YAML and 25 template — with no
cluster left in either. That is a good place for them to be, and it means
the next fix in each is worth less than anything above it.

1. **Function depth in `file`, `cmd`, `pkg`, and `service`** (section 3).
   `file` has 14 exec functions of about 50, `pkg` 6 of 26. Mechanical
   work, and it is what a real tree actually hits.
2. **A Linux host.** Everything in section 4 is blocked on it, and it is
   the point at which the apt and systemd providers stop being
   theoretical. 60 of the 62 platform modules of SPEC 15.3 wait behind it.
3. **A Salt installation to run the differential gate against.** SPEC 31
   calls it the primary correctness gate. It has never been run.
4. **The remaining 40 YAML conformance gaps** (5.4). 23 are documents
   accepted that should be refused, which is the safe direction; the other
   17 are spread across nine classes of two or fewer.
5. **The remaining 25 template conformance gaps** (5.5). The largest class
   is 9, and it is float and dict spelling.
