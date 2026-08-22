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

### 1.5 The filesystem layout follows the platform, not the FHS

SPEC section 27.3 fixes the layout in Linux FHS terms: `/etc/halite`,
`/var/lib/halite`, `/run/halite`. A BSD uses none of those. A package's
configuration lives under `/usr/local/etc`, durable state in `/var/db`,
and `/run` does not exist at all.

Following the text literally put three sets of files where no BSD
administrator looks, and it had already caused a defect rather than a
theoretical one: the rc.d scripts in `contrib/` say
`/usr/local/etc/halite`, because that is where they belong, while the
binary defaulted to `/etc/halite`. On the one platform this build is
verified on, a service and a hand-run command read different
configuration.

| SPEC 27.3 | FreeBSD, OpenBSD, NetBSD, DragonFly |
|---|---|
| `/etc/halite` | `/usr/local/etc/halite` |
| `/var/lib/halite` | `/var/db/halite` |
| `/run/halite` | `/var/run/halite` |

`/var/cache` and `/var/log` are the same on both and are unchanged.
macOS is deliberately not in the list: Homebrew's prefix is not fixed, so
`/etc` is the honest default there until someone with a Mac says
otherwise.

A test asserts the service files in `contrib/` and the compiled default
agree, in both directions, so the drift that caused this cannot come
back. `docs/configuration.md` writes these defaults as tokens rather
than paths, so that a document generated on FreeBSD and one generated on
Linux are the same document.

The configuration root is additionally probed for `state` and `pillar`
directories, ahead of 27.3's `/srv` paths. That is not in the
specification. It is there because an administrator setting halite up
beside an existing Salt installation symlinks the trees into the
configuration root, and having to write `file_roots` to describe what is
already sitting there is a papercut with no upside.

---

## 2. Module coverage

The build ships **42 execution modules / 209 functions** and **20 state
modules / 56 functions**.

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
| `cmd` | implemented | 12 | |
| `config` | implemented | 3 | |
| `cron` | implemented | 2 | |
| `disk` | implemented | 1 | |
| `dnsutil` | implemented | 2 | |
| `environ` | implemented | 3 | |
| `event` | implemented | 1 | local only until the hub exists |
| `file` | implemented | 32 | |
| `git` | implemented | 5 | through the system `git` binary |
| `grains` | implemented | 6 | |
| `group` | implemented | 1 | |
| `hashutil` | implemented | 9 | |
| `hosts` | implemented | 3 | |
| `mine` | implemented | 2 | local only until the hub exists |
| `mount` | implemented | 1 | read-only; `mount`/`umount`/`fstab` not written |
| `network` | implemented | 5 | |
| `pillar` | implemented | 5 | |
| `pkg` | implemented | 18 | FreeBSD `pkg` provider only; see 2.5. `version_cmp` implements the Debian and RPM orderings directly and asks pkg(8) for FreeBSD's |
| `random` | implemented | 3 | `crypto/rand` |
| `saltutil` | implemented | 5 | stubs that name the phase that will implement them |
| `service` | implemented | 16 | FreeBSD rc provider only; see 2.5 |
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
| `cmd` | implemented | 3 | `script` takes its source as the state's name, as Salt's does |
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
| `sysrc` | implemented | 3 | not in SPEC 15.5; FreeBSD's equivalent of the `hostname`/`service`-enable states. `managed` is Salt's name and `present` is this build's, and they are the same function |
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

### `file` — 32 exec functions of the ~50 enumerated

Present: `access`, `append`, `basename`, `check_hash`, `chgrp`, `chmod`,
`chown`, `contains`, `copy`, `directory_exists`, `dirname`, `file_exists`,
`find`, `get_diff`, `get_hash`, `grep`, `hardlink`, `is_link`, `join`,
`makedirs`, `mkdir`, `move`, `prepend`, `read`, `readlink`, `remove`,
`replace`, `rmdir`, `search`, `stats`, `truncate`, `write`. The state side
additionally covers `managed`, `directory`, `symlink`, `touch`, `line`,
`blockreplace`, `comment`, `uncomment`, `absent`.

Absent: `patch`, `sed`, `set_selinux_context`, `get_selinux_context`,
`extract_hash`, `apply_template_on_contents`, `list_backups`,
`restore_backup`, `seek_read`, `seek_write`.

