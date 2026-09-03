# Divergence and gaps

What halite does that SPEC.md does not say, what SPEC.md says that halite does
not yet do, and why in each case.

This file is checked mechanically. `internal/specaudit` parses the module
tables in SPEC.md sections 15.2 through 15.5, compares them against the
registries an actual build ships, and fails if a module is neither
implemented nor recorded here, or if a gap recorded here has since been
filled. A stale entry below is a test failure, not a documentation problem.

**Status as of this writing:** SPEC section 32 phases 0 through 4 are
complete and phase 5 is part built — gitfs, s3fs, the agentless path,
relays and the FIPS artifact set are in; Windows and macOS parity is not
started. Phase 6 has not started.

The development host is FreeBSD 15.1 on amd64, and most of what follows
was verified there. It is no longer the only platform anything has run
on: a real Ubuntu node enrolled with this estate's hub and applied a
highstate through it (4.5), and the tree builds natively on macOS
without having been run there (4.4a). Section 4 is the authority on
which claim rests on what.

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
`TestSingleLetterYNStayStrings`.

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

### 1.6 The template random seed is per node and template, not per job

SPEC 10.2.4 says `random`, `shuffle`, and `rand_str` are "seeded per
render from a deterministic seed derived from the node ID and the job ID
by default, so that a `test=True` run and the subsequent real run
agree."

A `--test` run and the real run that follows it are two invocations with
two job IDs, so a seed containing the job ID guarantees they disagree.
The mechanism defeats the purpose in the same sentence that states it,
and the purpose is the point: `random` in a template must not produce a
phantom diff on every run. Implemented literally, it did — the two runs
drew different numbers, which is exactly what the feature exists to
prevent and what `docs/migrating-from-salt.md` promises it does not do.

The seed is the node ID and the template's path. It is stable across
runs, varies between machines, and varies between files so two templates
do not draw in step. `random_seed: nondeterministic` still restores
Salt's unseeded behaviour.

This is the fourth place SPEC states a fact that can be checked and is
wrong; the others are 1.1, and the `0o17` and `1e3` resolutions in 5.8.
The pattern is worth naming: where the specification gives a mechanism
*and* the reason for it, and the two disagree, the reason is what the
tree depends on.

---

### 1.7 The ALPN identifier is required in the offer, not selected

SPEC 6.1 gives the ALPN protocol identifier as `halite/1` and asks that
a peer which does not offer it be rejected at the handshake.

`net/http` runs its bundled HTTP/2 server only for the exact identifier
`h2`. A connection that negotiates any other non-empty protocol is
closed after the handshake without a byte of HTTP being read
(`net/http.(*conn).serve`, guarded by `validNextProto`), and no exported
API registers a second name for the HTTP/2 handler. Selecting
`halite/1` therefore means either serving HTTP/1.1 — losing the
multiplexing the subscribe stream depends on — or vendoring an HTTP/2
implementation, which section 4.2 forbids.

So the identifier is mandatory in the *offer* and `h2` is what gets
selected. `transport.requireProtocol` refuses a ClientHello whose
`SupportedProtos` does not contain `halite/1`, before a certificate is
exchanged, which is the effect section 6.1 asks the identifier for: a
stray HTTPS client is rejected at the handshake and never reaches an
endpoint. What is lost is the ability to read the identifier off a
packet capture of the ServerHello; it is still in the ClientHello.

`transport.Negotiated` is the selected name, and it is a constant so the
two cannot be confused at a call site.

The contract a peer has to meet is therefore: **offer `halite/1`, and
offer `h2` or `http/1.1` as well**. halite's own client offers
`halite/1,h2` and gets HTTP/2. Anything probing `/v1/health` by hand
needs the same:

```sh
printf 'GET /v1/health HTTP/1.1\r\nHost: hub\r\nConnection: close\r\n\r\n' |
  openssl s_client -connect hub:4510 -alpn halite/1,http/1.1 -CAfile ca.crt -quiet
```

A client that does not offer `halite/1` -- `curl` with its defaults, a
browser, a load balancer's HTTPS check, Prometheus -- is refused at the
handshake. Measured against the running hub, the alert depends on what
was offered:

| What the client offers | What it gets |
|---|---|
| nothing, or `h2` alone | `tlsv1 alert internal error` |
| `halite/1` alone | `no application protocol` |
| `halite/1` and `h2` | connects, and `h2` is selected |

`internal error` is the one an operator meets, and it says nothing about
ALPN, certificates, or halite. It is what `requireProtocol` returning an
error from `GetConfigForClient` becomes on the wire: TLS 1.3 has no way
to carry a reason, so there is no better alert to send. This entry said
`no application protocol` until 2026-08-29, which is the answer for an
offer no real client makes, and would have sent anyone debugging the
common case looking in the wrong place.

That is the gate working, and it is worth knowing before a health check
or a scraper is pointed at the port: `/v1/health` is reachable without a
*certificate*, which is what SPEC 6.2 says, and not without the
*protocol*. A scraper reaches the hub's metrics through `halite-api`,
whose listener is ordinary TLS; operations.md says how.

### 1.8 A relay does not forward what it was asked to do itself

SPEC 5.3 says a relay serves its own nodes and presents itself upstream
as one client. It does not say what happens to a job submitted to the
relay directly, and the two readings behave very differently.

This build keeps such a job local. The relay records every job it
forwards down with its own identity as the submitter, and forwards a
return upstream only for those. A job an operator submits to the relay
runs on its subordinates and is filed in the relay's cache alone.

The alternative — announcing locally-submitted jobs upstream so the
whole estate is visible from one place — was not taken, because the
upstream cannot then distinguish a job it authorized from one a relay's
own policy authorized, and its job cache stops being a record of what it
dispatched. The cost is real and worth stating: an operator upstream
cannot see a job run from a relay's own command line. The relay's `jobs
list` is where those live.

The behaviour is not cosmetic. A return for a job the upstream never
dispatched is refused as an unknown jid, and before this rule such
returns sat at the head of the spool being retried for ever.

### 1.9 A relayed node has no key on the upstream

The relay issues its subordinates' certificates, so the upstream holds
no key for them and cannot verify one. What it holds instead is the
relay's assertion, bounded by policy: a hub accepts subordinates only
from a certificate its policy grants `relay.proxy`, and refuses a return
naming a node that relay has not claimed.

This is what makes the arrangement worth having — a segment behind a
relay is administered by that relay — and it is also the trust that has
to be understood before one is deployed. A compromised relay can claim
any node id its upstream does not already hold directly, and file
returns for what it claims. It cannot claim a node connected to the
upstream itself; that check is explicit, because silently shadowing a
real node would be the worst version of this.

### 1.10 `GODEBUG=fips140=on` does not enforce, so this build does

SPEC 27.4 has the service unit run with `GODEBUG=fips140=on` and then
describes what holds "in FIPS mode": approved cipher suites and P-256 or
P-384 key exchange, no Ed25519, no SHA-1 and therefore no TOTP.

Those are the semantics of `fips140=only`, not of `on`. Measured on the
toolchain this is built with: `on` routes approved algorithms through
the module and leaves the rest reachable — HMAC-SHA-1 computes a digest
quite happily — while `only` rejects them, by panicking rather than by
returning an error. `crypto/fips140.Enforced()` reports false under `on`
and true under `only`.

The restrictions are therefore applied by this build rather than assumed
from the setting. `internal/fips.Restricted()` is keyed on FIPS mode
being on at all, and the Ed25519 refusal, the TOTP refusal, and the
curve preference are halite's own. The service unit still sets `on`, as
the specification says, because the value it adds is different from
enforcement: it states the mode rather than inheriting it, so a
`GODEBUG=fips140=off` in the environment cannot quietly turn a FIPS
deployment into a non-FIPS one.

One half is left to the module. TLS 1.3 cipher suites are not
configurable in Go — `tls.Config.CipherSuites` is ignored for 1.3 — so
the exclusion of `TLS_CHACHA20_POLY1305_SHA256` is the module's doing
and not this build's. The two suites SPEC 26.1 names are what a FIPS hub
negotiates; 5.15 records that measured against a foreign client.

That `only` panics rather than erroring is why the TOTP refusal is
load-bearing rather than cosmetic: without it, a login against an
account with a second factor takes down the handler.

### 1.11 The FIPS grain is a pair, not one grain

SPEC 27.4 says the `fips_mode` grain "reports both the host's kernel
FIPS state and the binary's own mode". They are reported as separate
grains here: `fips_mode` stays the boolean it was — the host kernel —
and `fips_build`, `fips_enabled`, and `fips_module` carry the process's
own state.

`fips_mode` is in SPEC 12.4's default `pillar_trusted_grains` and is
what trees target on and templates branch on. Turning it into a map
would make `{% if grains.fips_mode %}` true on every host in the estate,
including every host where it is false today — a silent inversion in
whichever trees already use it, found at apply time.

The pair is what makes the mismatch SPEC asks `doctor` to warn about
visible at all: a FIPS kernel running a non-FIPS binary, or the reverse,
is a deployment mistake neither fact finds alone. `doctor` itself is not
built, so nothing warns yet; the grains are there for a tree to assert
on in the meantime.

None of the four is evidence to an assessor. They are grains, which is
to say a node's own account of itself, and a node that is lying about
its cryptography is a node that can lie about this too. What the grains
are good for is inventory — finding the hosts that need attention — and
the artifact's own `version` output is what says what a binary is.

### 1.12 First contact is a pinned fingerprint, and nothing else is optional

SPEC 7.3 describes a CA delivered to the node by one route and a
fingerprint delivered by another, the fingerprint existing "so that a CA
file substituted in transit is caught here rather than never". It does
not say the CA cannot come from the hub itself.

This build takes the fingerprint as the whole of the trust decision. A
node with no pinned CA fetches one from the hub and accepts it only if
it matches, so the operator distributes one short string rather than a
string and a file. `hub_ca_file` and `--ca-file` still take a CA
delivered by another route, and neither removes the fingerprint
requirement: a CA the node has not already pinned is one it is being
asked to start trusting, however it arrived. Only a CA already written
into `pki_dir` is exempt, because it was checked when it was written —
which is why `connect` and `renew` need no fingerprint.

`hub_fingerprint` was optional. That was the weaker position and it read
as the safer one: a node could be given a CA file and check nothing at
all, trusting whatever route the file took, and nothing said so. There
is now no mode that skips the check, because the guarantee *is* the
check — a missing fingerprint is not a looser one, it is none.

The fetch is the only place in the build that sets
`InsecureSkipVerify`, and it does more than the default verifier rather
than less. Inside the handshake it finds a certificate in the presented
chain whose fingerprint matches the pin, then verifies the hub's own
certificate against a pool holding that certificate alone. A chain that
fails either step fails the connection, so no caller can forget to check
afterwards.

Both steps carry weight. The CA is public — `/v1/enroll` returns it and
every enrolled node holds a copy — so an attacker can put the real CA in
a chain beside their own certificate. Matching the fingerprint somewhere
in the chain is therefore not evidence of anything; without verifying
the leaf against the matched CA the node would pin the right CA and
still be talking to the wrong hub. `TestAForeignLeafBesideTheRealCAIsRefused`
is that exact scenario, and it fails when the verification is removed.

What this rests on, stated plainly: SHA-256 preimage resistance, and the
operator delivering the fingerprint by a route the attacker does not
control. The second is the assumption worth being deliberate about — it
is the same one SPEC 7.3 already makes, and the same one an SSH host key
fingerprint makes.

What has not been established: none of this has been run against a
hostile network, only against a hostile chain assembled in a test. The
hub now sends its CA to any client that completes a TLS handshake with
it, which is a certificate it already returned at enrollment, but is a
larger unauthenticated surface than before by one certificate.

---

## 2. Module coverage

The build ships **44 execution modules / 249 functions** and **23 state
modules / 64 functions**.

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
| `file` | implemented | 40 | |
| `git` | implemented | 5 | through the system `git` binary |
| `grains` | implemented | 7 | |
| `group` | implemented | 1 | |
| `hashutil` | implemented | 9 | |
| `hosts` | implemented | 3 | |
| `mine` | implemented | 6 | the store is on the hub; a node publishes its own and reads others' through the RBAC policy |
| `mount` | implemented | 1 | read-only; `mount`/`umount`/`fstab` not written |
| `network` | implemented | 5 | |
| `pillar` | implemented | 5 | |
| `pkg` | implemented | 18 | FreeBSD `pkg` provider only; see 2.5. `version_cmp` implements the Debian and RPM orderings directly and asks pkg(8) for FreeBSD's |
| `random` | implemented | 3 | `crypto/rand` |
| `saltutil` | implemented | 9 | |
| `service` | implemented | 16 | FreeBSD rc provider only; see 2.5 |
| `ssh_auth` | implemented | 1 | registered as `ssh.auth_keys` |
| `status` | implemented | 4 | |
| `sys` | implemented | 9 | |
| `sysctl` | implemented | 3 | |
| `sysrc` | implemented | 3 | FreeBSD; SPEC lists it as core |
| `test` | implemented | 5 | |
| `timezone` | implemented | 1 | read-only; `set_zone` not written |
| `user` | implemented | 3 | reads through `os/user`, writes through `pw` or `useradd` |
| `at` | not implemented | 0 | |
| `acl` | not implemented | 0 | POSIX ACL reading needs `acl_get_file`, which is cgo on FreeBSD; needs the `getfacl` binary path instead |
| `apparmor` | not implemented | 0 | Linux only; no host to verify on |
| `beacons` | implemented | 10 | `list` answers from the registry and the configuration; the nine that change a running node's watchers name the phase they arrive in |
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
| `schedule` | implemented | 12 | `list` and `show_next_fire_time` answer from the configuration; the ten that change a running node's schedule name the phase they arrive in |
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
| `file` | implemented | 15 | |
| `git` | implemented | 1 | |
| `group` | implemented | 2 | |
| `host` | implemented | 2 | |
| `module` | implemented | 2 | |
| `pkg` | implemented | 4 | |
| `service` | implemented | 4 | |
| `ssh_auth` | implemented | 2 | |
| `sysctl` | implemented | 1 | |
| `sysrc` | implemented | 3 | not in SPEC 15.5; FreeBSD's equivalent of the `hostname`/`service`-enable states. `managed` is Salt's name and `present` is this build's, and they are the same function |
| `test` | implemented | 6 | |
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
| `schedule` | implemented | 1 | `absent`; the runtime control is in the execution module |
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

2 of 65 present — the rows below total 63 absent. This is the largest
single gap and it is a direct consequence of having one host to develop
on.

The 63 are declared as pending rather than simply missing. A name absent
from the registry makes "not written yet" and "you have mistyped it" the
same message, and the second sends an operator looking for a spelling
error that is not there:

```
$ halite-node call aptpkg.install nginx
halite: "aptpkg.install" is not built: SPEC section 15.3 names the aptpkg
module among the debian platform modules, and this build does not have it
(phase 5, with the Debian and Ubuntu platform work)
```

A test holds that table to SPEC 15.3 in both directions, so a module
that arrives cannot stay listed as pending and one added to the
specification cannot be quietly missed.

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

A module restricted to a platform refuses on any other by name, rather
than reaching the module and reporting a missing binary: the Linux
binary answers `sysrc.get runs on freebsd, and this node is linux`.
Checked on 2026-08-29 by running the Linux build, which is also what
makes `builtin/TestSysrcPresent` fail there — the refusal working, and
the test not expecting it.

Three numbers in this section were stale when audited on that date: the
packages passing under emulation, the grain key count, and the number of
restricted functions. What is written down now is the property in each
case, because a count in prose goes stale the first time the thing is
added to and nobody re-reads the sentence.

| Platform | Compiles | Unit tests run | Verified against a real system |
|---|---|---|---|
| FreeBSD amd64 | yes | yes | yes — grains, highstate, drift reconvergence, requisites |
| Linux amd64 | yes | yes, under emulation — all but three packages, named in 4.1 | yes — a node enrolled with a hub, highstate applied, under systemd (Ubuntu; see 4.5) |
| Linux arm64 | yes | no | no |
| macOS | yes, since 2026-08-29, and built natively on one | no | no |
| Windows | yes | no | no |

### 4.0 Where each platform keeps its files

SPEC 27.3 states the layout in Linux FHS terms and 27.5 puts Windows
configuration under `%PROGRAMDATA%`. The BSD and Windows conventions are
both departures from the literal text, for the same reason: a file no
administrator on that platform would think to look for is a file that is
effectively missing.