Two of those absences are choices rather than work not done. `sed` and
`patch` both shell out to a tool to edit a file in place, which is the
pattern `file.replace` and `file.blockreplace` exist to replace: a state
that describes the file it wants converges, and one that applies an edit
does not. `patch` may still be worth having for a vendor-supplied diff.

`file.check_hash` refuses an MD5 or SHA-1 digest by name rather than
comparing it. A tree carrying one should hear that it verifies nothing;
MD5 collisions are cheap and SHA-1's are within reach, and the same source
that published either almost certainly publishes a sha256.

`list_backups` and `restore_backup` are worth calling out: the backup
mechanism they manage is itself not implemented, so a state asking for a
backup copy before overwriting a file silently gets none today. Salt spells
that option with a role name SPEC section 2.3 prohibits, so the replacement
spelling is itself an open question.

### `cmd` — 12 of 13

Present: `run`, `run_all`, `run_stdout`, `run_stderr`, `retcode`, `which`,
`has_exec`, `shell`, `script`, `script_retcode`, `exec_code`, `run_bg`.
Absent: `run_chroot`, which needs a chroot to be worth testing in.

The security-relevant parts of the spec's `cmd` paragraph are implemented:
argv by default, `shell=True` as the opt-in, and `runas` through setuid and
setgid with the full supplementary group set rather than `su -c`. `shell`
and `script` are the two that run a shell on purpose, and `cmd.shell` logs
that it did — SPEC 15.2 asks that opting back in be visible, and a silent
shell is the thing the inversion exists to stop.

The `cmd.run` **state** does not take Salt's `bg` argument, so a tree
carrying one does not compile. The execution module has `run_bg`, so the
capability is there; what is missing is the decision about what a
backgrounded state means — it cannot report whether it changed anything,
it cannot be meaningfully run under `--test`, and nothing waits for it or
reaps it. Salt answers all three by returning success immediately.
Copying that is a choice rather than an oversight, and it has not been
made. The differential gate found this.

A script is fetched to a file only its owner can read or run, and removed
after. Many carry a credential, and the temporary directory is
world-readable. A `salt://` source goes through the file server, so the
containment rules of 13.5 apply to it as to any other file.

### `pkg` — 18 of 26

Present: `install`, `remove`, `purge`, `upgrade`, `version`,
`version_cmp`, `latest_version`, `upgrade_available`, `list_pkgs`,
`list_upgrades`, `list_holds`, `list_repos`, `hold`, `unhold`,
`file_list`, `owner`, `refresh_db`, `available_version`.
Absent: `info_installed`, `file_dict`, `mod_repo`, `del_repo`,
`list_downloaded`, `download`, `autoremove`.

The optional capabilities — holding, upgrading everything at once, mapping
a package to the files it owns, and listing repositories — sit behind
interfaces beside the provider one rather than in it, because they are not
universal: apk has no hold in the dpkg sense, and pkgng's idea of a
repository is a file rather than a line in sources.list. A provider that
cannot answer says so and names itself, rather than returning an empty
answer that a tree would read as "there are none". Only pkgng implements
them so far, which is what this host can verify; apt, dnf, and apk are
recorded above as unexercised anyway.

Every mutating function answers with what actually changed, by comparing
the package list before and after, rather than with what was asked for. A
state's `changes` is then the truth even when the package manager pulled a
dependency in with it.

### `service` — 16 of 18

Present: `start`, `stop`, `restart`, `reload`, `force_reload`, `status`,
`enable`, `disable`, `enabled`, `disabled`, `available`, `missing`,
`get_all`, `mask`, `unmask`, `masked`. Absent: `execs`, and the
`run_chroot`-shaped corner of the module.

`get_all` needed a second interface beside the provider one, because not
every init system can enumerate its services and folding it into the main
interface would make the others implement a stub that lies. `mask` needed a
third: masking is systemd's alone, and a node that is not running systemd
gets an error naming the init system it *is* running and pointing at
`disable`, rather than a silent no-op. Verified here — 235 services listed
by `service -l`, `available sshd` true, and masking refused by name.

---

## 4. Platform coverage

| Platform | Compiles | Unit tests run | Verified against a real system |
|---|---|---|---|
| FreeBSD amd64 | yes | yes | yes — grains, highstate, drift reconvergence, requisites |
| Linux amd64 | yes | yes, under emulation — 19 of 21 packages | partly — grains only |
| Linux arm64 | yes | no | no |
| macOS | yes | no | no |
| Windows | yes | no | no |

### 4.1 What the Linux runs did establish

The development host is FreeBSD with the Linux compat layer, linprocfs, and
linsysfs, so it executes Linux ELF binaries directly. `make test-linux`
cross-compiles the test binaries and runs them there: 23 of 25 packages pass, the CLI
tests among them. The two failures are the emulator rather than the code:

- `builtin/TestFileAccess` — the compat layer resolves a symlink's absolute
  target against the FreeBSD root, so a stat through the link fails while a
  stat of the same path string succeeds. Reduced to a nine-line Go program
  that reproduces it with no halite involved.
- `docsaudit` — shells out to the Go toolchain, which a cross-executed
  binary cannot reach.

Grain collection was the sharpest edge and is no longer theoretical. The
Linux grain code reads `/proc` and `/sys` where the FreeBSD code reads
sysctl — a separate implementation, previously never executed. Run here it
returns the same 63 keys the FreeBSD collector returns, no key unique to
either side, and every hardware fact agrees between them: the same CPU
model, the same 12 cores, the same 130902 MB. `os` comes back `Rocky` and
`os_family` `RedHat`, from the userland actually installed.

### 4.2 What it did not establish

The compat layer has no Linux package manager and no init: no `apt`,
`apt-get`, `dpkg`, `dnf`, `yum`, `rpm`, `apk`, `zypper`, `systemctl`,
`useradd`, `groupadd`, or `usermod`. Provider selection is by probing for
the binary, so the Linux binary correctly reached for the FreeBSD `pkg` and
`service` that are there and answered from them. That is the right
behaviour and it is also why the following remain **written and never
executed**:

- the apt, dnf, and apk providers of `pkg`
- the systemd provider of `service`, and `service.masked`
- the `useradd`/`groupadd`/`usermod` branch of `user` and `group`
- Linux `sysctl` handling, which differs from FreeBSD's

These need a real Linux host. Nothing short of one will exercise them.

### 4.3 The per-state `runas` and `umask` no-op

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
| `internal/yaml` | 96.2% | >90% branch — met on statements, unmeasured on branches |
| `internal/cli` | 93.4% | — |
| `internal/target` | 92.8% | >90% branch — met on statements, unmeasured on branches |
| `internal/state` | 90.7% | >90% branch — met on statements, unmeasured on branches |
| `internal/buildpolicy` | 90.7% | — |
| `internal/fileserver` | 89.4% | — |
| `internal/value` | 89.1% | — |
| `internal/states` | 85.9% | — |
| `internal/pillar` | 84.7% | — |
| `internal/render` | 83.7% | — |
| `internal/runner` | 83.3% | — |
| `internal/template` | 81.9% | **>90% branch — not met** |
| `internal/exec` | 81.4% | — |
| `internal/signature` | 80.9% | — |
| `internal/config` | 77.8% | — |
| `internal/grains` | 75.3% | — |
| `internal/migrate` | 69.4% | — |
| `cmd/halite-api` | 66.7% | — |
| `cmd/halite-node` | 54.5% | — |
| `cmd/halite-hub` | 49.2% | — |
| `internal/builtin` | 44.8% | — |
| `internal/specaudit`, `internal/docsaudit` | n/a | they test documents, not code |
| `internal/version` | 0% | — |

Whole tree: 71.5%.

`internal/template` is the one correctness-core package still short of the
bar on either metric. It is also the largest: roughly 130 filters, the
expression grammar, inheritance, and macros. Closing it is a matter of
volume rather than difficulty.

`internal/builtin` at 44.8% is structurally limited rather than neglected: a
large share of its statements need root, a package manager with a writable
database, or a service manager with services to stop. Raising it honestly
means the containerised integration suite of SPEC 31, which is phase 5 work.