| Platform | Configuration | Durable state | Sockets |
|---|---|---|---|
| Linux | `/etc/halite` | `/var/lib/halite` | `/run/halite` |
| FreeBSD and the other BSDs | `/usr/local/etc/halite` | `/var/db/halite` | `/var/run/halite` |
| macOS | `/etc/halite` | `/var/lib/halite` | `/run/halite` |
| Windows | `%PROGRAMDATA%\Halite` | `%PROGRAMDATA%\Halite\lib` | `%PROGRAMDATA%\Halite\run` |

Windows had no case at all until 2026-08-26. It fell through to the FHS
branch, and `filepath.Join("/etc", "halite")` is `\etc\halite` there —
so a Windows node would have kept its configuration, its enrollment key,
and its cache in three directories off the root of whichever drive the
process happened to start in, none of them the one the `.msi` is
specified to create. Nothing caught it because the test asserting the
default asserted `/etc/halite` for every platform that was not a BSD.

The layout is now computed from the target rather than from
`runtime.GOOS` alone, so all four platforms are checked from one host —
`RootFor`, `VarPathFor`, `RunPathFor` — and the table in
`getting-started.md` is checked against them. That is a check on the
paths, not on the platform: it says the code and the documentation agree
about Windows, not that halite works there.

macOS takes the Linux paths deliberately. Homebrew's prefix is not fixed,
so `/etc` is the honest default rather than a guess at one.

### 4.1 What the Linux runs did establish

The development host is FreeBSD with the Linux compat layer, linprocfs, and
linsysfs, so it executes Linux ELF binaries directly. `make test-linux`
cross-compiles the test binaries and runs them there: every package but
three passes, the CLI tests among them. A count is not given because the
last two written down here were both stale — the package list grows and
nobody re-reads the sentence. Which three, and why each is the emulator
rather than the code:

- `builtin/TestFileAccess` — the compat layer resolves a symlink's absolute
  target against the FreeBSD root, so a stat through the link fails while a
  stat of the same path string succeeds. Reduced to a nine-line Go program
  that reproduces it with no halite involved.
- `builtin/TestSysrcPresent` — the restriction working, and the test not
  knowing it: the module answers `sysrc.present runs on freebsd, and this
  node is linux`, which is the behaviour the top of this section
  describes. The test asserts the FreeBSD path.
- `builtin/TestCurrentHashSaysWhenItCannotRead` — expects a refusal about
  privilege and gets one about absence, because the compat layer has no
  `/etc/shadow` at all.
- `docsaudit` — shells out to the Go toolchain, which a cross-executed
  binary cannot reach.
- `gitfs` — shells out to `git`, and the native binary sees a different
  `/tmp` than the emulated one that created the repository. The same path
  resolution as `TestFileAccess`, one process further out.

Grain collection was the sharpest edge and is no longer theoretical. The
Linux grain code reads `/proc` and `/sys` where the FreeBSD code reads
sysctl — a separate implementation, previously never executed. Run here it
returns the same key set the FreeBSD collector returns, with no key
unique to either side, and every hardware fact agrees between them: the
same CPU model, the same core count, the same memory. Re-checked on
2026-08-29 after the FIPS grains were added — the count had moved from
63 to 66 and the sentence still said 63, so what is recorded now is the
property rather than the number. `os` comes back `Rocky` and
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


### 4.4 `parallel: True` runs in order

SPEC section 11.7 lists `parallel` among the per-state options, and Salt
runs such a state in a separate process so that two slow states overlap.
This build parses the option into the low state and runs every chunk in
one order, one at a time.

It was read by nothing and said nothing, which is the accept-but-ignore
defect: a tree using it to overlap two slow states got neither the
overlap nor a word about it. The compiler now warns at the line that
wrote it. Warning rather than refusing, because running a parallel state
in order is correct, only slower — and refusing would stop a tree Salt
runs.

### 4.4a macOS did not compile at all

The matrix said macOS compiled from the beginning. It did not, and
nothing noticed until somebody ran `make` on one: the width of
`syscall.Rlimit`'s fields was declared by build tag, and macOS was
grouped with the BSDs. It is right about nearly everything and wrong
about that — `int64` on FreeBSD and NetBSD, `uint64` on Linux, macOS,
and OpenBSD — so `internal/bridge` failed to build and took the whole
tree with it.

The type is no longer declared anywhere. The field is taken by pointer
and the compiler supplies its width, which removes the class rather than
the instance.

`make build` compiles for the host alone, so a cross-platform break is
invisible to it, and `make cross` is a release step nobody runs while
working. `make build-all` compiles every shipped target and is part of
`make check` now, which is what should have been true before the claim
"macOS: compiles" was written down.

The fix was cross-compiled here and then built natively on a Mac, which
is the difference between the claim the matrix used to make and one
worth writing down. Nothing has been *run* there: `pkg`, `service`, and
everything under `mac_*` are as unexercised as they were.

OpenBSD still does not build — `syscall.RLIMIT_AS` does not exist there
— and is not in the shipped target list, so nothing claims it does.

### 4.5 What a real Linux node established

On 2026-08-28 an Ubuntu host enrolled with this estate's hub and applied
a highstate through it with no errors. It runs from the shipped systemd
units.

That is the first time several things have been exercised anywhere:

- **The Linux node path end to end.** Enrollment, the subscribe stream,
  the hub's file server, hub-compiled pillar, a state run, and its
  return filed in the job cache — on a machine that is not the
  development host and not the compat layer.
- **A package provider on a real Linux userland.** 4.2 records that the
  compat layer has no `apt` and no `dpkg`, so provider selection there
  chose nothing. On Ubuntu it chose, and a highstate that installs
  packages converged.
- **The systemd units.** They had never been run at all — every claim
  about them until now was read off the file. `ExecStart`, the sandbox
  settings, and `RestartPreventExitStatus=1` hold up under an actual
  service manager.

What it does not establish, and 4.2 still stands for the rest:

- One distribution. Ubuntu chooses `apt`; the `dnf`, `zypper`, and `apk`
  providers remain unexercised, and `os_family` branching in a tree is
  the commonest thing to get wrong across them.
- One architecture. Linux arm64 still compiles and nothing more.
- The node only. The hub and the API have not been run on Linux, so
  their units, their sandboxes, and `StateDirectory=halite-api` are
  still only read rather than run.
- One run. Nothing here says what a restart, a revocation, a certificate
  renewal, or a week of scheduled highstates does on that host.
- Not FIPS. The `-fips` artifacts ship for Linux and have been run
  nowhere.

## 5. Test coverage against SPEC 31

### 5.1 Branch coverage

SPEC 31 holds the YAML parser, the template engine, the state compiler, and
the targeting matcher to **branch coverage above 90%**. Go's tooling measures
statement coverage, not branch coverage, so the numbers below are not the
same metric and are, in general, more forgiving than the bar asked for.

These figures were re-measured with `make cover` on 2026-08-30, against
the whole tree as it stands. Unlike the module tables above they are not
machine checked, because measuring coverage requires running the suite
that would be doing the checking — so they are a snapshot, and the date
is part of the claim.

| Package | Statement coverage | SPEC 31 bar |
|---|---|---|
| `internal/regexcompat` | 100.0% | — |
| `internal/metrics` | 96.9% | — |
| `internal/yaml` | 96.3% | >90% branch — met on statements, unmeasured on branches |
| `internal/redact` | 95.5% | — |
| `internal/state` | 90.1% | >90% branch — met on statements, unmeasured on branches |
| `internal/value` | 89.9% | — |
| `internal/target` | 89.2% | **>90% branch — not met** |
| `internal/log` | 89.0% | — |
| `internal/buildpolicy` | 87.9% | — |
| `internal/cli` | 86.2% | — |
| `internal/render` | 86.2% | — |
| `internal/pillar` | 85.5% | — |
| `internal/states` | 85.2% | — |
| `internal/template` | 82.0% | **>90% branch — not met** |
| `internal/config` | 79.8% | — |
| `internal/eventbus` | 79.8% | — |
| `internal/signature` | 79.5% | — |
| `internal/migrate` | 79.2% | — |
| `internal/apitoken` | 78.9% | — |
| `internal/extension` | 78.9% | — |
| `internal/ldap` | 78.7% | — |
| `internal/oidc` | 78.7% | — |
| `internal/policy` | 78.0% | — |
| `internal/s3fs` | 77.8% | — |
| `internal/job` | 76.9% | — |
| `internal/grains` | 75.5% | — |
| `internal/exec` | 75.1% | — |
| `internal/gitfs` | 75.1% | — |
| `internal/account` | 74.6% | — |
| `internal/schedule` | 73.3% | — |
| `internal/runner` | 69.4% | — |
| `internal/roster` | 69.2% | — |
| `internal/websocket` | 69.0% | — |
| `internal/keystore` | 67.2% | — |
| `internal/hub` | 66.1% | — |
| `internal/api` | 65.7% | — |
| `internal/returner` | 65.5% | — |
| `internal/bridge` | 59.2% | — |
| `internal/beacon` | 56.0% | — |
| `internal/awsauth` | 48.4% | — |
| `internal/fips` | 47.4% | — |
| `internal/builtin` | 43.1% | — |
| `internal/fileserver` | 41.8% | — |
| `internal/pki` | 34.6% | — |
| `internal/relay` | 31.8% | — |
| `cmd/halite-node` | 29.5% | — |
| `internal/transport` | 20.5% | — |
| `internal/sshexec` | 17.4% | — |
| `cmd/halite-api` | 11.8% | — |
| `cmd/halite-hub` | 10.9% | — |
| `internal/specaudit`, `internal/docsaudit` | n/a | they test documents, not code |
| `internal/version` | 0% | — |

Whole tree: 62.5%.

**It was 71.5% when this table was first written, and the fall is the
finding.** Nothing was deleted and no test was removed: phases 4 and 5
added the API, the relay, gitfs, s3fs, the agentless path, the extension
bridge and the schedulers, and the suite did not grow with them. A
percentage that drops while the tests all pass is the only signal that
says so, which is why the number is kept here rather than quietly
re-measured.

Two of SPEC 31's four correctness-core packages are now short of the
bar, where the first measurement had one:

- `internal/template` at 82.0%, roughly where it was. It is the largest
  of the four — about 130 filters, the expression grammar, inheritance
  and macros — and closing it is volume rather than difficulty.
- `internal/target` at 89.2%, **down from 92.8% and now below the bar it
  used to meet.** The matcher grew compound targeting, the `-G`/`-E`/`-L`
  forms and the roster paths without matching tests. This is a
  regression against SPEC 31 rather than a gap never closed, and it is
  the more urgent of the two for that reason.

`internal/relay` at 31.8%, `internal/transport` at 20.5% and
`internal/sshexec` at 17.4% are the largest untested surfaces added
since. All three are exercised by the lab runs of 5.11 and 5.14 rather
than by unit tests, which is why the defects those runs found — the nil
`Fleet`, the untargetable relayed node, the discarded spool — were not
reachable from the suite.

`internal/builtin` at 43.1% is structurally limited rather than
neglected: a large share of its statements need root, a package manager
with a writable database, or a service manager with services to stop.
Raising it honestly means the containerised integration suite of SPEC
31, which is phase 5 work.

The three `cmd` packages tell the sharpest version of the story. They
were 0% until the pass described below, then 49–67%, and are now
10.9–29.5% — not because tests were lost, but because `serve` grew
relays and FIPS, `run` grew batching and targeting flags, `ssh` and
`orch` and the API's `token` and `account` subcommands arrived, and none
of it was tested. The reasoning that first put them at 0% was that they
are argument dispatch over tested libraries, covered by hand and by the
lab run. That was wrong, and testing them showed it within the hour:
`grains item a b c` resolved only `a`, `--fail-on` took a misspelled
level as the default and audited less than it was asked to, and the
usage text advertised a `grains setval` that had never existed.

It was wrong again on 2026-08-30, in the same packages and the same
shape: every unknown flag was accepted and dropped, so
`policy test --policy other.yaml` evaluated the configured file and
exited 0. Twice is the argument against the reasoning, not against the
instance — see 5.24.

They are tested by re-executing the test binary as the command, so the
tests need no toolchain at run time and pass under the Linux run in
section 4.1. What is still uncovered is what needs a hub.

### 5.2 The fourteen test layers

Every layer SPEC 31 requires beyond the unit layer of 5.1, and where each
stands. Five are present, one of them stronger than
specified, four are partial, and four are absent. Nothing is unverified
any more: the reproducibility layer was, and it is now partial with the
limit stated rather than the question left open.

| Layer | Status |
|---|---|
| Conformance, YAML | **present.** All 402 cases of the suite's `data` branch run on every `go test`, vendored under `internal/yaml/testdata/yaml-test-suite/`. Each case is checked three ways: a document the suite calls invalid must be refused, one it calls valid must parse, and where the suite supplies `in.json` the parsed tree must match. Every disagreement has a row in a table giving its reason, enforced in both directions so a stale row fails as loudly as an unrecorded one. Standing: 331 of 402 agree, 34 disagree by design, 37 are gaps — see 5.4. The dialect SPEC 10.1 actually specifies is PyYAML's rather than the standard's, and that half is checked against PyYAML itself — see 5.8. |
| Conformance, templates | **present.** Two corpora under `internal/template/testdata/jinja-corpus/`, run on every `go test`. 198 cases are extracted mechanically from Jinja's own pytest suite, carrying each case's environment options; disagreements have a row apiece with a reason, enforced in both directions. 123 more are written here for what Jinja's tests cannot cover: Salt's added filters, the strict undefined of 10.2.6, the limits of 10.2.8, and the refusals the subset owes an operator — those carry no deviation table, because a case that fails there is one this project got wrong. Standing: 157 of 198 agree, 26 are outside the subset, 15 are gaps — see 5.5. |
| Differential against Salt | **partial.** `internal/saltdiff` compiles ten trees with both implementations and compares the low state: the chunk sequence first, then each chunk's arguments. It runs against Salt 3006.25 and 3008.2. The trees cover file and cmd states, a five-link requisite chain including a reversed requisite, Jinja loops and conditionals over pillar, include with extend, `names` expansion, explicit ordering, macros and filters, grain conditionals, and argument types end to end. Two deviations are recorded, each naming the Salt major it was observed under, because the majors disagree with each other about what `show_lowstate` projects. Standing: every tree agrees. It makes all three comparisons SPEC 31 asks for, with the third — the state results — compared as test-mode *predictions* rather than as the results of an apply, which still needs somewhere to apply a tree. See 5.7. |
| Differential, version comparison | **partial.** `pkg.version_cmp` exists, with the Debian and RPM orderings implemented directly and FreeBSD's asked of pkg(8), since libpkg is its own specification. The FreeBSD half of the differential is real and runs here: 14 pairs go to `pkg version -t` and to halite and must agree, and the test skips loudly rather than passing quietly where pkg(8) is absent. The Debian and RPM halves need a Debian or RHEL host for `dpkg --compare-versions` and `rpmdev-vercmp`; until then they are tested against those projects' own published vectors, which are the cases the algorithms are known to get wrong. |
| Conformance, state modules | **present** and stronger than specified — see 1.4. Covers 6 of the 46 state functions. |
| Property | **present** for all five named properties, each checked over generated input rather than a fixed corpus: path containment never escapes a root (`internal/fileserver/property_test.go`, 23000 generated paths plus the symlink cases), the topological sort is stable, requisite resolution terminates, and a requisite genuinely orders its target (`internal/state/property_test.go`, over random requisite graphs including cycles), the YAML parser never panics (`internal/yaml/property_test.go`, 50000 generated documents), and targeting is monotonic under grain addition (`internal/target/property_test.go`, 20000 expression and node pairs). Negation is asserted as the documented exception to monotonicity rather than left implicit. |
| Fuzz | **present** for three of the eight named targets: the YAML parser and its encoder, the template lexer and parser, and the compound target parser. `make fuzz` runs all seven functions; `make fuzz FUZZTIME=30m` is a campaign. The first run found four defects, listed in 5.3 below. Still absent: the wire message decoder, the cron parser, the roster parser, and the bridge protocol decoder, all of which belong to phases that have not started. |
| Integration | **absent.** No containerised hub-plus-nodes harness. Blocked on phase 2. |
| Scale | **absent.** Blocked on phase 2. |
| Upgrade | **absent.** Nothing to upgrade from. |
| Chaos | **absent.** Blocked on phase 2. |
| Security | **partial.** The dependency-graph assertion of 4.2 is implemented and enforced (`internal/buildpolicy`, `make policy`), and `make vuln` runs `govulncheck`. It is not part of `make check`, because it fetches the tool and the vulnerability database and `check` has to work on the machine a release is built on, which has no network and `GOPROXY=off`. With no third-party dependencies it scans the Go standard library and nothing else, which makes it a check on the toolchain rather than on a supply chain — a smaller claim than the name suggests, and the one worth making. Clean against the database of 2026-08-21. No static analysis beyond `go vet`. No external review. |
| Reproducibility | **partial.** `make repro` builds every binary twice, the second time from a copy of the tree at a different path so that `-trimpath` is exercised rather than assumed, and compares the digests. They match. That is one builder, one toolchain, one machine — not the two independent builders SPEC 31 asks for — but it establishes the half that usually breaks first and has to hold before two builders can agree about anything: the build embeds neither the clock nor the working directory. A second builder has still never been tried. |

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
140 defects. Twenty fixes took it to 328 and 40, and refusing a block
collection on its key's line took it to **331 and 37**. Statement coverage of
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
| `gapLenient` | 20 | halite parses a document the suite requires to be an error. This was called the safe direction, and the PyYAML differential of 5.8 showed the framing was wrong: a document the reference implementation refuses is one Salt would not load, so accepting it means the tree loads here and means something nobody wrote. What is left is mostly tabs in odd positions, document markers inside quoted scalars, and under-indented continuations. |
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

157 of 198 of Jinja's own extractable cases, 26 of the rest outside the
subset by design and 15 gaps. `internal/template` rose to 81.9% statements
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

Every one of the fifteen has been read rather than counted, which had
not been done before and changed what three of the class names mean.

| Class | Cases | What it is |
|---|---|---|
| `gapScoping` | 4 | needs a `test` callable those Jinja tests register on the context, which the extractor cannot carry. Scoping cases in name only. |
| `gapOther` | 4 | a custom `Context` class, a custom `select` test, `self.foo()` block self-reference, and a call with `*args` and `**kwargs`. Three of the four need something the extractor cannot carry; only the block self-reference is a real absence. |
| `gapRendering` | 2 | both need a custom code generator. |
| `gapTestArgument` | 2 | both use a `matching` test the Jinja suite registers itself. `is divisibleby 3` and its like already work, with or without parentheses — the class name was misleading. |
| `gapFilterBehaviour` | 1 | `indent(width='>>> ')`, where the width may be a string used as the prefix. |
| `gapNumericAttribute` | 1 | `groupby(0)`, grouping by index rather than attribute name. |
| `gapCallResult` | 1 | calling the result of a filter: `foo|attr("items")()`. |

So of fifteen, **eight need a Python callable the corpus extractor cannot
carry across** and are not gaps in the engine at all; the extractor
records them as gaps because it cannot tell the difference, which is the
honest default. What is left is five small features and one absence,
`self.foo()`.

The whitespace class is gone. `lstrip_blocks` strips the whitespace
running from the start of a line to a block tag, and four of those five
words were not being honoured: whitespace with no newline left in the
span was only stripped at the very start of a template, so the newline
trim_blocks had just eaten took the rule with it; the rule was applied
before `{{ x }}`, where Jinja leaves the whitespace alone, and at the end
of a template, where there is no tag at all, so a file ending in an
indented line lost its indent; and `{% endraw %}` was not treated as the
block tag it is. `+#}` on a comment was not read either. Seven cases,
one rule, checked against Jinja 3.1.6 rather than reasoned about.

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

**2026-08-22: the estate's own tree, in test mode, as root.** Run by its
owner, with the encrypted pillar decrypted for real:

    Succeeded: 47  Would change: 4  Failed: 0  Total: 51 (2 held back)

That is the first time halite's *modules* have been asked about a real
estate's declarations rather than its compiler. 51 chunks across pkg,
file, service, user, cmd, sysrc, and git, against a live FreeBSD host,
in 22 seconds, with nothing failing.

It is worth being exact about what it establishes. Test mode is a
prediction: 47 states read the system and reported it already as
declared, 4 reported that they would change something, and 2 were held
back by their own `unless` guards. Nothing was written. The prediction
has not been checked against what an apply would actually do — that is
the state-results half of the differential in 5.7, and it is still
absent. A state whose test mode is wrong reports exactly this and then
does something else, which is why SPEC 11.6 makes the contract testable
and 1.4 checks it more strictly than specified.

The run also found defects, and none of them in the states it ran.

The summary line read `Skipped: 2` beside the other counts, so it added
to 53 against a total of 51.

A later apply produced the failure an unconverted tree reliably
produces: `name: bastille stop troupe` is a shell line in Salt and one
program name here. `docs/migrating-from-salt.md` promises that halite
explains this in the error, and the explanation had been written and
never appeared — it tested `errors.Is(err, os.ErrNotExist)`, and a bare
name that is not on PATH gives `exec.ErrNotFound`. The one case the hint
existed for was the one case it missed.

Worse, the audit had not warned. This is the most common thing an
unconverted tree gets wrong and the reason the default was inverted at
all, and `halite-hub migrate` was not looking for it — so the tree was
reported clean and its author found out one state at a time, mid-apply.
It is a review finding now, and there were six.

The audit could not see the files where it mattered most, either. A
state ID built from an expression — `{{ sls }} create jail:` — was
blanked to spaces, which moved the key ten columns right, broke the
file's structure, and made the declaration audit skip the whole file
silently. Two of the tree's files were invisible to it, and one of them
used two `file.directory` arguments this build did not have.

### 5.7 What the Salt differential covers, and what it does not

`make check` runs it against whichever `salt-call` is on PATH. It has
been run against Salt 3006.25 and 3008.2.

Compared, over ten trees:

- the low state: the chunk sequence first, then each chunk's arguments
- the pillar, with its merge across two files

- the **test-mode prediction** for every state: whether it says it would
  change, is already as declared, or fails, and whether it reports
  changes. Opt in with `HALITE_SALTDIFF_RESULTS=1`; it evaluates every
  state against the host, reading the system and writing nothing.

  This one wants the privileges the tree itself needs. Run against a
  real tree unprivileged it reports a dozen differences and almost all
  of them are one thing: Salt's `service`, `sysrc`, and `file` modules
  fail or raise where halite reads the same state without privilege, so
  the two disagree about a host neither of them was allowed to inspect.
  That is worth knowing once and not worth reading every time.

Not compared:

- **what an apply actually does.** A prediction is not a result. This
  catches a module that predicts differently from Salt; it does not
  catch one that predicts correctly and then does something else, which
  is what SPEC 11.6's contract and 1.4's stricter check exist for.
  Applying a tree twice under both implementations and comparing
  `changes` needs a container to apply it in, which is the integration
  layer, which is phase 2.
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

The prediction comparison has two recorded deviations. The first is the
claim the README has been making about `test=True` all along. Salt fires
`onfail` when its target did not *succeed*, and in test mode a state
that would change reports neither success nor failure — so Salt predicts
that an onfail state will run when a real run would not run it. halite
fires onfail when the target failed, which is what the requisite means.

The second: in test mode halite reports what would change and Salt
reports nothing. SPEC 11.6 asks a state that would change to say what,
and an empty `changes` on a result of None tells an operator only that
something was going to happen.

The low state comparison has one deviation, and it is a difference
between the two Salt majors rather than between Salt and halite: 3006
resolves the reversed requisites while executing rather than while
compiling, so its `show_lowstate` does not carry them, and 3008 does, as
halite does. A deviation row therefore names the version it was observed
under; one that did not would be unfalsifiable.

The per-state options of SPEC 11.7 — `unless`, `timeout`, `runas` and
their neighbours — are not compared as module arguments. Salt passes
them through to the module and halite lifts them out for the runner to
apply, so comparing where each files them compares two schemas rather
than two behaviours. `timeout` was a recorded deviation until the
comparison stopped asking the wrong question.

### 5.8 Where the PyYAML differential stands

SPEC 10.1 specifies the dialect as PyYAML's. 114 documents go to both
and the resolved type is compared as well as the value.

104 agree, and every one of the ten that differ does so by design:

| Rule | Cases | Why |
|---|---|---|
| Duplicate keys are an error | 2 | SPEC 10.1.2. PyYAML takes the last silently. |
| A date stays a string | 4 | SPEC 10.1.3. A date becoming a struct breaks `file.managed` contents. |
| A sexagesimal stays a string | 3 | SPEC 10.1.3, with a lint warning where it would have differed. `12:30` is 750 under PyYAML. |
| Integers are int64 | 1 | Python's are unbounded. |

There are no gaps left here. The four it had were one defect: `k: -`
became a one-element sequence, `k: ?` and `k: :` became a mapping of
null to null, and `a: b: c` became a nested mapping, where YAML puts a
block collection on the following lines and PyYAML refuses all four.
Closing it also closed three of the lenient gaps in 5.4.

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
| `file.managed` read an unreadable file as empty | The error from the read was discarded, so a 0640 credential the run could not read compared as empty: the state reported that the contents differed, showed a diff adding the whole file, rewrote it, and would have done so on every run for ever, because it still could not read what it had written. |
| `file.replace` took no `bufsize` | Salt's names a read buffer. |
| A decrypted pillar value could reach a log | The `gpg` renderer works, so a real secret now travels through code that has no idea what it is holding. SPEC 26.1's value-based redactor is applied at the sink — the logger and `Fatalf` — and seeded from every decryption and every setting whose name says it holds a secret. The state return is scrubbed too — both output formats and the SPEC 11.8 key, which carries the state's name and therefore whatever a `cmd.run` was pointed at. The run's own data is left intact, because `onchanges` and `prereq` compare changes and two secrets both becoming asterisks would make two states look alike. |
| Thirteen settings were declared and read by nothing | `yaml_bool_11`, `random_seed`, `legacy_arg_parse`, `template_trim_blocks`, `template_lstrip_blocks`, and the pair `env_allowlist` and `env_denylist` — three named in SPEC as the switch a tree throws during a migration, and one an access control that did not control. `node_id_lowercase` and `node_id_remove_domain` came with them: the shim translated a Salt configuration into keys nothing read, so the same file produced a different identity here, and the identity is what pillar and targeting are keyed by. So did `log_level`, `log_format`, and `log_file`: every diagnostic went to stderr whatever it was, so `log_level: error` on an unattended node changed nothing, and one of this project's own example configurations set a `log_format` that did nothing. The audit had two false negatives of its own, and they were the sharper find. It counted a key mentioned in a *test* as read, and a test proving the loader carries a key says nothing about whether anything acts on it. It also counted a *module parameter* of the same name: `hash_type` is declared on `file.managed` **and** is a configuration key, and neither was read by anything. Looking for the key passed to a configuration accessor — which is what reading one looks like — found `pillarenv` (a tree holding its pillar in one environment while its states moved between several got the states' environment for both), `renderer` (every file got `jinja|yaml` whatever the tree asked for), and `hash_type` itself, whose `file.managed` parameter claimed to be the "digest used to compare contents" where the contents are compared byte for byte. A test now requires every declared key to be read or listed with the reason it is not, enforced in both directions, so the class cannot grow again in silence. |
| `cmd_default_shell` silently dropped `args` | The setting is a default for states that do not say which form they are in, and it was applied to states that had said. A state converted during a transition stopped passing its arguments and reported success: the tree said `/bin/echo a b` and the node ran `/bin/echo`. Every state converted while the setting was on was quietly doing nothing of what it said. |
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

**The tree compiles, and Salt agrees with the result.** With its state
files corrected by its author on 2026-08-22, `state show_lowstate`
produces 51 chunks across the 11 SLS files this host matches, and the
differential of 5.7 was pointed at it:

> halite 49 chunks (after the two below), Salt 3008.2 49 — identical, in
> the same order, with the same arguments.

The two set aside are a state gated on `grains.get('productname')`.
halite reads the SMBIOS tables through kenv and gets `PowerEdge R730xd`,
which is what the machine is; Salt shells out to dmidecode, which needs
`/dev/mem` and therefore root, and unprivileged it returns the error
text *as the grain's value*. A tree branching on the hardware takes the
wrong branch under Salt and the right one here. As root they agree.

Comparing against Salt 3006.25 additionally differs on ordering, which
is the recorded deviation: 3006 resolves requisites while executing
rather than while compiling, so its `show_lowstate` is declaration
order.

Getting there took three more fixes, and the first is the most damaging
defect the differential has found:

| What | Why it mattered |
|---|---|
| A `names` entry's own arguments were dropped in the list form | Which is the form Salt takes; the mapping form halite handled raises a ValueError out of Salt's compiler. On `file.managed` the dropped argument was the `source`, so a tree installing seven scripts would have written seven empty files over them. |
| Colon traversal would not descend into a list | Salt searches the mappings inside a list for a non-numeric key. `salt['pillar.get']('users:ed:password_bsd')` returned nothing, and the template rendered the empty value into `user.present` — an account created with no password rather than a state that failed. |
| The differential harness had the CLI's dispatcher hole | Invisible because the written corpus never used `salt['pillar.get']`. |

What none of this means is that the tree has been applied. Compilation
proves it is understood and the differential proves both implementations
plan the same thing; neither says the modules do the right thing when
they run. Nothing in this tree has been run.

Still open, from the same tree:

- **`mode: 640`** is refused, correctly: it is the integer 640, and the
  tree means the mode 0640. Salt happens to get this right by reading
  the decimal digits as octal, and gets `mode: 0644` wrong the same way,
  silently applying 0420.
- **The tree's own `mkdirs`** is not a Salt argument — the name is
  `makedirs` — so Salt has been ignoring it in four places. halite
  reports it, which is the audit working rather than a gap.
- **The pillar decrypts, and its values do not reach the output.**
  Confirmed by the estate's owner on 2026-08-22, running `halite-node`
  as root against the real tree and its own keyring at
  `/usr/local/etc/salt/gpgkeys`, and again with `--test` once the
  redactor existed: the zerotier bearer token and network id, which the
  tree renders into a `cmd.run`'s own name, were censored from the run's
  output. That is the redactor checked against real secrets rather than
  against a value its own test invented. The renderer's tests
  generate a throwaway keyring, so before that the path had only ever
  been exercised against data written by the test itself; it has now
  been run against the encrypted pillar it was written for. An
  unprivileged session still cannot, because the keyring is root-owned
  0700, and the failure names the pillar key rather than the contents.

---

### 5.10 What the declared-and-unread sweep found

Three defects in a row were one setting each that nothing acted on:
`cmd_default_shell` applied where it should not have been, a per-state
`timeout` parsed and dropped, and the `salt` dispatcher plumbed and never
populated. Rather than wait for the fourth, `internal/config`'s
`TestEveryDeclaredKeyIsReadOrRecorded` requires every key to be passed to
a configuration accessor somewhere, or listed with the reason it is not,
enforced in both directions.

Thirteen were live: `yaml_bool_11`, `random_seed`, `legacy_arg_parse`,
`template_trim_blocks`, `template_lstrip_blocks`, `env_allowlist`,
`env_denylist`, `node_id_lowercase`, `node_id_remove_domain`,
`log_level`, `log_format`, `log_file`, `pillarenv`, and `renderer` —
four of them named in SPEC as the switch a tree throws during a
migration, one an access control that did not control, and three the
whole of the logging configuration.

The same sweep one level down, over the 167 parameter names the module
registry declares, found exactly one: `hash_type` on `file.managed`. The
module layer was in better shape than the configuration layer, which is
worth knowing.

A level down again, over the fields of a signature rather than its
parameters, found two read by nothing at all. `Platforms` documents
itself as "restricts the function; empty means every platform" and
restricted nothing, so a `sysrc` call on Linux reached the module and
reported a missing binary — true, and about the wrong thing. Twelve
functions declare it. `Privileges` is declared by twenty-nine, all of
them mutating and all of them naming `root`; refusing up front would be
correct and would also refuse a `--test` run, which is the run an
operator makes precisely because they are not ready to be root, so it
explains a failure instead. Two fields, two different right answers,
which is why "enforce what is declared" is not one change.