The three `cmd` packages were 0% until this pass, on the reasoning that they
are argument dispatch over tested libraries and are covered by hand and by
the lab run. That reasoning was wrong, and testing them showed it within the
hour: `grains item a b c` resolved only `a`, `--fail-on` took a misspelled
level as the default and audited less than it was asked to, and the usage
text advertised a `grains setval` that had never existed. All three are the
same shape — a CLI that accepts input it does not honour — and none was
reachable from a library test, because the defect was in the dispatch.

They are tested now by re-executing the test binary as the command, so the
tests need no toolchain at run time and pass under the Linux run in section
4.1. What is still uncovered there is what needs a hub: the phase 2 and
phase 4 subcommands are stubs that report the phase, and that report is the
only behaviour they have to test.

### 5.2 The fourteen test layers

Every layer SPEC 31 requires beyond the unit layer of 5.1, and where each
stands. Four are present, one of them stronger than
specified, three are partial, four are absent, and one is unverified.

| Layer | Status |
|---|---|
| Conformance, YAML | **present.** All 402 cases of the suite's `data` branch run on every `go test`, vendored under `internal/yaml/testdata/yaml-test-suite/`. Each case is checked three ways: a document the suite calls invalid must be refused, one it calls valid must parse, and where the suite supplies `in.json` the parsed tree must match. Every disagreement has a row in a table giving its reason, enforced in both directions so a stale row fails as loudly as an unrecorded one. Standing: 328 of 402 agree, 34 disagree by design, 40 are gaps — see 5.4. The dialect SPEC 10.1 actually specifies is PyYAML's rather than the standard's, and that half is checked against PyYAML itself — see 5.8. |
| Conformance, templates | **present.** Two corpora under `internal/template/testdata/jinja-corpus/`, run on every `go test`. 198 cases are extracted mechanically from Jinja's own pytest suite, carrying each case's environment options; disagreements have a row apiece with a reason, enforced in both directions. 123 more are written here for what Jinja's tests cannot cover: Salt's added filters, the strict undefined of 10.2.6, the limits of 10.2.8, and the refusals the subset owes an operator — those carry no deviation table, because a case that fails there is one this project got wrong. Standing: 146 of 198 agree, 27 are outside the subset, 25 are gaps — see 5.5. |
| Differential against Salt | **partial.** `internal/saltdiff` compiles eight trees with both implementations and compares the low state: the chunk sequence first, then each chunk's arguments. It runs against Salt 3006.25 and 3008.2. The trees cover file and cmd states, a five-link requisite chain including a reversed requisite, Jinja loops and conditionals over pillar, include with extend, `names` expansion, explicit ordering, macros and filters, grain conditionals, and argument types end to end. Two deviations are recorded, each naming the Salt major it was observed under, because the majors disagree with each other about what `show_lowstate` projects. Standing: every tree agrees. SPEC 31 asks for three comparisons and this makes two: the state *results* of actually applying a tree are not compared — see 5.7. |
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
| `gapLenient` | 23 | halite parses a document the suite requires to be an error. This was called the safe direction, and the PyYAML differential of 5.8 showed the framing was wrong: a document the reference implementation refuses is one Salt would not load, so accepting it means the tree loads here and means something nobody wrote. What is left is mostly tabs in odd positions, document markers inside quoted scalars, and under-indented continuations. |
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

### 5.7 What the Salt differential covers, and what it does not

`make check` runs it against whichever `salt-call` is on PATH. It has
been run against Salt 3006.25 and 3008.2.

Compared, over nine trees:

- the low state: the chunk sequence first, then each chunk's arguments
- the pillar, with its merge across two files

Not compared:

- **the state results.** SPEC 31 asks for these and they are the half
  that would catch a module doing the wrong thing rather than a compiler
  planning the wrong thing. Applying a tree twice under both
  implementations and comparing `changes` needs a container to apply it
  in, which is the integration layer, which is phase 2.
- **a real estate's trees.** SPEC 31 says "a corpus of real SLS and
  pillar trees from this estate". These nine are written for the gate.
  They cover the constructs, not the volume, and volume is where the
  surprises are.
- **the renderers other than jinja|yaml**, and `#!py`, which is not
  implemented at all.