The last surface is the command line. A flag in the usage that nothing
parses, or one parsed and never documented, is the same defect where an
operator meets it first. Both directions are checked in both programs.
That found `--root` on the hub, parsed and undocumented, and something
worse: `--config` named the program's own configuration in
`halite-hub lint` and "a Salt file to translate" in `halite-hub
migrate`. One flag, two meanings, one program, and pointing it at
hub.yaml asked the audit to translate that as Salt without saying so.
The migrate one is `--salt-config`.

Counting the whole sweep: thirteen settings, one module parameter, two
signature fields, and two flags. The pattern in all of them is the same
and is worth stating once — something was written down, and writing it
down was mistaken for doing it.

Two things the sweep taught about sweeps. The first version counted a
key mentioned in a *test* as read; the second counted a module parameter
of the same name. Both are the shape of a check that passes for the
wrong reason, which is worse than no check, because the list of
exceptions grows and nobody looks again. And the strict version turned
two correct reads into false positives — `file_roots` and `pillar_roots`
go through a helper that takes the key as an argument — which is
recorded as an exception with its reason rather than fixed by loosening
the rule.

### 5.11 What the transport lab run covers

The unit tests for `internal/hub` stand up a real listener, a real CA,
and a real client, so they are not mocks. They still missed two things
that a hub and two node processes on one machine found in ten minutes,
and both were the same shape: **a connection outlives the handshake that
authenticated it.**

| Found by running it | What it was |
|---|---|
| A revoked node reconnected and was served | The node's HTTP/2 connection was still open, so `Subscribe` reused it and no second ClientHello reached `VerifyPeerCertificate`. The tests called `Reset()` before checking, which drops the pooled connection -- so they tested only the path that was already safe. Revocation is now checked per request as well as per handshake. |
| A renewed node kept streaming on the superseded certificate | Renewal revokes the old serial. The stream opened with it stayed up, authenticated by a serial the hub had just denied. The hub now ends that stream with a `reload`, and the node reconnects and reads the certificate it has just been issued. |

Two smaller ones came from the same run: the hub's serving certificate
put `127.0.0.1` in the DNS names rather than the IP names, so a node
configured with `hub: 127.0.0.1` could not verify it at all -- every
test dialled by name; and the hub read `log_fmt`, which is not a
setting, so the loader warned on every start.

Remote execution added two more of the same kind, and one of them is
the worst defect in this phase:

| Found by running it | What it was |
|---|---|
| A `--test` job put the agent into test mode **for ever** | `executeJob` set the flag on the long-lived node rather than on a copy. The first `run '*' state.apply --test` was correct; every real apply after it on that node reported what it *would* do, and an operator would have believed it had done it. A job now runs against a shallow copy of the node, and the same applies to the environment a job names. |
| A state return came back as `map[Pos:map[Col:0 File: Line:0]]` | The return payload was `any` and went through `encoding/json`, which cannot see the ordered model: it marshalled the struct rather than the mapping. The payload is now encoded on the node with the model's own codec, which also keeps SPEC 6.4's promise about 64-bit integers. |

One more thing came out of running it, and this one is a hazard an
operator can create as easily as I did: the lab had `state_dir` inside
`file_roots`, so the hub served **its own key store and job cache** to
every enrolled node. The key store is not secret, but the job cache
holds every return in the estate, and returns carry pillar-derived
values. `file_roots: /srv/halite` beside `state_dir: /srv/halite/state`
is an easy thing to write. `serve` now refuses to start on an overlap
and names both directories, because everything after startup looks like
it is working.

What the lab run establishes: manual enrollment with an out-of-band
fingerprint comparison, token enrollment within a node-ID glob and a
source CIDR, single-use enforcement, acceptance and rejection,
revocation from a *separate process* reaching a running hub and the
connected node, renewal with a fresh key while connected, a signed CRL
that OpenSSL parses, a highstate driven from the hub across two nodes in
test mode and then for real and then again to convergence, glob, list,
and grain targeting, `--async`, a job for a node that is not connected
reported as unanswered with exit code 3, a function that does not exist
returned as a failure rather than a crash, a node certificate refused at
`/v1/jobs`, and a highstate compiled from a tree that exists only on the
hub -- including a `salt://` source fetched, verified against the
published digest, and cached -- then edited on the hub and reconverged,
and a per-node pillar compiled on the hub and used in a template, with
each node receiving only its own.

What it does not: more than two nodes, more than one hub, a network that
is not loopback, a hub restart with nodes connected, a certificate
actually reaching expiry, clock skew between hub and node, a replay or
an expired job over the wire (the guard is covered by tests, not by the
lab), a return large enough to need chunking, and the endpoints that do
not exist. It has been run on FreeBSD only.

### 5.12 What the runner lab run covers

A hub, a node, and two operator certificates as separate processes on
one machine. The `internal/hub` tests already drive the runners over a
real listener with a real CA, and running it still found one defect:

| Found by running it | What it was |
|---|---|
| `manage.versions` reported a matched fleet as mismatched | The hub compared `version.Version` against the string a node reports, which is `version.String()` — the same build, with the commit appended. Every node in an estate running the hub's own build was listed as behind it. |
| Every structured argument reached the hub as a position record | The operator's command line parses `data='{"a":1}'` into the ordered model, and the transport marshals its bodies with `encoding/json`, which sees the model's unexported entries and its one exported field: what arrived was `{"Pos":{"File":"","Line":0,"Col":0}}`. This is the same defect the state return had, in the other direction, and it had been there since `run` landed — `run '*' state.apply pillar='{"a":1}'` included. `value.Map` marshals as its mapping now, which fixes it everywhere the standard encoder is reached rather than at each call site. |
| A 64-bit integer in a job argument or an event came back changed | `9007199254740993` came back as `...992` at three decoders: the node's job stream, the hub's event ingest, and every read off the event log. SPEC 6.4 says it must not. They decode with `UseNumber` now and lift the result into the model. One test asserted the float64 behaviour and was holding it in place. |
| A tag glob mixing `*` and `**` matched nothing | Everything before the `**` was compared as a plain string, so `halite/node/*/deploy/**` looked for a node literally called `*`. A filter that matches nothing and says nothing is the worst shape one can have. It matches segment by segment now, and `halite/**/ret/*` works too, which the prefix comparison could not express. |
| A step's `timeout` was two timeouts that disagreed | SPEC 19.1 lists `timeout` among a step's options and SPEC 11.7 lists it among every state's, and they are the same option: the state runner strips it and bounds the step's context with it. The step module was waiting on its own default of five minutes while the context expired underneath it, so a step written with `timeout: 10s` waited its ten seconds and then reported that the run had been stopped. The module reads the deadline off the context now. |
| A node that started before its hub never re-attached to it | It falls back to its own file roots and pillar, correctly — an outage should not stop a node managing itself. It then stayed there for the life of the process, taking jobs and running highstates compiled from whatever local tree it happened to have, long after the hub was up. The only sign was one warning at startup. In an estate where a hub and its nodes reboot together, the node most likely to win that race is every node. Attachment is now attempted on every connection. |
| `event.send` and `pillar.refresh` failed on every node | Both were registered as stubs saying they needed the hub, "which arrives in phase 2", for as long as phase 2 had been finished. The `saltutil.refresh_pillar` runner dispatched `pillar.refresh` to nodes that could only refuse it. An audit now reads the stubs out of the source and fails on any naming a delivered phase. |

What the lab run establishes: `manage.status`, `up`, `versions`, and
`list_state` against a connected node and against nothing; `key.list`
over a real key store; `jobs.lookup_jid`, `exit_success`, `list_jobs`,
and `survey.hash` against a job actually driven across the wire, with
the runner's own call recorded in the same cache; `cache.grains` and
`cache.clear_grains`; `fileserver.file_list` and `dir_list` over the
hub's tree; `event.send` and `event.replay` round-tripping through the
durable bus with the principal taken from the certificate;
`nodegroups.expand` refusing a name that is not defined;
`saltutil.refresh_grains` and `saltutil.refresh_pillar` dispatching real
jobs to a real node; `event.send` called as a module on the node, with a
payload holding a nested mapping and a 64-bit integer, arriving on the
hub's log unchanged; `error.error`; a three-step orchestration compiled
from a templated SLS with `require` between the steps, run in test mode
and then for real, reaching a real node and a hub runner; a failing
orchestration whose `onfail` rollback ran; `orch list`, `orch show`, and
`orch resume --from` carrying a failed step forward and completing;
`salt.wait_for_event` blocking until an event fired by a node arrived;
a configured reactor matching an event a node fired, running both a
`runner` and a `local` reaction as its own principal and recording both
in the job cache under it, `reactor.list` and `reactor.test` reporting
the plan and the policy decision without dispatching, a `dedupe_window`
collapsing two identical events into one reaction, and a hub restart
resuming the reactor from its recorded offset; the `filechanges` and
`diskusage` beacons running on a real node, firing on a real change to a
watched file, reaching the hub's bus, and the reactor acting on one --
the whole automation loop, end to end; `halite-api serve` against the
real hub, with a login issuing a usable token, introspection reporting
it, logout killing it, the security headers on every response, and the
token appearing nowhere in the service's log; a synchronous run and an
asynchronous submission through the API reaching a real node, the job
recording `on_behalf_of` beside the service's own certificate, and
`/v1/nodes` and `/v1/keys` answering from the real estate; a schedule running `test.ping`
every five seconds and a `cron` job reporting its next fire time, with
the returns landing in the node's own NDJSON log; a beacon and a job
added to a running node, disabled, run out of turn, saved, and still
there after a restart with the disabled state intact, and a fragment
written in the wrong shape refused by name; a node publishing two
mine functions on its interval and another read of them refused by the
policy for the function it was not granted; a node started before its
hub falling back to its own roots and then re-attaching once the hub
appeared; a runner declared and not built naming its phase; an
unknown name listing its module's runners; and the two-stage
authorization — an operator holding `runners: ['*']` and nothing else
calls `manage.status` and is refused `saltutil.refresh_grains` because
the job it would dispatch is not granted.

What it does not: more than one node, `event.listen` waiting on a live
event, `jobs.prune` against a cache old enough to prune, `key.accept`,
`key.reject`, `key.revoke`, and `key.delete` through the runner rather
than through `halite-hub keys`, every runner registered as pending, and
on the orchestration side `batch` and `subset` on a step, `tolerate_failures`
against a real second node, `salt.wheel`, and a hub restart in the middle
of a run. On the reactor side it does not cover a `caller` reaction, a
burst large enough to overflow the queue, the rate limiter, `debounce`,
a causality chain long enough to be broken, or an event arriving while
the hub was down. On the beacon side it does not cover `load`,
`memusage`, `service`, `cert_info`, or `status` against real thresholds,
`delay`, `disable_during_state_run` during an actual state run, or a
beacon left running long enough to exercise the coalescing window. On
the scheduler side it does not cover a `cron` job actually firing, a
daylight-saving transition on a real node, `catchup` after a real
outage, `maxrunning` under a job that overruns, or `splay`. On the mine
side it does not cover a second node reading a first one's data — both
halves ran on the one node — nor `allow_tgt` refusing a real reader, nor
`mine.send` for a single value. It has been run on FreeBSD only.

### 5.13 What the API lab run covers

A hub, a node, and `halite-api serve` as three processes on one machine,
the API holding its own operator certificate and a local account behind
a token. It covers login, an execution call through both authorizations,
the job endpoints, `/v1/nodes`, both event transports, and a signed
webhook delivered end to end onto the bus.

| Found by running it | What it was |
|---|---|
| `/v1/nodes` failed on an estate-wide question | It asks the hub `cache.grains`, whose signature required a `node`. There was no way to ask for the whole estate, which is the only thing that endpoint asks for. |
| A runner grant ignored `NeverWildcard` | `/v1/pillar/{id}` must not be satisfied by a wildcard grant — reading one node's pillar is reading its secrets. The check was written on the fleet-function branch only, so the runner branch let a `runners: ['*']` role through. |
| A webhook delivery that failed downstream could never be retried | The replay nonce was recorded when the signature verified. A transient hub failure therefore consumed the delivery: the sender's retry carried the same signature and was refused as a replay. Recording moved to after the delivery lands. |
| The payload reached the hub in a shape it refuses | The delivery was handed to `event.send` as a JSON string, and that runner declares `data` as a mapping, so every real delivery was refused — while the test passed, because a stub hub accepts either. The body is now carried as what it parsed to, and the test asserts the shape rather than the substring. |
| Beacon events were in the wrong namespace | SPEC 17.1 puts a beacon under `halite/beacon/<node_id>/<beacon>/` and SPEC 18.1's own reactor example matches on it; they were arriving under `halite/node/<node_id>/`. A reactor written from the specification matched nothing and said nothing about it. Found while adding the metric that counts them. |
| Two expositions concatenated are not one exposition | Both components expose `halite_build_info`, and the text format allows one `# HELP` per metric name in a document. A scraper rejects the whole body for the duplicate, so the failure arrives as "no metrics at all" rather than as one duplicated family. They are merged now. |
| An agentless target compiled its own pillar | The hub sends pillar inline and the target fell back to a local tree when none arrived — so the first run against a machine that used to run Salt compiled the *old estate's* pillar, half of it encrypted to keys the process did not have. It uses what the hub sent and only that now, an empty pillar included. |
| The target's login shell is not always POSIX | ssh hands its command to the login shell. Against a target running `fish`, a script containing `if ... then ... fi` was a syntax error before `/bin/sh` was reached, and the POSIX `'\''` idiom for an embedded quote did not survive it either. Setup scripts go over stdin to `sh -s` now, where no quoting is involved; the one command that cannot takes validated values rather than escaped ones. |
| `git archive` emits a header the extractor called a file | The extractor refuses anything that is not a regular file or a directory, which is right — and `git archive` writes a `pax_global_header` first, carrying the commit id. Every ref failed to materialise. Found on the first run against real git, which is the only place it could have been found. |
| Configuring an extension returner deadlocked the node | A returner that is an extension arrives through `saltutil.sync_returners`, which needs a running node — and the node refused to start because its configured returner was not there. A node that will not boot cannot be sent the thing it is waiting for. A returner name this build does not have is no longer fatal; it fails every return with the reason instead, which is said once at startup. |
| Warming an extension probed it with an empty function name | The first cut read "no such function" as proof the handshake had happened. That held until an extension parsed its arguments before looking at the function name, and then failed with "unexpected end of JSON input" — a message about nothing the operator had done. `Pool.Warm` already completed the handshake without calling anything. |
| An extension's working directory was never created | Go reports that as `fork/exec <executable>: no such file or directory`, naming a file that is present and correct. Whoever read it would have gone looking at the executable, its permissions, and its architecture, and found nothing wrong with any of them. |
| An extension's working directory sat inside the verified cache | At `<cache>/<name>/work`, so the store read it as a version of the extension and every load logged a refusal for it — beside three real ones, which is how an operator learns to ignore refusals. The cache holds bundles verified on every load, and a writable directory inside one is a file the manifest does not list. |
| A login failure was logged as "unreachable" when it was a blank password | The classification was a boolean over "did the directory answer", so the commonest failure of all — somebody submitting the form with the password field empty, which this client refuses without asking the directory — read as an outage. An estate alerting on outages would have been alerted every time. It is a named reason now. |
| The OIDC provider had no way to trust an internal CA | An identity provider behind an estate's own CA is the common case, and the only ways to reach one were a public certificate or skipping verification — on the service that decides who an operator is. The same omission the webhook returner had, found the same way. |
| A node whose pillar did not compile answered nothing | Every exec function compiled pillar first and failed the job when that failed, so one unreadable file in the pillar tree meant no `test.ping`, no `grains.items`, no `service.status` — at exactly the moment somebody was trying to find out what was wrong with the node. The error travels on the execution context now and surfaces at the functions that read pillar. Found by running a node against a real tree whose pillar this process had no GPG key for. |
| The webhook returner had no way to trust an internal CA | It verifies TLS, correctly, and refused a receiver holding a certificate from the estate's own CA. The only ways out were a public certificate or skipping verification, and the second is not on offer for a connection carrying whatever a job printed. |
| A diagnostic about a secret file redacted the file's name | `returner_webhook_secret_file` matched the redactor's "secret" rule, so "the secret at ********** is mode 644" was the message. The path is not the secret; the contents are. A key ending `_file` is exempt now. |
| No WebSocket upgrade could ever succeed | The access-log wrapper did not pass through `http.Hijacker`, so `/v1/ws/events` answered "this connection cannot be upgraded" for every caller. The endpoint's own tests called the handler directly and stayed green. A test now dials the assembled server and speaks the protocol. |

The event stream was watched over both transports while a job ran, and
delivered `halite/job/<jid>/new` and `.../ret/<node>` with the bus
offset as the SSE `id:`. The WebSocket path was exercised with a
hand-rolled RFC 6455 client: the handshake, the accept key, masked
client frames, the thirty-second heartbeat answered with a pong, and a
close handshake completing with 1001. A signed hook delivery was
accepted and reached the bus carrying `cert:CN=api` as its principal; a
replay of it was refused, and a tampered body was refused.

The metrics were read from a live hub and a live API: a job's dispatch,
duration, and return; the states inside a state run; pillar compilations;
file server requests by status code; beacon events by beacon; a refused
authorization counted as a denial; and a scrape taken with the hub
stopped, which answered 200 with the service's own numbers and the
reason as a comment. The merged body was checked to have no metric name
declared twice and every series line under a declaration.

The returners were run against real receivers: a TLS webhook sink that
verified every HMAC signature with its own implementation, and a TCP
syslog receiver. Twenty-three returns arrived across a receiver outage
— none lost, none duplicated, and in order — with the spool filling
while the receiver was down and draining ahead of new returns when it
came back. `event_return` shipped 170 events to a file returner,
rotated at the configured bound, and resumed from its offset across a
hub restart without re-shipping what had gone.

OIDC was run against a provider process that verifies PKCE for real:
the interactive flow completed end to end and its token drove the fleet,
and four negative paths were refused — a verifier the provider never
saw, a token minted for another login, a replayed `state`, and groups
that map to no role. The reason stayed in the log and the answer stayed
generic, except for the unmapped-groups case, which names the groups on
purpose. The auth metrics separated accepted, refused, and unmapped by
method.

LDAP was checked against an implementation this one did not write:
`ldapsearch`, the OpenLDAP client, binds over LDAPS to this package's
test directory, sends a compound filter, and parses the responses — so
both halves of the BER are validated by a real peer rather than only by
each other. An operator then logged in through `/v1/login` with
`eauth: ldap` against that directory and the token drove the fleet, with
a wrong password, an unknown user, an empty password, and an injected
filter all refused with one message and told apart in the log.

The extension model was run end to end: a Go binary was signed into a
bundle by `tools/extbundle`, put in a node's cache, verified against a
trust key and a version-and-digest pin, started as a sandboxed process,
and called as `echo.say` through the ordinary module registry — with
argument validation refusing an argument the extension had not declared.
Three refusals were confirmed against the running node: a tampered
executable, an unsigned extra file in the bundle, and a pin that no
longer matches.

Synchronization was then run through the hub's own file server: a bundle
published under `_ext/`, fetched by `saltutil.sync_all`, verified,
pinned, and cached. A second synchronization reported it unchanged and
downloaded nothing. The publisher then swapped the executable without
re-signing; the synchronization refused it and the node went on
answering with the version it already had. A second bundle, of kind
`returner`, was synchronized and used as the node's returner — the
scheduled job's returns were filed by an extension in a sandboxed
process.

Agentless mode was run against a real `sshd` on a loopback port with its
own host key and authorized_keys, so none of the operator's ssh state was
touched: the binary was pushed and verified, a second run skipped the
transfer in under a second, roster grains reached the target and were
targeted on, a state run applied from an inline tree, `--test` changed
nothing, `--clean` removed the cache, and an unsafe `thin_dir` was
refused by name. It does not cover a target on another platform, a
target reached through a jump host, sudo against a real sudoers policy,
or the `cache` and `ansible` rosters against real inputs.

s3fs was run against an S3 that verifies SigV4 with its own
implementation: the hub listed a bucket, mapped two prefixes to two
environments, fetched the objects, and applied a highstate served out of
S3 to a real node. Every request the hub made verified; the only refusal
the server logged was a `curl` sent without a signature.

gitfs was run against a real repository through a real hub: two branches
became two environments, a highstate served out of a git branch applied
to a node, a push was picked up both on the update interval and on
demand through `fileserver.update`, and with
`gitfs_verify_signatures: true` against an unsigned repository both refs
were refused, no git environment was served, and the hub went on serving
its local roots. The signed-and-served half is covered by a unit test
against real GnuPG and real `git verify-commit` rather than in the lab.

One thing the lab taught that was not a defect: a `top.sls` on a branch
declares which environment it contributes to, and a branch whose top
file says `base:` contributes to `base` however the branch is named.
Salt merges top files across environments and so does this; an hour went
into concluding that the code was right and the test tree was wrong.

It does not cover a Prometheus server actually scraping it, a real
directory (OpenLDAP's slapd, Active Directory) rather than this
package's own test server, StartTLS against one, a real identity
provider (Keycloak, Entra, Okta) rather than a conforming stand-in, the
client-credentials grant against one, an SMTP returner against a real
mail server, syslog over TLS, `mtls` hook authentication,
`Last-Event-ID` resumption after a real disconnection, a token expiring
mid-stream, a second operator with a narrower policy watching the same
stream, a hub restart underneath an open stream, `/v1/nodes/{id}/state`
against a node that fails, or `/v1/orch`. The two-stage authorization
was seen to refuse — the API's own certificate lacked `event.send`
until its role was granted it — but only for that one function. It has
been run on FreeBSD only.


### 5.14 What the relay lab run covers

An upstream hub, a relay, and a node as three processes on one machine.
The relay enrolled with the upstream as an ordinary node and was granted
`relay.proxy` there; the node enrolled with the relay and has no key on
the upstream at all. It covers a job submitted upstream reaching the
node through the relay's stream, the return filed upstream and
attributed to the node, `manage.up` upstream reporting the relayed node,
a return spooled through a real upstream outage and drained when it came
back, and event forwarding filtered by tag glob.

| Found by running it | What it was |
|---|---|
| The relay panicked before it connected | `Server.Fleet` is created lazily on the first node connection, and the relay reads it at startup to report its subordinates upstream — a nil dereference on every relay that started before a node arrived, which is every relay. The lazy constructor also replaced a fleet a caller had already set, so the field's own documentation was false the moment anything touched it. |
| A relayed node was unreachable by any job | Targeting resolves against the keystore, and a relayed node has no key on the upstream and never will — the relay issued it. `Connected` reported the node as up while `resolve` matched nothing, so a job aimed at it came back as if the machine were absent. Targeting reads the accepted keys and the relays' subordinates now. |
| Every return through the relay was refused | The relay forwards a job down but never recorded it, so the node's return arrived at a hub with no such jid. The node logged that its return was refused, the relay logged an unknown job, and the operator upstream waited out the timeout on a job that had run and succeeded. |
| Reconnecting discarded the whole spool | The upstream refuses a return from a relay that does not own the node it names, and the drain ran concurrently with the subscription — so on every reconnection the spool was refused as impersonation before the upstream had recorded who the relay proxies for. The relay announces its subordinates first and drains after, and a refusal now costs an entry several attempts rather than its life. |
| A refused entry blocked the spool for ever | The first cut stopped the drain at the first failure and logged nothing, so one entry the upstream would never accept held every later return behind it and said so nowhere. |
| Relayed returns were tagged with the relay | Attribution came from the certificate the return arrived on rather than the node that ran the job. Upstream, every return behind a relay was `halite/job/<jid>/ret/relay1.example`; a reactor watching for its own node never fired, and a whole segment looked like one machine. |

What it does not cover: a relay two deep, a relay whose upstream is
itself a relay, more than one relay on one upstream, a relay restarting
under an open subordinate connection, a spool that reaches its size
limit, pillar compiled upstream and forwarded down, a subordinate moving
between relays, or the depth cap being reached in practice. It has been
run on FreeBSD only, with one relay, one subordinate, and one upstream.


### 5.15 What the FIPS lab run covers

A hub and a node built with `GOFIPS140=v1.0.0` and run with
`GODEBUG=fips140=on`, enrolled against each other and driven through a
job, plus the whole test suite built the same way. The key exchange was
measured from outside with OpenSSL 3.5.6 rather than from the
configuration this build sets, because a restriction asserted against
one's own `tls.Config` is a restriction asserted against oneself.

| Group offered | FIPS hub | Ordinary hub |
|---|---|---|
| X25519 | refused at the handshake | `X25519, 253 bits` |
| P-256 | `ECDH, prime256v1, 256 bits` | same |
| P-384 | `ECDH, secp384r1, 384 bits` | same |

Both hubs negotiated `TLS_AES_128_GCM_SHA256`, which is one of the two
SPEC 26.1 names. The restriction is conditional on FIPS mode rather than
a blanket change: the ordinary hub still takes X25519.

| Found by running it | What it was |
|---|---|
| `GODEBUG=fips140=on` enforces nothing | The setting SPEC 27.4 names routes approved algorithms through the module and leaves the rest reachable; HMAC-SHA-1 computed a digest under it. Everything the specification describes as holding "in FIPS mode" is `only`'s behaviour. 1.10 records what this build does instead. |
| A TOTP login would have panicked, not failed | Under `fips140=only` the module panics on HMAC-SHA-1 rather than returning an error, so an account with a second factor took the login handler down instead of being refused. |
| The suite assumed SHA-1 and Ed25519 were always there | Three tests failed as a FIPS build — two TOTP, one key generation — which is the same assumption any caller would have made. `make check` now runs the suite both ways so the assumption cannot come back. |
| A `-fips` binary need not be one | `GOFIPS140` is an environment variable, and a build that lost it produces a working binary with the right filename and the wrong cryptography. `make fips` asks the artifact what module it carries and refuses to ship one that answers wrong. |

What it does not cover: a FIPS build on Linux, which is the only
platform the artifact set ships for — this was run on FreeBSD. Nor an
actual FIPS-enabled kernel, so the `fips_mode` grain was false against a
`fips_build` of true throughout and the matching case was never seen. No
assessment has been done, and none of this is a claim of validation: it
is the Go Cryptographic Module doing the cryptography, and what is
certified is that module.


### 5.16 What writing the example policy found

`contrib/examples/policy.yaml` is documentation that executes: it is
loaded by the policy parser and the decisions its comments describe are
asserted, the same way the configuration examples are loaded as the
programs they are written for.

Writing it found that three of the six functions SPEC 23.5 names as
never granted by a wildcard were not declaring `arbitrary_code`, so
`functions: ['*']` granted them:

| Function | What a wildcard was granting |
|---|---|
| `cmd.shell` | A command line through a shell. A role deliberately refused `cmd.run` got the same power by asking for this instead. |
| `file.write` | Chosen content at a chosen path — a cron file, an `authorized_keys`, a unit file, a `sudoers` line. |
| `file.replace` | The same, by edit rather than by whole-file write. |

`cmd.shell` is the one that mattered. The control reads as enforced in
the log and in `policy show`, and the estate that carefully withheld
`cmd.run` from a role had given it away in the same breath. Found by
asking `policy test` what it decided rather than by reading the policy,
which is the difference between the two.

The list is now checked against SPEC 23.5's own names, written out in
the test rather than derived from the code, so that dropping a
declaration cannot also drop it from what the check compares against.

### 5.17 What writing the example account file found

`contrib/examples/accounts.yaml` is loaded by the account parser in a
test, with its claims asserted: which accounts carry a second factor,
which is disabled, and that every role it names exists in the example
policy — a role that does not grants nothing, silently.

Its password hashes were generated from 32 random bytes that were never
recorded, so nothing matches them. Two tests keep it that way. One tries
a short list of guessable passwords against each account, because an
example account file is exactly the thing somebody copies into
production intact. The other pins the digest of each shipped hash, which
costs no PBKDF2 and catches a replacement whether the new password is
guessable or not. Both were confirmed to fail against a hash of
`password` substituted into the file.

Writing it also found `halite-api`'s usage text still offering "Still to
come in phase 4: OIDC, LDAP, returners, and the bridge protocol", all
four of which ship. The audit that exists for exactly this —
`TestNothingClaimsADeliveredPhase` — had never been told phase 4 was
delivered. Adding it surfaced three more messages naming a phase that
had landed:

| Message | What was actually missing |
|---|---|
| An orchestration step's `ret` was "ineffective, returners are phase 4" | Returners ship. An orchestration step does not route its return through one. |
| The `saltutil.sync_all` runner was "phase 4, with the extension model" | The extension model ships, and the node-side `saltutil.sync_all` with it. The hub-side push is what is missing. |
| `smtp.send`, `slack.post`, and `http.query` were "phase 4, with the API" | The API ships. The hub has no outbound notification runner; the returners send from the return path instead. |

Each now names the subsystem rather than a phase, which is what the file
holding the third one already said to do: a message naming a phase goes
stale when the phase lands and the function still does not exist, and it
is worse than a missing feature — it is a working feature reporting
itself as absent, in a message nobody reads the source of.


### 5.18 What auditing the docs against the code found

Three defects, each surfaced by comparing a written claim with the code
rather than by reading either alone. All three were in what a new
operator meets first.

| Found by the comparison | What it was |
|---|---|
| Windows had no path layout | It fell through to the FHS branch, and `filepath.Join("/etc", "halite")` is `\etc\halite` there — configuration, enrollment key, and cache off the root of whichever drive the process started in, none of them the `%PROGRAMDATA%\Halite` SPEC 27.3 specifies. The test asserting the default asserted `/etc/halite` for everything that was not a BSD, so it would have failed on the first Windows run. 4.0 has the layout. |
| `pillar_roots` was marked hub-only | Every masterless node reads it, and the generated configuration reference taught otherwise. |
| `fileserver_backend` refused a backend it serves | The validator accepted `roots`, `git`, and `gitfs` while s3fs enables itself on `s3` or `s3fs`, so a hub configured for S3 was warned that this build did not serve it and then started the S3 file server on the next line. |

Commented settings in `contrib/examples` are now held to the same
standard as live ones. They never reach the loader, so a typo in one
shipped as documentation of a setting that does not exist, and most of
what an example teaches is commented out.

### 5.19 What auditing the migration tool found

`halite-hub migrate` is the first command anyone coming from Salt runs,
so a wrong answer there is expensive: it is the report that decides
whether the tree is thought portable at all.

| Found by running it on a real estate | What it was |
|---|---|
| Pillar was audited as state | A single-repository estate keeps pillar in `pillar/` beside its states. The state walk recursed into it and read every pillar file as a state, so a mapping of hostname to values came back as "beastie.example is not a state function this build ships", marked BLOCKING. Two blocking findings that did not exist. |
| `- match: grain` targets were invisible | The pillar-targeting check looked only for a `G@` sigil. A Salt tree writes `'nodename:host'` with `- match: grain` in the body, so the audit called the tree clean while the compiler refuses it — the same omission 5.9 records in the compiler itself, in the audit's own copy of the rule. |

Before: two blocking findings, neither real, and none of the four the
compiler actually refuses. After: no blocking findings, and the four
that predict what a real run does.

### 5.20 What a directory left owned by root cost twice

Two failures on the same host, both from a directory created by a
hand-run as root and then used by a service account.

The log directory made `service halite_hub start` fail with
`daemon: open: Permission denied`, naming no file. rc.subr drops to the
service account before daemon(8) runs, and the prestart created the
directory only when it was missing — deliberately, to leave an
operator's arrangement alone, which is exactly the case where the file
inside it then cannot be made. The prestart now creates the log file
itself, as root, owned by the account.

The node cache made every target match nothing. `MkdirAll` is satisfied
by a directory that already exists, whoever owns it, so the hub opened a
root-owned cache without complaint and could read nothing in it — and a
node whose cached data cannot be read is skipped during targeting. The
operator saw `no node matched "*"` immediately after `keys list` showed
the node accepted, which reads as a wrong target and sends them to fix
one that was right. The reason was a warning in a log the operator was
not reading.

Both halves are now refused where they can be seen: opening a node cache
this process cannot write fails at startup and names the directory, and
a target that could consider no node at all reports the nodes and the
reason rather than an empty match.

The general shape is worth keeping: `MkdirAll` and `[ -d ]` both answer
"does it exist", and neither answers "can this process use it". For a
service that changes account between a hand-run and a service start,
those are different questions.

### 5.21 What reviewing the service files against the tooling found

Read against FreeBSD's `daemon(8)` and `/etc/rc.subr`, and against a
running process for the signal, rather than by reading the files.

| Found | What it was |
|---|---|
| `${name}_program` is reserved | `rc.subr` assigns it over `command`. A FIPS switch using that name replaced `/usr/sbin/daemon` with the halite binary, which then received daemon's own flags as arguments. All three services failed to start, and the only clue was under `rc_debug`. Introduced by the FIPS work in this same series. |
| `${name}_user` is reserved too | `rc.subr` wraps the command in `su -m` itself, so passing `-u` to daemon as well made it drop privileges a second time as a non-root user. The pidfile also has to live somewhere that account can write, which `/var/run` is not. |
| `stop` and `restart` never worked | daemon's `-p` records the *child's* pid, and rc.subr matches it against `procname`, which defaults to `command` — so rc looked for `daemon` at a pid belonging to `halite-hub` and reported a running service as stopped. Measured both ways: with `procname`, "running as pid 63758"; without it, "not running". |
| `systemctl reload` was an outage | Nothing handles `SIGHUP`, so Go's default disposition terminates the process, and all three long-running units carried `ExecReload=/bin/kill -HUP $MAINPID`. Confirmed by sending it to a running hub. |
| The API could not write its tokens | `StateDirectory=halite-api` under `ProtectSystem=strict`, while the program defaulted `state_dir` to `/var/lib/halite` — read-only. |

What this did **not** establish: none of it was run as root, so
`daemon -u`, the real `/var/run` and `/var/log` paths, and
`limits -C daemon` are reasoned from the tooling rather than executed.
The exit codes each unit depends on were measured.

The systemd side was read rather than run when this was written. It has
since been run: an Ubuntu node has used `halite-node.service` to enrol
and apply a highstate, which is 4.5. The hub and API units remain
unexercised, so `ProtectSystem=strict`, the `ReadWritePaths` for the
enrollment CA, and `StateDirectory=halite-api` are still only read.

### 5.22 What the second node found

A second machine enrolled against the estate's own hub on 2026-08-28,
against a real Salt tree with encrypted pillar. Six defects, none of
which the tests could see, and every one of them reported a symptom
pointing somewhere other than its cause.

| The operator saw | What it was |
|---|---|
| `daemon: open: Permission denied`, no file named | The log directory survived an earlier install and was root-owned. rc.subr drops to the service account before daemon(8) runs, and the prestart created the directory only when missing — the case where the file inside it then cannot be made. |
| `no node matched "*"`, a line after `keys list` showed it accepted | `MkdirAll` is satisfied by an existing directory whoever owns it, so the hub opened a root-owned node cache and could read nothing in it. A node whose cached data cannot be read is skipped during targeting. |
| A correct fingerprint reported as not matching | The hub was running a build older than the node and served only its own certificate, so there was no CA in the chain to match. Reported as a fingerprint mismatch, which sent the operator to check the one thing that was right. |
| `no top file was found in any environment` | `file_roots` pointed at a symlink. Reading a named file resolved it and listing the tree did not, so the hub answered every file request correctly and reported an empty tree. |
| `{}` from `pillar items`, and a highstate that wrote the wrong file and reported success | The hub could not decrypt pillar — its keyring belonged to root and it runs as `halite` — and the node fell back to compiling its own, which with no local tree is empty. Every state reading pillar rendered against nothing. |
| An enrollment that could not be completed as instructed | The refusal named `halite-hub keys fingerprint` as the source of a certificate, and that command prints a fingerprint. |

Four of the six are the same shape: two paths that had to agree about a
fact and did not — the writer against the reader, the lister against the
fetcher, the producer of a status against its consumer. Each was fixed
by giving both sides one helper rather than by correcting one side.

The fifth is the one worth keeping in mind. A hub that cannot compile
pillar is not a hub that has none, and treating them alike turned a
broken secret store into an empty one silently: `admins=NONE` written to
disk and recorded as `Result: True`. In an estate that is an
`authorized_keys` with no keys. It was found by pulling on `{}` rather
than by anything failing.

What this did not establish: the estate is two FreeBSD machines. Nothing
here was run on Linux, and the systemd units remain unexercised.
### 5.23 Eleven of SPEC 26.2's metric families are not registered

The specification's table names thirty-two; this build registers
twenty-one. Counted mechanically against the source, not read off the
table:

| Not registered | Why it matters |
|---|---|
| `halite_state_run_duration_seconds`, `halite_state_compile_duration_seconds` | State runs are counted by outcome and not timed, so a highstate that is getting slower is invisible. |
| `halite_pillar_cache_hits_total` | The pillar cache is not instrumented. |
| `halite_pillar_ext_failures_total` | External pillar is not built at all, so this one waits on a feature rather than on the counter. |
| `halite_gitfs_fetch_duration_seconds`, `halite_gitfs_signature_failures_total` | gitfs fetches and verifies signatures, and neither is counted — a ref refused for an untrusted signature is a log line and nothing else. |
| `halite_event_subscriber_lag_seconds` | A subscriber falling behind the bus is not measurable. |
| `halite_beacon_dropped_total` | Beacon events are counted arriving and not counted dropped, which is half of the pair SPEC 26.2's own rule asks for. |
| `halite_ext_invocations_total`, `halite_ext_duration_seconds`, `halite_ext_timeouts_total` | The extension model ships and is entirely uninstrumented. A bridged extension timing out is a job failure with no counter behind it. |

SPEC 26.2 says "every bounded queue and every drop path in this
specification has a corresponding counter", and `halite_beacon_dropped_total`
and the extension timeouts are drop paths without one. That rule holds
for the reactor, the event bus, the returner spools, and the relay
spool, which all have theirs.

The gap was found while documenting the metrics rather than by anything
failing, which is the shape of it: an alert written from the
specification's table against a family that is not registered does not
error. It stays silent, and silence is what it would do if the estate
were healthy.

The operations guide lists what is registered, and names these eleven so
that a reader writing alerts has both halves.

### 5.24 What a real Prometheus scraper found

Standing up an external Prometheus against this estate on 2026-08-29 and
2026-08-30 found four faults, none of which any test could see, and the
first of them hid the other three.

- **The scrape had never run once.** `ca_file` pointed inside
  `/usr/local/etc/halite/pki`, which is `drwxr--r--` and owned by the
  `halite` account. Without the execute bit nothing else can open a file
  in it however permissive the file itself is, so Prometheus could not
  build the scrape pool. It logged one line per interval and nothing
  else noticed.
- **A failed scrape pool registers no target.** `up{job="halite"}` was
  *absent* rather than 0, so every alert in the metrics guide matched a
  series that was never created and stayed silent. This is the same
  shape as 5.23: a rule written against something that does not exist
  does not fail, it goes quiet, and quiet is what it would do if the
  estate were healthy. `absent(up{...})` is now in the documented rules
  and in the dashboard.
- **`ca_file` was the enrollment CA.** The API's serving certificate is
  its own and self-signed; `pki/ca.crt` signs node identities and does
  not sign it. The two are one directory apart and the guide had not
  said which.
- **`token_lifetime` defaults to 12h**, so a scraper's token dies
  overnight and the scrape starts failing with nothing to say why. The
  guide said "give it a long life" without saying the default was too
  short. `token_idle` cannot be turned off at all: zero means the 4h
  default rather than "never", and only a negative value disables it.

Separately, the API's own grant was missing and produced the quietest
failure of the set. `halite-api` merges its own exposition with the
hub's, and reads the hub's as an ordinary client — so `cert:CN=api`
needs `metrics.show` at the hub, which is a different principal from the
scraper's own account on a different hop. Without it the scrape
succeeds, `up` stays 1, and every `halite_hub_*` family is simply
missing. `halite_api_hub_scrape_failures_total` counted 92 before anyone
looked at it.

Nothing here was a code defect. All five were documentation that named
the right settings without saying what they had to contain, or grants
the guide prescribed and nobody had applied — which is the failure mode
5.18 was written about, found again in a feature documented after it.

### 5.25 What writing the Grafana dashboard found

The example dashboard of `contrib/examples/grafana-dashboard.json` was
written against the exposition and then checked against the live estate,
which found one real defect and one mistake of my own worth recording.

The defect: a histogram nothing had observed was exposed as
`halite_pillar_compile_duration_seconds 0` under a
`# TYPE ... histogram` declaration. An unlabelled family is written at
zero before its first observation so a scraper can see it exists, and
histograms took that same path — but a bare family name is what a
counter writes. A histogram's series are `_bucket`, `_sum` and `_count`,
so on a hub that had not yet compiled pillar there was nothing to query
and `histogram_quantile` had no buckets to read. `promtool check
metrics` accepts the old line, so a test pins the shape rather than
leaving it to the linter.