What it found on its first run, all now fixed: `order: first` refused
though Salt gives it the order 0; `user: 0` refused though Salt reads an
integer as a uid; `contents` as a list of lines refused by a signature
though the code behind it had always handled one; and a per-state
`timeout` parsed, stripped from the arguments, and then read by nothing.

The two recorded deviations are both schema rather than behaviour. Salt
passes `timeout` through to the module where halite reads it as a
per-state option of SPEC 11.7 and applies it there. And Salt 3006
resolves the reversed requisites while executing rather than while
compiling, so `show_lowstate` does not carry them; 3008 does, as halite
does. The two majors disagree with each other, so a deviation row names
the version it was observed under.

### 5.8 Where the PyYAML differential stands

SPEC 10.1 specifies the dialect as PyYAML's. 114 documents go to both
and the resolved type is compared as well as the value.

100 agree. Ten differ by design:

| Rule | Cases | Why |
|---|---|---|
| Duplicate keys are an error | 2 | SPEC 10.1.2. PyYAML takes the last silently. |
| A date stays a string | 4 | SPEC 10.1.3. A date becoming a struct breaks `file.managed` contents. |
| A sexagesimal stays a string | 3 | SPEC 10.1.3, with a lint warning where it would have differed. `12:30` is 750 under PyYAML. |
| Integers are int64 | 1 | Python's are unbounded. |

Four are gaps, and all four are the dangerous direction — halite reads a
document PyYAML refuses:

| Document | halite reads | PyYAML |
|---|---|---|
| `k: -` | a one-element sequence | refuses |
| `k: ?` | a mapping of null to null | refuses |
| `k: :` | a mapping of null to null | refuses |
| `a: b: c` | a nested mapping | refuses |

These are the same class as the 23 in 5.4: a tree Salt would not load
loads here and means something. They are the ones worth closing first.

### 5.9 What a real Salt tree found

The corpus in 5.7 is written for the gate: it covers constructs, not
volume, and this project's own author called that its weakest point. A
real tree — seventeen state files and eight pillar files running a small
estate of FreeBSD hosts — was pointed at halite on 2026-08-21. It found
more in an hour than the written corpus had in a day: eleven defects,
nine of which no test in this repository could have seen.

Fixed as a result:

| What | Why it mattered |
|---|---|
| The `salt` dispatcher was never bound | Both compilers carried the field, passed it to the renderer, and nothing ever set it, so `salt['pillar.get']` was undefined in every SLS and pillar file. The tree used it six times in four files. |
| A renderer stage after the serializer was dropped | `#!yaml|gpg` rendered as plain yaml and delivered the PGP armor as the value. Five of eight pillar files use it. |
| The `gpg` renderer did not exist | Which is what made the previous row a silent wrong answer rather than an error. Implemented as SPEC 12.6 specifies it. |
| `ignore_missing` in a pillar top | Parsed out of the SLS list and acted on by nothing, so a tree naming a pillar file per host failed to compile on every host missing one. |
| `ignore_missing` in a state top | Read as an SLS name and reported as an error. Salt accepts it there and ignores it. |
| Salt's short declaration | `apache24:` followed by a bare `pkg.latest`. Four of seventeen files. |
| `file.managed: template: jinja` | Six uses in five files. The source was written unrendered. |
| `sysrc.managed`, `cmd.script`, `git.latest`'s branch and force flags, `file.directory: dir_mode` | Named or accepted by Salt and not by this build. |
| The advice on an unquoted mode | Right for `0644` and wrong for `640`, where it suggested the octal of a number nobody wrote. |
| `user.present` took no `password` or `usergroup` | The tree sets its account password from an encrypted pillar value. |
| `file.replace` took no `bufsize` | Salt's names a read buffer. |
| The migration audit ignored state declarations | It reported the tree clean. Compiling it produced twenty-seven errors, twenty-two of them declarations. |
| The pillar top ignored `- match: grain` | The state top read it; the pillar top had its own copy that did not, so a `nodename:host` target was compiled as a glob, matched nothing, and the file was absent from the pillar with nothing reported. |
| An untrusted grain target was neither refused nor reported | SPEC 12.4's check looked for a `G@` sigil, which `- match: grain` does not use, so the target compiled against an empty grain set and delivered nothing. |
| `pillar_trusted_grains` was hub-only | Along with the two pillar merge settings, so a node compiling its own pillar masterless could not set the option it was about to be told to set. |