The mistake: the script that decided which panels were empty queried
each metric by its bare family name, so every histogram came back
absent, and the pillar panel was reported empty when its `_bucket`
series was present all along. The defect above is real; the symptom
first attributed to it was not. A check written minutes earlier is not
ground truth, and a negative result that confirms a theory already in
hand is the one most worth testing.

A test now parses the dashboard and holds every query — panels,
collapsed rows and template variables — to naming a family this build
registers, because a panel querying a metric that does not exist draws
an empty graph rather than failing. Descriptions are deliberately not
scanned: several name a family in prose to explain what goes missing
when a grant is absent.

Of 28 panels, 16 have data on this estate. Five are relay families on a
hub that is not a relay, and the rest are labelled families with no
events yet — a labelled family has no series until its first event, so
an empty panel is not evidence that nothing is happening.

### 5.26 What a large third-party Salt tree found

`halite migrate` was run against a shared Salt estate far larger than
the homelab this build has been developed against: 133 state files, 65
pillar files, 198 rendered, with orchestration and reactor trees. It is
the first tree exercised here that was written by people who had never
heard of halite, which is the only kind that finds what the author's own
habits hide.

The counts below are one run, on 2026-08-31, against one checkout. A
second run two days later saw 129 state files and 64 pillar files and
different totals, because the estate is somebody's working tree and
moves. They are the shape of what a tree like this carries, not a
measurement that reproduces.

**The audit's own bug came first.** Twenty-three of its 233 blocking
findings were wrong: an orchestration SLS is a state file by every
syntactic measure, and the audit judged every declaration against the
node-side state registry, which does not hold the `salt.*` steps or a
reaction and never will — they run on the hub. `salt.state` (10),
`salt.function` (6), `runner.state.orchestrate` (6) and
`local.saltutil.sync_grains` were reported as gaps against a build that
ships every one of them. That is worse than a missed finding: it sends
an operator to rewrite something that already works, and it inflates the
estimate that decides whether the migration is worth starting. The
orchestration and runner registries are consulted now, and such a
declaration is reported for review with the context it needs rather than
as a gap — nothing in a file says which of the three kinds it is, and
Salt does not mark them either.

Confirmed against the estate rather than against a fixture: the fixed
build reclassified exactly those 23 and left every real gap blocking,
taking the blocking count from 233 to 205. A reaction calling something
this build genuinely lacks still blocks and now names it —
`local.state.apply is a reaction calling an execution function on the
matched nodes, and state.apply is not an execution function this build
ships` — rather than calling the reaction itself wrong.

Two runs of the fixed audit were needed to establish that, because the
first was made by a binary that predated it and nothing in a report said
which build had produced it. That is fixed too: the header names the
build under the tree it audited. A checkout with no tags stamps the
commit alone, which still answers the question.

What the tree found that is real, with the reference count it carried:

**The template engine, and the only hard parse failure in 198 files.**
`{% break %}` at one call site. Salt enables three Jinja extensions —
`do`, `with_`, and `loopcontrols` — and this build has the first two.
`break` and `continue` are the third. Confirmed against the Salt on this
host: `salt/utils/templates.py` adds `jinja2.ext.loopcontrols`, and a
Jinja environment without it rejects the tag exactly as halite does.

**State functions missing from modules this build ships.** `grains.present`
(11) is the largest single gap in the tree: the grains *execution*
functions are all here and there is no grains state, so a tree that sets
a grain declaratively has nowhere to put it. Then `file.recurse` (4),
`pkgrepo.managed` and `pkgrepo.absent` (5), `test.show_notification` (4),
`pkg.purged` (3), `file.serialize` (2), and one reference each to
`file.rename`, `file.get_user`, `grains.absent`, `schedule.absent`,
`mount.mounted`, `shadow.gen_password` and `event.send`.

**A reactor incompatibility.** `saltutil.runner` (6) is Salt's other way
of calling a runner from a reaction; this build accepts only
`runner.<function>`. Also absent: `state.apply` and `grains.set` as
execution functions, both called from reactions.

**Modules SPEC never planned for.** `alternatives` (3),
`docker_container` and `docker_image` (2), `rabbitmq_policy`,
`rabbitmq_user` and `rabbitmq_vhost` (3), `kmod` (1), `macpackage` (1).
These are not gaps against SPEC — nothing promised them — but they are
migration blockers for a tree that uses them, and a reader deciding
whether to move needs them counted somewhere.

**Arguments Salt has and this build rejects.** Checked by introspecting
the Salt installed on this host rather than by reading its
documentation, which separates a gap here from a tree that was already
broken:

| Function | Arguments |
|---|---|
| `user.present` | `mindays`, `maxdays`, `inactdays`, `unique`, `optional_groups`, `enforce_password` |
| `archive.extracted` | `user`, `group`, `archive_format`, `options`, `skip_verify` |
| `group.present` | `system`, `members` |
| `file.managed` | `skip_verify`, `keep_source` |
| `file.replace` | `ignore_if_missing` |
| `pkg.installed` | `allow_updates` |
| `git.latest` | `fetch_tags` |

The `user.present` row is a coherent feature rather than seven
oversights: this build manages an account and not the shadow ageing
policy attached to it.

**A semantic difference worth deciding rather than fixing.**
`module.run` was reported for `user`, `cwd` and `rev`. Salt takes those
through `**kwargs` and hands them to the execution function being run,
so the state has no fixed parameter list; this build validates against
one. Strict validation is right for every other state and wrong for this
one, because pass-through is what `module.run` is. Nothing is decided
here.

**The report can now say what strict undefined costs.** SPEC 28.5 asks
for "every name that would fail under strict undefined, with file and
line", and the category was declared and never emitted — so the one
question an estate needs answered before SPEC 33 question 4 can be
decided had no data behind it. It is decided statically: rendering would
need the estate's pillar and grains, and would report every pillar value
as undefined, while a name no scope binds and no context supplies fails
whatever the data holds. It blocks, because strict is the default this
build renders with, and the finding names `permissive: true` as the
transition.

Two things it has to know to avoid reporting a tree that is fine: what
the renderer puts in the context, read from the renderer rather than
listed; and that a reactor SLS is a `.sls` like any other, so `data` and
`tag` count as defined everywhere. That second one is the orchestration
problem again — nothing in a file says which of the three kinds it is.

**What was the tree's own problem, and not this build's.** 58 duplicate
mapping keys, which SPEC 10.1.2 makes an error and Salt's loader
silently resolves; 12 pillar files targeting the `roles` grain, which a
node controls and SPEC 12.4 does not trust by default; 11 Python
extension directories, which is the bridged-extension path of SPEC 24.6
working as intended; and one `service.xk`, a typo the audit caught
statically that Salt would have found at run time.

## 6. Everything else not started

### 6.1 Delivery phases

Phases 0 through 4 are complete and phase 5 is under way; 6.1a and 6.1b
say where each landed. What follows is the record of how each phase landed,
in the order it did.

Phase 2 began with the identity half of it: the
enrollment CA of SPEC section 7, the mutual-TLS transport of section
6.1, and three of section 6.2's endpoints -- `/v1/health`, `/v1/enroll`
with its renewal, and `/v1/subscribe`. `halite-hub serve` runs, `keys`
manages the lifecycle, and `halite-node enroll`, `renew`, and `connect`
are the node's side of it. A hub and two nodes have been run against
each other; see 5.11.

Remote execution followed: `halite-hub run` submits on an operator
certificate, the hub resolves the target against the grains a node
reported, records the job with its expected respondents, and delivers
it; the node validates it against SPEC 6.3, runs it, and posts a return
that is filed in the job cache. `halite-hub jobs` reads that cache. A
highstate has been driven from a hub against two nodes, applied, and
run again to convergence.

The file server followed, and with it the exit criterion SPEC section
32 names for this phase: an operator edits the tree on the hub, and the
fleet converges to it. A node compiles against the hub's tree, caches
what it fetched, and asks conditionally afterwards, so a redeployed tree
with identical contents costs a round trip and no transfer.

Batching and the event bus followed, and with them the last four of
phase 2's stated contents. Every item SPEC section 32 lists for the
phase — transport, enrollment CA, targeting, remote execution, job
cache, file server `roots`, hub-side pillar compilation, RBAC, the
event bus — is built.

The named sub-features that were accepted and did nothing are done too:
the `queue` offline policy spools for a node that is off and refuses a
job that expired while it waited, `jobs kill` stops what has not
happened yet and says plainly that a node already running a state
finishes it, `/v1/grains` takes the refresh a node pushes on its
interval, and `file_ignore_regex` hides what it says it hides.

Batching is hub-side, which is the point: in Salt `--batch` lives in the
CLI, so closing the terminal abandons the run with half the estate
updated and no record of where it stopped. Here the group has its own
record, `jobs active` says what is in flight, and `jobs resume` picks up
a batch a hub restart interrupted. A safe limit stops the rest of the
estate getting the same broken change.

The event bus is a durable segmented log rather than Salt's in-memory
ZeroMQ bus. A subscriber resumes from an offset, so a reactor restart is
lossless and an incident can be reconstructed — which is exactly what a
Salt estate discovers it cannot do during one. A node's events are
namespaced under `halite/node/<its own id>/` whatever tag it asks for:
Salt's reactor runs with the control plane's full privilege, so a node
that can fire the right event can cause fleet-wide execution.

RBAC followed. A policy file grants a role a target and the functions
permitted against it together, a request must match one rule entirely,
and nothing is authorized without a rule -- including when the file is
absent, which a hub says at startup rather than treating as permission.
`halite-hub policy test` evaluates a hypothetical request and exits
non-zero on a denial, so a policy can be checked in CI.

A wildcard never grants a function that runs arbitrary code. The set
comes from the `arbitrary_code` flag on the signatures a build ships
rather than from a list, so a function marked in a later build is
covered without anyone remembering. In this build that is the `cmd.*`
family and `module.run`/`module.wait`; SPEC 23.5 also names
`cmd.script`, `cmd.shell`, `file.write`, and `file.replace`, none of
which this build ships.

Hub-side pillar followed that. A node posts its grains to `/v1/pillar`
and the hub compiles that node's pillar and nothing else -- which is
the point of moving the compilation: the node holds no other node's
secrets and cannot ask for them, because the identity comes from the
certificate. `pillar items`, `call`, and `state apply` on an enrolled
node go through the hub unless `--local` says otherwise, the way
`salt-call` uses its master. <!-- lexicon:allow -->

What is **not** built, in phase 2:

- **External pillar** (SPEC 12.7). `ext_pillar` is read only to warn
  that the sources it names contribute nothing, and `ext_pillar_fail`
  is read by nothing at all.
- ~~**`file_ignore_regex`.**~~ Built. Both forms hide paths from
  listing and from fetching, and a pattern that does not compile is
  fatal at startup rather than a rule that silently hides nothing —
  `internal/fileserver/roots.go`.
`fileserver_backend` accepts `roots`, `git`, and `s3`, and warns about
  anything else at startup rather than silently serving nothing.
- **`halite-hub files`** (`salt-cp`). The file server serves; pushing a
  file the other way is not built.
- **`/v1/mine`**, which is phase 3 along with orchestration, beacons,
  and the scheduler.
- **The event bus's tag-prefix index** (SPEC 17.2). A subscriber's
  globs are matched while reading rather than looked up, so a narrow
  glob over a long log reads the whole log. It is correct and it is
  linear.
- **Tokens** (SPEC 23.6). An operator authenticates with a certificate;
  there is no token issuance, which is what `halite-api` needs and
  which is phase 4.
- **The RBAC principals that are not certificates.** OIDC is phase 4
  and is now built. The `node:` principal is produced and enforced on
  the read half of SPEC 19.5's peer interface: a node asking the mine
  for another node's data is authorized against the policy,
  deny-by-default, in `internal/hub/mine.go`. The execute half —
  `publish.*`, one node running a job on another — is not built.
- **Return chunking.** A return is one request; the 16 MiB paginating
  path of SPEC 6.5 is not built.

Phase 3 has started with the runners of SPEC 19.2. `halite-hub runner`
calls them over the same operator certificate `run` uses, and they are
granted by the `runners:` list of a role rather than by `functions:`,
because permission to ask the hub a question is not permission to run a
command on every node. A runner that reaches the fleet is authorized a
second time as the job it dispatches, so the narrower grant cannot
become the wider one. Every call gets a jid, is filed in the job cache
with the principal that asked, and emits `halite/run/<jid>/new` and
`halite/run/<jid>/ret`.

Built: `jobs`, `manage`, `key`, `nodegroups`, `pillar`, `cache`,
`fileserver` (the parts a filesystem backend can answer), `event`,
`saltutil`, `survey`, and `error` — 42 functions. The rest of the
SPEC 19.2 inventory is **registered and not built**, each answering with
the phase it arrives in. That is deliberate: a name left out of the
registry makes "orchestration is not written yet" and "you have mistyped
`state.orchestrate`" the same message at the terminal.

Three of those pending entries are not waiting on a phase but on a
subsystem this design does not have:

- **`pillar.clear_cache`, `cache.pillar`, `cache.clear_pillar`.** The
  hub compiles pillar on every request and caches none, so there is
  nothing to clear. `pillar.show_pillar` compiles it on demand.
- **`fileserver.clear_cache`, `lock`, `clear_lock`, `versions`.**
  `fileserver.update` is built and fetches both git and s3; the rest
  have no counterpart here. There is no update lock because the hub
  rebuilds the whole search path in one step rather than mutating it,
  and a cache that is verified on every read is not one to clear.
- **`fileserver.symlink_list`.** The roots backend resolves symbolic
  links and does not list them.

`key.revoke` is an addition: SPEC 19.2's `key` row does not name it,
and a lifecycle with `accept`, `reject`, and `delete` but no way to
withdraw an acceptance from an orchestration is missing the one an
incident needs.

**Orchestration followed**, which is the other half of phase 3's exit
criterion. `halite-hub orch run <sls>` compiles an orchestration on the
hub and runs it, and an orchestration here *is* a state run whose
modules act on the fleet: the compiler and the runner are the node's,
unchanged, so `require`, `onfail`, `prereq`, and ordering mean exactly
what they mean in a highstate. Writing a second set for the hub would
have meant two implementations of the requisites that have to agree.

Built: `salt.state`, `salt.sls`, `salt.highstate`, `salt.function`,
`salt.runner`, `salt.wheel`, and `salt.wait_for_event`. Each step is
authorized twice — once as the orchestration, again as the job it
dispatches. A run is a first-class record kept on disk with its own jid,
every step in the order it ran, and the per-node returns; `orch show`
prints it and `orch resume <jid> --from <step>` picks it up, carrying
the earlier steps forward as they finished. Salt cannot resume, and SPEC
19.1 names that as the reason a long deployment orchestration is usable
here and not there.

What is **not** built in orchestration:

- **`salt.parallel`** and the per-step `parallel`. This build runs a low
  state in one order, one step at a time; see 4.4.
- **`queue`.** A step asking to be held on the hub's durable queue is
  refused by name rather than run immediately, and the queue runner is
  still pending (SPEC 19.4).
- **`state.pause` and `state.resume`**, which hold a *running*
  orchestration. Resuming a finished one works; pausing a live one does
  not exist.
- **`salt.wheel` as a separate namespace.** SPEC 19.3 lists wheel apart
  from the runners; this build has one hub-function namespace, and a
  `salt.wheel` step reaches the same registry a `salt.runner` step does.
- **A pillar of the hub's own.** An orchestration template sees exactly
  the `pillar` the caller passed and nothing else. There is no
  hub-as-a-node pillar compilation, and SPEC 25.5's restricted `salt`
  dispatcher is not built either, so an orchestration template has none
  rather than one that has not been audited against that list.

**Reactors followed**, which completes the output side of the automation
loop. `reactor:` maps a tag glob to reaction SLS; the four reaction
types and the SLS syntax are Salt's, so an existing reaction translates
unchanged. Two things are not Salt's:

- **A reaction is authorized.** Each entry names a `principal` and is
  subject to the RBAC policy exactly like a human caller. Salt's reactor
  runs with the control plane's full privilege, so a node that can fire
  the right event can cause arbitrary fleet-wide execution. An entry
  that names no principal gets a restricted default which is bound to
  nothing, so it is refused until someone writes what it may do. A
  `caller` reaction runs on the node that fired the event and nowhere
  else.
- **It does not serialize.** Salt's reactor is single-threaded, so a
  burst becomes a backlog and the backlog becomes an outage; SPEC 18.2
  calls this the most common scaling failure in a Salt estate. Here it
  is a worker pool with same-chain events hashed to a fixed worker, a
  bounded queue that drops the oldest and reports the count rather than
  blocking the bus reader, and per-glob `debounce`, `dedupe_window`, and
  `rate_limit`. A reaction that fails to render or dispatch emits
  `halite/reactor/error`; Salt fails that silently and the event does
  not come again.

Every event the reactor acts on belongs to a causality chain, carried
into the jobs a reaction dispatches and into the events those produce,
so the beacon-fires-reactor-changes-the-file loop of SPEC 16.3 is
countable and is broken at `max_causality_depth`.

What is **not** built in the reactor:

- **A node's own reactor.** SPEC 18.1's `caller` type exists and runs on
  the node that fired the event, dispatched from the hub. A reactor
  configured on a node, reacting to its own local bus without the hub,
  is not built — there is no node-side bus yet (SPEC 17.3).
- **`salt` in a reaction template.** SPEC 25.5 restricts the hub's
  dispatcher to a named safe set, and this build gives a reaction none
  rather than one that has not been audited against that list. `data`,
  `tag`, and `id` are bound; `grains` and `pillar` are bound empty.
- **Correlation from a node-fired event.** A chain that begins with
  `event.send` on a node carries no identifier of its own, so the
  reactor names the chain when it first sees the event. A node that
  wants to join an existing chain cannot say so.

**Beacons followed**, which closes the loop: a file changes on a node,
the beacon fires, the hub's reactor acts. A beacon here is a function
over the node's own execution modules rather than a second reader of the
system, so it is portable wherever its module is and cannot disagree
with the state that acts on the same fact.

Built: `diskusage`, `load`, `memusage`, `service`, `filechanges`,
`cert_info`, and `status`. The controls of SPEC 16.3 are all there — a
token bucket per instance, coalescing with a count, a bounded queue that
reports what it dropped, and `disable_during_state_run`.

What is **not** built in beacons:

- **`inotify` and `fanotify`.** Both need raw syscalls through
  `golang.org/x/sys`, which SPEC 4.2 records as an open question and
  which this build does not admit. `filechanges` polls on digest and
  metadata, which is the portable answer SPEC 16.2 names for exactly
  this case; it is slower, and a change that is reverted between two
  polls is one it never sees.
- **Seventeen of SPEC 16.2's inventory**: `swapusage`, `cpuusage`,
  `network_info`, `network_settings`, `proc`, `ps`, `pkg`, `journald`,
  `log`, `wtmp`, `btmp`, `sh`, and the four platform notifiers. Each is
  registered and answers with when it arrives, so a configuration
  naming one is refused with a reason rather than skipped.
- **Beacons through pillar.** SPEC 16.1 names three sources: the
  configuration file, `beacons.d`, and pillar. The first two work; a
  beacon delivered through pillar does not.
- **The default interval.** Salt polls every second by default; this
  build polls every minute unless the beacon says otherwise. Reading the
  filesystem once a second for a threshold that moves in hours is a cost
  with no benefit, and every example in SPEC 16.1 names an interval.

**The scheduler followed.** `schedule:` runs jobs on a clock with no hub
involved, which is how a node keeps itself converged during a hub
outage. The cron parser is written directly — five fields, ranges,
steps, lists, names, the `@` shorthands, and standard cron's rule that
both day fields restricted means either matches. `L`, `W`, `#`, `?`,
and a seconds field are refused by name.

Time handling follows SPEC 20.1 exactly, because it is where missed runs
come from: the walk is over wall-clock fields rather than absolute time,
so a repeated hour runs a job once and a skipped hour runs it once at
the transition. The transition instant is computed rather than read off
the result, because Go resolves a nonexistent local time to one side of
the gap without documenting which.

What is **not** built in the scheduler:

- **Schedules through pillar.** SPEC 20.1 names three sources: the
  configuration file, `schedule.d`, and pillar. The first two work; a
  job delivered through pillar does not.
- **A node's own reactor** (SPEC 20.2). A beacon on a node reaches the
  hub's reactor; there is no local bus and no local reaction, so
  self-healing still needs the hub.
- **Returners** (SPEC 20.3). `local` is built, which is the default:
  append-only NDJSON on the node, and it is where a scheduled job's
  return goes. `local_cache` is not — the hub refuses a return for a job
  it never dispatched — and neither are `syslog`, `file`, `webhook`, or
  `smtp`. A `returner:` naming any of them is refused at startup rather
  than accepted and written nowhere.
- **`jid_include`.** Accepted and means nothing here: every job this
  scheduler runs is recorded under a jid either way.

**The mine followed**, which completes the contents SPEC section 32
lists for phase 3. `mine_functions` publishes; another node's state
reads. The store is on the hub, because a node asking another node
directly would be a second authorization surface and a connection in the
wrong direction (SPEC 5.1).

Reading is the peer interface, expressed in the one RBAC policy rather
than in Salt's separate `peer` dialect: the caller is a `node:`
principal, and a grant names the functions and the targets and nothing
wider. `allow_tgt` is the publisher's own restriction on top of that —
a node publishing something sensitive decides who may see it without
trusting every reader's policy to be right.

What is **not** built in the mine:

- **Node-initiated execution on other nodes.** SPEC 19.5's peer
  interface covers reading the mine, which works, and `publish.publish`
  and `publish.runner` — a node causing a job to run elsewhere — which
  are not built. The RBAC shape they would use is the one the mine
  already uses.
- **`mine_interval` finer than a minute.** Salt's unit is minutes and
  this reads it the same way, so a node cannot publish more often than
  that without `mine.send`.

**The runtime management of both followed.** The nineteen functions of
SPEC 16.1 and 20.1 act on the running engines: a watcher or a schedule
that can only be changed by restarting the node is one nobody changes
during an incident, which is when the reason to change it usually
arrives.

Both engines reconcile against their configured set on every pass rather
than starting a goroutine per entry at boot, which is what lets an entry
added later take effect. Disabling holds without forgetting, so enabling
restores exactly what was there. Modifying keeps what the change did not
mention — a beacon turned off stays off when its threshold is fixed, and
a job keeps its last run so an interval does not restart because someone
adjusted it.

`save` writes to `beacons.d/99-runtime.yaml` and
`schedule.d/99-runtime.yaml`: a file of the node's own, numbered last so
a runtime change beats the file it was made against, and never over what
a package manager put there. What it writes parses back into the same
beacons and jobs.

One shape is refused deliberately. A fragment in either directory is a
mapping of names to definitions with no `beacons:` or `schedule:` above
them, because the directory already says what they are. Written in the
shape of the main configuration file it produces a beacon called
`beacons`, and the node then complains about a name nobody typed — so it
is refused per fragment, with the fix in the message. Per fragment
rather than after the merge: mixed with an unwrapped file, a wrapper
would otherwise slip through.

### 6.1a Phase 4, delivered

**The API's authentication spine is built.** `halite-api serve` runs,
holding its own operator certificate as a client of the hub: login,
logout, token introspection, the module schema, and health, with the
transport hardening of SPEC 22.3 on every response.

Local accounts are PBKDF2-HMAC-SHA-512 through the standard library,
each hash carrying its own cost so the floor can be raised without
invalidating what is stored. A hash below the floor is refused rather
than accepted and re-hashed on the next login: an operator has to know
it is there. TOTP is built, RFC 6238, one step either side.

Tokens are 256 bits from `crypto/rand` stored as a SHA-256 digest, with
both expiries, an optional source network, roles frozen at issue, and
revocation individually or by principal.

**The execution endpoints are built.** `/v1/run`, `/v1/jobs`,
`/v1/jobs/{jid}`, `/v1/nodes`, `/v1/nodes/{id}`,
`/v1/nodes/{id}/state`, `/v1/keys`, `/v1/orch`, and `/v1/pillar/{id}`.
Every one of them is authorized **twice**: the operator behind the token
at the API, against the token's frozen roles, and the API's own
certificate at the hub. A job carries `on_behalf_of` beside `submitter`,
recorded and never trusted — the hub decides on the certificate in front
of it, not on a name in a payload.