The password was left undone for one pass and then done carefully. The
value is a hash, and `usermod -p <hash>` puts it in the process table
where any unprivileged account on the machine can read it while the
command runs. `pw usermod -H 0` and `chpasswd -e` both take it on
standard input, which is the only way this module writes one; a test
asserts the hash is absent from the argument vector on both platforms.
It is not logged, not returned in `changes`, and not in any comment,
because a job return carrying a hash is a hash in every returner, event
bus, and log the estate has. Comparing it needs the root-only hash file,
and a state that cannot read it reports that rather than claiming a
change it cannot verify — which would never converge.

`bufsize` produced the third answer: a parameter may now declare itself
**ineffective** and say why. The tree compiles, the compiler warns once
at the line that wrote it, and the audit reports a note. Refusing a
harmless argument stops a tree Salt runs; accepting it silently is the
defect in 5.3 that this project keeps finding in itself.

**The tree compiles.** With its state files corrected by its author on
2026-08-22 and the fixes above, `state show_lowstate` produces 51 chunks
across the 11 SLS files this host matches. That is the first time an
estate's real tree has been compiled by halite end to end.

What that does *not* mean is that it has been applied. Compilation
proves the tree is understood, not that the modules do the right thing
with it; SPEC 31's differential compares the low state and the pillar
and stops short of the results for the same reason (5.7). Nothing in
this tree has been run.

Still open, from the same tree:

- **`mode: 640`** is refused, correctly: it is the integer 640, and the
  tree means the mode 0640. Salt happens to get this right by reading
  the decimal digits as octal, and gets `mode: 0644` wrong the same way,
  silently applying 0420.
- **The tree's own `mkdirs`** is not a Salt argument — the name is
  `makedirs` — so Salt has been ignoring it in four places. halite
  reports it, which is the audit working rather than a gap.
- **The pillar decrypts, but not from this session.** The `gpg` renderer
  of SPEC 12.6 is implemented, and pointed at the tree it finds every
  encrypted value, invokes gpg, and reports the exact key path of each
  failure. It fails here because the tree's private key lives in Salt's
  own keyring at `/usr/local/etc/salt/gpgkeys`, which is root-owned
  0700, and this session is unprivileged. A node runs as root, so this
  is the environment rather than a gap — but it does mean the decryption
  path has been exercised only against a throwaway keyring generated by
  its own test, and never against the tree it was written for.

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

The differential gate against Salt has moved off this list's top: it
runs. What is left of it is the half that needs a container, which is
phase 2 work, so it sits at 2 rather than 1.

The template conformance suite stays at the bottom. It is running and
down to isolated cases — 25, largest class 9 — with no cluster left. The
YAML suite has moved *up*, not because the count changed but because the
reason changed: see 3.

1. **A Linux host.** The compat layer got the platform-neutral code and
   the `/proc` grain collector run under Linux (4.1), which was the part
   that could be got cheaply. What is left needs a real one: the apt, dnf,
   and apk providers, the systemd provider, and `useradd`. 60 of the 62
   platform modules of SPEC 15.3 wait behind it, and so does the other
   half of every optional provider capability written in this pass —
   holding, upgrading, and file ownership exist for pkgng because pkgng is
   what this host runs.
2. **More real trees** (5.9). One was pointed at halite and found ten
   defects in an hour, against a written corpus that had found four in a
   day. The written corpus covers constructs; a real tree covers what
   people write. This is the cheapest finding-per-hour on the list by a
   wide margin and needs no new machinery — only trees. The state-results
   half of the gate (5.7) still waits on a container to apply one in,
   which is phase 2.
3. **The documents accepted that should be refused** (5.4 and 5.8). 23
   in the conformance suite and 4 in the PyYAML differential, and the
   framing in earlier versions of this file was wrong: accepting a
   document the reference implementation refuses is not the safe
   direction. It means a tree Salt would not load loads here and means
   something. `a: b: c` becoming a nested mapping is the sharpest of
   them.
4. **The remaining 25 template conformance gaps** (5.5). The largest class
   is 9, and it is float and dict spelling.