**The event stream is built**, as one stream with two transports.
`GET /v1/events` is SSE whose `id:` is the bus offset, so a
`Last-Event-ID` on reconnection resumes where the connection dropped
rather than at "now". `GET /v1/ws/events` carries the same events over a
WebSocket, hand-rolled against the standard library because SPEC 4.2
allows no third-party code: masked client frames required, fragment
reassembly, a length claim refused before anything is allocated for it,
and a ping every thirty seconds so an intermediary does not close a
quiet stream.

Both transports share one filter. A tag naming a node reaches only a
caller whose policy targets that node; an event about no node in
particular — a job, a reactor error — reaches any caller the policy
grants something, and a principal bound to nothing sees no events at
all. The filter is shared rather than written twice on purpose: two
authorization paths over the same events are two chances to leak, and
the one that leaks is the one nobody tested.

**Webhook ingress is built.** `POST /v1/hook/{path}` is authenticated by
construction, as SPEC 22.2 requires: there is no configuration that
produces an unauthenticated hook, and a hook with no credential
configured is refused at load rather than served. HMAC-SHA-256 over the
timestamp and the raw bytes, a replay window, a nonce cache, a
content-type allowlist, and a body limit. A delivery becomes an event
under `halite/hook/<path>` carrying the principal it authenticated as,
so a reaction authorizes on that and never on the payload.

The nonce is recorded once the delivery has landed on the bus, not when
the signature verifies. Recording it earlier is strictly safer against a
replay and costs more than it saves: a delivery that fails downstream is
one the sender will retry carrying the same signature, and refusing that
as a replay turns a transient fault into the lost event a webhook exists
to prevent.

**Metrics are built**, on both components. `internal/metrics` writes
Prometheus text exposition directly, because SPEC 26.2 says the format
is documented and stable and needs no client library, and SPEC 4.2 says
a dependency in a control plane's supply chain needs a better reason
than saving a hundred lines of formatting.

`GET /v1/metrics` on the API is the estate's scrape target and is
authenticated by default, as SPEC 22.1 requires. It answers with both
expositions — its own and the hub's, fetched under its own certificate
and *merged*, because the text format allows one `# HELP` per metric
name and both components expose `halite_build_info`. A hub that cannot
be reached does not fail the scrape: the reason comes back as a comment
and the service's own numbers survive, one of which counts how often
that happens.

The hub has the same endpoint behind its ordinary operator certificate,
granted as the runner `metrics.show`, plus `halite-hub metrics` to read
it — the hub speaks its own ALPN protocol, so no scraper can reach it
directly. An unauthenticated scrape endpoint on a control plane tells
anyone who asks how many nodes it has and when a deployment went out.

Two decisions the format does not require. A family is declared before
anything has been observed, so SPEC 26.2's rule — every bounded queue
and every drop path has a counter — can be checked by a scraper rather
than by reading the source. And a family holds at most 512 series, with
the excess counted under `__overflow__`: every label the specification
names is written by something outside the program, so an estate with a
thousand distinct functions would otherwise turn one family into a
thousand series.

**Returners are built**, all six SPEC 20.3 marks Full: `local` and
`file` (append-only NDJSON, the second with rotation), `local_cache`,
`syslog` (RFC 5424 written directly, because `log/syslog` speaks the
older RFC 3164 and does not exist on Windows), `webhook`, and `smtp`.
The sixteen marked Bridged are refused by name as bridged rather than
as typos.

The webhook returner is where SPEC 20.3 asks for three things together
— HMAC-SHA-256 body signing, retry with backoff, and a durable spool —
and the third is what makes the other two worth having. Without it the
returns lost are exactly the ones from the incident that took the
receiver down. The backlog goes out ahead of new returns so the order
survives; a 4xx is not retried, because a request the receiver will
never accept would otherwise fill a disk; and a full spool refuses
rather than making room.

`event_return` ships the whole bus, resuming from an offset, and a
delivery failure does not advance it.

**OIDC is built**, both paths of SPEC 23.4: Authorization Code with
PKCE for an interactive operator, and a token presented directly for
automation with no browser.

The accepted algorithm list is this package's own rather than a
library's default, which is the point of writing it here: the nine
SPEC 23.4 names, with `none` and every `HS*` absent. That closes the
algorithm confusion attack, where a token is signed with the provider's
public key as an HMAC secret and a verifier that trusts the header's
`alg` accepts it. The algorithm's key type is checked against the key
that was found, so a header claiming RSA cannot verify against an EC
key, and a token with no `exp` is refused because one that never expires
is a password with a longer name.

The key set respects `Cache-Control: max-age` bounded at five minutes
and a day, and an unknown `kid` causes one rate-limited refresh — a
rotation is invisible, and a stream of invented key identifiers is not a
way to make this service hammer the provider on somebody's behalf.

Groups map to roles through a table the estate writes, and a group with
no entry grants nothing: the provider's directory is not this estate's
authorization model. An operator whose groups all map to nothing is told
which groups they had. A session never outlives the assertion it was
made on.

**LDAP is built**, the narrow surface SPEC 23.3 specifies: the six
operations it names, simple bind over LDAPS or StartTLS, no referral
chasing, and no plaintext mode at all. Written against `encoding/asn1`
through `asn1.RawValue`, which gives the application and context tag
control LDAP needs while leaving length encoding — the classic place to
get BER wrong — to the standard library.

Anonymous bind is refused in both directions. The service account is
required, and an empty operator password is refused before the directory
is asked: RFC 4513 makes an empty password an anonymous bind, which a
directory answers success to, so passing one through authenticates
anybody who leaves the field blank.

The username never becomes part of a DN. It goes into a filter, escaped
per RFC 4515, and the filter is parsed into BER rather than concatenated
as text. A filter matching two entries is refused rather than binding as
whichever the directory listed first.

Groups come from `memberOf`, a group search, or both, with nested groups
followed to a configured depth for Active Directory and a cycle
terminating rather than hanging.

**The extension model is built**, which is SPEC section 24 and the
centre of the supply-chain goal. Salt's extensibility is a Python file
dropped in `_modules/` on the file server, which the agent imports and
runs in process, as root, with no signature requirement. SPEC 24.1 calls
that a code distribution channel.

An extension here is a separate executable speaking length-prefixed JSON
over stdio. Length-prefixed rather than newline-delimited: a frame
boundary must not depend on an extension never emitting a newline inside
a string. Concurrency is a process pool, so an extension never has to be
thread-safe; a hung one is killed and replaced, so it cannot hang the
agent; a protocol violation kills the process rather than failing the
call, because an extension that sent something unreadable has lost its
place in the stream.

A bundle is signed with Ed25519 over a Merkle root of its contents, and
verified on **every load** rather than once at fetch — the cache is a
directory on a managed node. Verification runs both ways: a listed file
whose digest is wrong is tampering, and an unlisted file that is present
is one nobody signed, in a directory the extension can load from. The
root covers paths as well as contents, so a bundle cannot swap which
file is the executable without changing what it is signed as. The signed
message carries a domain separator, so a bundle signature can never be
replayed as a signature over anything else this project signs.

`Sandbox.Describe`, which `sys.list_extensions` shows, says what is
actually enforced on the machine in front of the operator rather than
what SPEC 24.3's table hopes for across five operating systems. Built:
the process boundary, a dropped identity, a process group so a kill
takes the children, and resource limits. Not built and named as such:
Landlock, seccomp-bpf, `pledge`, `unveil`, and Windows job objects. The
resource limits are applied by the child rather than the host, because
`setrlimit` applies to the calling process — so they hold for an
extension built against this protocol and not for an arbitrary one, and
the description says that too.

`RLIMIT_AS` is available and off by default: it bounds virtual address
space, and a garbage-collected runtime reserves far more of that than it
commits. A Go extension under a 512 MiB limit dies after about 160 MiB,
measured on this build's own test extension.

**Synchronization is built**, as SPEC 24.5's mapping of
`saltutil.sync_all` and six per-kind variants. It fetches and does not
load — the behavioural difference the specification states plainly — and
the answer says so when anything arrived. A bundle is verified in a
staging directory and moved into the cache only if it verifies, so a
node running a good version does not lose it because somebody published
a bad one. A bundle published at one path and signed as another is
refused, and an extension pinned to a different version is not fetched
at all.

**The bridged returners are built.** SPEC 20.3's sixteen are
extensions of kind `returner`, found by name, so `returner: postgres`
does not require the operator to know it is one.

**The bridge skeleton generator is built**, which is SPEC 24.6 and the
last piece of phase 4. `migrate --bridge-skeleton <dir>` reads a
formula's Python modules and writes one Go command per module with the
signatures filled in. It honours `__virtualname__`, skips `_private`
functions and `__virtual__` as Salt's loader does, and handles the
shapes real formulas use — multi-line signatures, list defaults holding
commas, both docstring quote styles, `*args` and `**kwargs`. Every
generated function returns an error, so a bridge that was generated and
forgotten fails loudly. A test parses the output as Go, which is what
caught the signature JSON being emitted inside a raw string literal: a
docstring containing a backtick produced code that would not build.

What is **not** built in the API:

- **Node-side metrics.** A node has no exposition endpoint, so what only
  it knows is counted nowhere: a beacon event its own queue dropped, a
  local state run's duration, and the scheduler's `maxrunning` skips.
  The hub counts what reaches it, which is most of SPEC 26.2's state and
  beacon families but not the drops.
- **Tracing** (SPEC 26.3) and **`doctor`** (SPEC 26.4), the other two
  parts of section 26.
- **`mtls` hook authentication.** The mode is implemented and refused
  when no client certificate is presented, but it has never been
  exercised against a real sender.

Phase 5 is part built — gitfs, s3fs, the agentless path, relays and the
FIPS artifact set are in, and 6.1b says what each covers. What is
absent from 5 and 6: Windows and macOS parity, detached job signing,
signed state trees, the render sandbox, node-side evidence, and the
backtracking regex engine.

The runners have been run against a hub and a node as separate
processes; 5.12 says what that established and what it did not.

Two named pieces of section 7 are also absent:

- **`attested` enrollment** (SPEC 7.3). `manual` and `token` are built.
  `enrollment_mode: attested` is refused by name rather than accepted
  and ignored.
- **`keys rotate-ca`** (SPEC 7.5). There is one CA generation. Creating
  a second one in the same directory is refused, so the failure mode is
  a message rather than an estate that has to enrol again.

A subcommand whose phase has not landed still reports that by name
rather than failing obscurely, which is deliberate: the alternative is a
binary that appears to work.

### 6.1b Phase 5, started

**The git file server is built**, SPEC 13.3. It invokes the system `git`
binary rather than linking pygit2 or libgit2 — together a large C
dependency with its own CVE history — so an estate gets its operating
system's git patching cadence.

The shape carries the weight: a bare mirror is fetched and verified, and
the served ref is materialised into a directory that becomes a `roots`
search path. The manifest, hashing, ignore globs, conditional requests,
and ranges are the existing code. A gitfs that served blobs through its
own path would be a second implementation of file serving, and the
second one is the one with the traversal bug in it.

`gitfs_verify_signatures` is a control rather than a log line: a ref
whose tip is not signed by a key in `gitfs_keyring` is not served.
Verification with no keyring is refused, because checking against the
hub user's own GnuPG home would pass for whatever that user happens to
trust.

A remote that fails, and a remote whose refs are all refused, both leave
the last tree that verified in place — a network blip or a withdrawn
signing key must not take the estate's state tree away. A branch deleted
upstream does stop being served.

The archive extractor refuses symlinks, device nodes, and anything
writing outside the tree, and bounds file count and total size. `git
archive` produces none of those; this unpacks an archive built from a
repository the hub did not write.

**Agentless mode is built**, SPEC section 21. `halite-hub ssh` pushes a
static `halite-node`, verified by digest after transfer and cached at
`<thin_dir>/<digest>`, and runs the job through a one-shot mode that
reads it on stdin and writes a framed return. The connection is the
system `ssh` binary, so an estate's `ssh_config`, `ProxyJump`,
certificate authentication, and `known_hosts` handling all work without
being reimplemented — and `paramiko`, the largest dependency in
`salt-ssh`, is not replaced by anything.

Pillar and the state tree are compiled on the hub and sent inline, so a
target holds no tree and no other target's secrets. The target uses what
the hub sent and only that.

Rosters: `flat`, `sshconfig`, `cache`, and `ansible`. Targeting is the
grammar of SPEC section 8 against the roster's grains, so an agentless
estate is targeted exactly as an enrolled one.

SPEC 21.3's limitations are inherent rather than gaps: no persistent
connection means no beacons, no scheduler, no mine, no presence, and no
node-initiated events for an agentless target.

**The S3 file server is built**, SPEC 13.4, with SigV4 written directly
rather than by importing the AWS SDK — hundreds of packages to satisfy
one signing algorithm. Credentials resolve in the specified order:
explicit configuration, the environment, the container credential
endpoint, then the instance metadata service, with IMDSv2 only and no
fallback to v1. IRSA is tried first when configured, because a pod with
a web identity token has it instead of the node's role. Endpoints and
the STS host are built from a partition value rather than hardcoded to
`aws`.

The signing is checked against AWS's published derivation of the signing
key for its documented example credentials, and in the lab against an S3
that recomputes the signature with its own implementation — because "our
implementation agrees with itself" is exactly the property a signing bug
preserves.

**Relays are built**, SPEC 5.3. A hub with `relay: true` serves its own
segment and appears to its upstream as one connected client; the
upstream holds no key for the nodes behind it, only the relay's
assertion, accepted from a certificate its policy grants `relay.proxy`.
Depth is capped at two.

Two things the syndic does not do. Returns are spooled durably through
an upstream outage and drained oldest-first when it returns, so the
outage delays returns rather than losing them; and event forwarding is
chosen by tag glob rather than being all or nothing, so a busy segment
forwards its job returns and keeps its beacon chatter local. 5.14
records what running it found, and 1.8 and 1.9 record what a relay
deliberately does not forward and what the upstream trusts it for.

**The FIPS artifact set is built**, SPEC 27.4. `make fips` produces
`-fips` binaries against the certified Go Cryptographic Module, and
refuses to ship one whose own `version` output does not name the module
and report its self-tests. `make fips-cross` is the release set, for the
tier 1 platforms only. `make check` runs the whole suite both ways.

In FIPS mode Ed25519 is refused by name, TOTP is refused and the
accounts it locks out are named at startup, and key exchange is P-256 or
P-384. 1.10 records why those are this build's doing rather than the
`GODEBUG` setting's, 1.11 why the grain is a pair, and 5.15 what running
it established. `doctor`, which SPEC 27.4 gives the mismatch warning to,
is not built.

What is **not** built in phase 5:

- **The reverse tunnel** of SPEC 21.1. Pillar and tree go inline, and a
  tree larger than 4 MiB is refused rather than transferred on every run
  against every target.
- **The `scan`, `cloud`, and `terraform` rosters** of SPEC 21.2, each
  refused by name.
- **Windows and macOS parity.** The code cross-compiles and none of it
  has been run there.
- **`minionfs`/`nodefs`**, which SPEC 13.2 marks a subset and disables
  by default.

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
It has been run against a real estate tree of 129 state files and 64
pillar files (5.26), and against a smaller real tree (5.9), as well as
against synthetic ones. What it has not been run against is a Salt tree
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
down to isolated cases — 15, of which eight are the extractor's limits
rather than the engine's — with no cluster left. The one that was a cluster, whitespace control, turned out to be a
single rule applied in four wrong places and is closed. The YAML suite
has moved *up*, not because the count changed but because the reason
changed: see 3.

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
3. **The documents accepted that should be refused** (5.4). 20 left in
   the conformance suite, down from 23, and none in the PyYAML
   differential. The framing in earlier versions of this file was wrong:
   accepting a document the reference implementation refuses is not the
   safe direction, because it means a tree Salt would not load loads
   here and means something nobody wrote. What remains is tabs in odd
   positions, document markers inside quoted scalars, and under-indented
   continuations — none of which a Salt tree contains, which is why this
   sits below the two above it.
4. **The remaining 15 template conformance gaps** (5.5), of which eight
   need a Python callable the corpus extractor cannot carry and are not
   engine gaps at all. The six that are real are one feature each.
