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
is a deployment mistake neither fact finds alone. `doctor` now compares
them and warns in both directions, with a different remedy for each;
5.30 has the reasoning, including why a platform with no kernel FIPS
mode is a skip rather than a warning.

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

### 1.13 A node listens, when an operator asks it to

SPEC 6.1 has a node dial the hub and be dialled by nothing, and every
other part of this build holds to that: there is no inbound control
path to a node, no port to reach it on, and nothing an attacker can
connect to.

`metrics_listen` is one deliberate exception. Set, the agent serves
`/v1/metrics` on that address and nothing else — an unrouted path gets
a 404 rather than a description of what might be there. Unset, which is
the default, a node opens no port at all and the divergence does not
exist on that machine.

The first cut avoided the listener entirely by writing the exposition
to a file for node_exporter's textfile collector. That works and needs
no port, and it was dropped because it needs node_exporter: two of the
five machines in the estate this was tried on did not have one, the
collector directory differs per platform, and an estate ends up
depending on somebody else's agent to see its own control plane.

The endpoint is not a second control surface. It reads, it serves one
path, and it takes a serving certificate the operator supplies —
there is no plaintext mode, because a node's exposition says which
functions ran, which extensions, and when a deployment went out. That
is the argument SPEC 26.2's own endpoints are built on, and it does not
weaken because the subject is one machine. `metrics_client_ca` goes
further and refuses a scraper that presents no certificate of its own.

The node's own `node.crt` is deliberately not reused. It is issued with
`ExtKeyUsage: ClientAuth` and carries no DNS or IP name, so it cannot
serve TLS and a scraper would have nothing to verify it against. Giving
a node a second, serving certificate would mean a new certificate type,
names requested at enrollment and vetted by the hub, and a renewal path
covering both — a PKI feature rather than a listener, and not one this
change makes.

## 2. Module coverage

The build ships **77 execution modules / 532 functions** and **44 state
modules / 119 functions**.

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
| `environ` | implemented | 6 | `setval` and `setenv` write the agent's own environment, and with `permanent` the place the platform keeps it: `/etc/environment` on a unix, the environment key of the registry on Windows. `persisted` reads that store back |
| `event` | implemented | 1 | local only until the hub exists |
| `file` | implemented | 46 | `patch` runs the system patch with `--forward`, because left to itself it reverses an already-applied patch and exits 0; `sed` is done in Go rather than by an editor, and its `limit` is a real per-line filter; `list_backups` and `restore_backup` read the cache a state fills with `backup: node` |
| `git` | implemented | 5 | through the system `git` binary |
| `grains` | implemented | 7 | |
| `group` | implemented | 1 | |
| `hashutil` | implemented | 9 | |
| `hosts` | implemented | 3 | |
| `mine` | implemented | 6 | the store is on the hub; a node publishes its own and reads others' through the RBAC policy |
| `mount` | implemented | 9 | the running table comes from `/proc/self/mounts` where there is one and from parsing `mount` where there is not, and how much a declared option can be compared against it depends on which; declared for the platforms with a mount table and refused on Windows, whose mount points are drive letters and junctions rather than an fstab |
| `network` | implemented | 5 | |
| `pillar` | implemented | 5 | |
| `pkg` | implemented | 23 | `info_installed`, `file_dict`, `download`, `list_downloaded` and `autoremove` are apt-only, verified live (5.40); see 2.5. `version_cmp` implements the Debian and RPM orderings directly and asks pkg(8) for FreeBSD's |
| `random` | implemented | 3 | `crypto/rand` |
| `saltutil` | implemented | 9 | |
| `service` | implemented | 16 | systemd (over D-Bus, 5.39) and FreeBSD rc verified; see 2.5 |
| `ssh_auth` | implemented | 2 | registered as `ssh.auth_keys` and `ssh.known_hosts`; SPEC 15.2 names no module for either state, and both read the same account's files, so they share one |
| `status` | implemented | 4 | |
| `sys` | implemented | 11 | `evidence` is not in SPEC 15.6 and reports what has been demonstrated about each module that changes something: an operator planning a change on a production machine is entitled to know which modules have never been run against the tool they drive, before rather than after |
| `sysctl` | implemented | 3 | |
| `sysrc` | implemented | 3 | FreeBSD; SPEC lists it as core |
| `test` | implemented | 5 | |
| `timezone` | implemented | 4 | `set_zone` writes through `timedatectl` where it runs and the zone files where it does not; the zone is named as the platform names it, and `list_zones` says which names those are |
| `user` | implemented | 3 | reads through `os/user`; writes through `pw` or `useradd`, and through `dscl` on macOS (5.43) |
| `at` | not implemented | 0 | |
| `acl` | not implemented | 0 | POSIX ACL reading needs `acl_get_file`, which is cgo on FreeBSD; needs the `getfacl` binary path instead |
| `apparmor` | implemented | 7 | reads securityfs directly rather than shelling to `aa-status`, so a node with no `apparmor-utils` can still be asked what it enforces; the mode changes need that package and name it |
| `beacons` | implemented | 10 | `list` answers from the registry and the configuration; the nine that change a running node's watchers name the phase they arrive in |
| `blockdev` | not implemented | 0 | |
| `data` | not implemented | 0 | |
| `firewall` | implemented | 8 | virtual, with a ufw provider: status, enable, disable, set_default, allow, deny, delete and reload. firewalld, nftables and pf are not built, and the provider interface is shaped by the one provider it has |
| `hostname` | implemented | 4 | get_hostname, get_fqdn, get_persistent and set_hostname; unix only, because a Windows rename does not take effect until a reboot and a state that set one would report a change on every run until somebody did |
| `http` | implemented | 1 | query, with SPEC 15.2's whole contract: mandatory certificate verification with no option to disable it, a 30 s timeout, a 10 MiB body limit, five redirects, and link-local and cloud metadata addresses refused at dial time 
| `kernelpkg` | not implemented | 0 | |
| `locale` | not implemented | 0 | |
| `logrotate` | not implemented | 0 | |
| `nfs` | not implemented | 0 | |
| `pkgrepo` | implemented | 4 | list_repos, get_repo, mod_repo, del_repo; virtual, with providers for apt, dnf/yum and Chocolatey 
| `ps` | implemented | 7 | reads through the system `ps`, and FreeBSD's own libxo JSON where there is one; `kvm` is C and `sysctl kern.proc` needs golang.org/x/sys, so neither was reachable under SPEC 4.2. `pkill` refuses a pattern matching nothing, because that is a misspelling far more often than a tidy machine |
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

16 of 46 present, plus `sysrc`, which the section does not list.

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
| `zfs` | implemented | 2 | `filesystem_present`, `absent` |
| `acl` | not implemented | 0 | see 2.1 |
| `apparmor` | implemented | 1 | `mode`, taking enforce, complain or disable; `kill` and `unconfined` are set in the profile itself and are refused by name |
| `at` | not implemented | 0 | |
| `beacon` | implemented | 2 | present and absent, both persisting to beacons.d so a declaration survives a restart 
| `environ` | implemented | 1 | `setenv`; `permanent` defaults to true here and to false in Salt, so a tree carrying this state writes a file or a registry value Salt never wrote; see migrating-from-salt.md |
| `firewall` | implemented | 4 | `enabled`, `allowed`, `denied` and `absent`; convergence is asked of ufw through its own `--dry-run` rather than computed from a status listing written for a person |
| `gem` | implemented | 2 | install and remove, comparing against the tool's own listing |
| `hostname` | implemented | 1 | `system`; the running name and the persistent one are read and reported separately, because a node where they disagree renames itself at the next boot |
| `iptables` | implemented | 7 | Linux only; `chain_present`, `chain_absent`, `append`, `insert`, `delete`, `set_policy`, `flush`. Idempotence is `iptables -C`, not a re-parse of `iptables-save`. `flush` refuses a built-in chain that is holding traffic out, and a whole-table flush, without force. Not a `firewall` provider -- it is the layer under ufw |
| `kernelpkg` | not implemented | 0 | |
| `locale` | not implemented | 0 | |
| `logrotate` | not implemented | 0 | |
| `lvm` | implemented | 6 | Linux only; `pv_present`, `pv_absent`, `vg_present`, `vg_absent`, `lv_present`, `lv_absent`. `vg_present` extends a group with named devices but never removes one, and `lv_present` grows a volume but never shrinks it — a shrink that outruns the filesystem loses data, and `lvm.lvresize` with `force` is the deliberate path for it. Reports read LVM's `--reportformat json`, not the padded table |
| `mac_defaults` | implemented | 2 | macOS only; `write` and `absent`, the exec side is `mac_defaults.*`. Convergence is decided from `defaults export`'s XML plist with the type kept distinct, so a key holding `1` is not taken for one holding `true` |
| `mount` | implemented | 2 | `mounted` and `unmounted`, each managing the running mount and the table together |
| `nftables` | implemented | 9 | Linux only; `table_present`/`absent`, `chain_present`/`absent`, `set_policy`, `append`, `insert`, `delete`, `flush`. Reads `nft -j list`; writes nft syntax through `nft -f -`. A managed rule **must carry a comment**, which is its identity -- a body change under an unchanged comment is not applied. `flush` of a table or the ruleset needs force |
| `npm` | implemented | 2 | install and remove, comparing against the tool's own listing |
| `pip` | implemented | 2 | install and remove, comparing against the tool's own listing |
| `pkgrepo` | implemented | 2 | managed and absent, both converging on a second run 
| `pro` | not implemented | 0 | Ubuntu only |
| `reboot` | not implemented | 0 | |
| `schedule` | implemented | 2 | present and absent; absent now persists, which it did not before 
| `selinux` | not implemented | 0 | Linux only |
| `ssh_known_hosts` | implemented | 2 | present and absent; a key is either declared outright or scanned and checked against a declared fingerprint, and trust on first use is refused by name rather than performed silently |
| `sudo` | not implemented | 0 | |
| `timezone` | implemented | 1 | `system`; a zone the node does not have is refused in test mode, where the tool would never run to say so |
| `win_dacl` | implemented | 4 | present, absent, inherit, owner; the exec side is win_dacl.* 
| `win_task` | implemented | 2 | present and absent; the exec side is win_task.* 
| `win_wua` | not implemented | 0 | Windows only |
| `x509` | implemented | 2 | private_key_managed and certificate_managed, both of which converge on a second run |
| `zpool` | implemented | 2 | `present` creates a pool that is not there and manages the properties of one that is; it does **not** reshape an existing pool, and reports a layout that does not match as a warning instead. `absent` exports by default and destroys only when told |

`file.accumulated`, which SPEC 15.5 requires, is not implemented.

The `x509` states are worth a note. Salt's own re-issue a certificate on
every highstate, because a re-issued certificate carries a new serial and
a new expiry and so never matches what the last run left. These read what
is on disk first and re-issue only when the certificate is missing, no
longer matches its private key, was not signed by the configured CA, or
has fallen inside the renewal window — and the comment says which. A
second run leaves the bytes alone, which the tests assert.

### 2.3 Platform modules (SPEC 15.3)

40 of 65 present — the rows below total 25 absent.

Ten of the thirty are **aliases**. SPEC names both
halves of this and both are true: 15.2 has `pkg`, `service` and `sysctl`
as virtual modules that pick a provider for the node they are on, and
15.3 names `aptpkg`, `freebsdpkg`, `systemd_service` and the rest as
modules of their own. This build implemented the first and left the
second refusing by name, which is the open question this table used to
end with. It is answered: the per-platform names should exist, and they
resolve to the virtual module.

The providers had been carrying 15.3's names all along — the apt
provider calls itself `aptpkg`, the launchd one `mac_service` — so what
was missing was only the name being callable. An alias is a module name
rather than a second set of functions, which is what keeps the counts
honest: `pkg` has eighteen functions whether or not four platforms can
each reach them under another name.

Only the names whose provider exists are aliased. `zypperpkg` and
`dnfpkg` stay pending, because SUSE has no provider here and the dnf one
covers repositories but not packages; aliasing either would turn "not
built" into "built, and fails when you call it", which is the worse of
the two answers. `sys.list_aliases` reports the table and says which of
them this node can use.

Of the nineteen that are modules in their own right, four are the
Windows ones, and they arrived because a Windows host became available:
the gap tracks the hardware, not the intent. Five are the Debian row —
`dpkg`, `debconf`, `netplan`, `apparmor` and `snap`. That row was built
when this project's fleet was assumed to be Ubuntu; it is one Ubuntu
host to four FreeBSD, which plan §7 re-ranked around on 2026-09-06.
Eight are the macOS row, which is now whole: `mac_defaults`
(`defaults(1)`, and the one member SPEC 15.5 also names as a core
state), `mac_power` (`pmset(8)`), `mac_user`, `mac_group` and
`mac_shadow`, which drive `dscl(1)` and `dseditgroup(1)` and are what
`user.present` and `group.present` branch to on a Mac — the same
"nothing to reach" gap this document records for Windows, closed here —
`mac_softwareupdate` (`softwareupdate(8)`), `mac_keychain`
(`security(1)`), and `mac_assistive`, which drives `sqlite3(1)` against
the SIP-protected `TCC.db` to manage the Accessibility grant list.

`apparmor` is the one of those that is not only a platform module: SPEC
names it in 15.2's core execution list and 15.5's core state list as
well, so building it closed three rows rather than one. It is also the
only module here that reads a kernel interface directly instead of
shelling out. `aa-status` lives in `apparmor-utils`, which a default
Ubuntu does not install, so a node can be running AppArmor with no way
to run `aa-status` on it; `/sys/kernel/security/apparmor/profiles` is
what `aa-status` itself reads and is always there. The tools that
*change* a mode really are in that package, and the module names it
rather than reporting a missing binary.

The 32 are declared as pending rather than simply missing. A name absent
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
| Common Linux | `pam`, `quota`, `openssl_cert`, `lvm`, `iptables`, `nftables`, `journald`, `mdadm`, `udev`, `modprobe`, `systemd_service` (alias) | `authselect` |
| ZFS, on every platform that has it | `zfs`, `zpool` | none |
| FreeBSD | `freebsdpkg`, `freebsd_service`, `freebsd_sysctl`, `pf` (aliases), `jail` | none |
| Debian, Ubuntu | `dpkg`, `debconf`, `netplan`, `apparmor`, `snap`, `aptpkg` and `ufw` (aliases) | `debbuild`, `apt_key`, `pro` |
| RHEL family | none | `yumpkg`, `dnfpkg`, `rpm`, `firewalld`, `subscription_manager`, `dnf_module`, `chattr` |
| SUSE | none | `zypperpkg` |
| Windows | `win_dacl`, `win_service`, `win_registry`, `win_task`, `win_pkg` (alias) | `win_file`, `win_useradd`, `win_groupadd`, `win_shadow`, `win_network`, `win_firewall`, `win_disk`, `win_system`, `win_timezone`, `win_wua`, `win_certutil`, `win_dsc`, `win_lgpo` |
| macOS | `mac_defaults`, `mac_power`, `mac_user`, `mac_group`, `mac_shadow`, `mac_softwareupdate`, `mac_keychain`, `mac_assistive`, and `mac_brew_pkg` and `mac_service` (aliases) | none |

Notes on this table:

- **`apt_key` is not pending, it is declined.** Every other entry here
  says "not yet"; this one says "not this". `apt-key` was deprecated in
  Debian 11 and removed in Debian 12 and Ubuntu 24.04, so there is no
  tool left for the module to drive, and `pkgrepo` already writes the
  `signed-by` keyrings that replaced it. It stays in the table because
  SPEC 15.3 still names it and a name the specification has and the
  build does not is exactly what the table exists to explain — but its
  reason says the module is not coming, rather than implying a date.
  Building it would have meant driving a binary the estate's own
  platform does not install.
- `zfs` and `zpool` used to be filed under "Common Linux" in SPEC 15.3,
  which was wrong: they were implemented and verified on FreeBSD, where
  ZFS is native, and the placement said a FreeBSD node could not manage
  its own pools. 15.3 now gives them a row of their own, because they
  belong to a filesystem rather than to an operating system. This is one
  of the few places the specification was amended rather than diverged
  from, and it is recorded here for that reason.
- The FreeBSD row used to read as entirely absent while being
  functionally present: the behaviour `freebsdpkg`, `freebsd_service`
  and `freebsd_sysctl` would provide is implemented inside the virtual
  `pkg`, `service` and `sysctl` modules, which is where SPEC 15.2 says
  provider selection belongs. Whether the named per-platform modules
  should *also* exist as aliases was the open question this note
  carried; it is answered, they should, and all three are aliases now.
  `pf` and `jail` are genuinely absent.
- An alias refuses on a node the provider does not match, and says which
  provider that node has. `aptpkg.install` on a RHEL node is neither a
  typo nor an unbuilt module — it is the wrong module for the machine,
  and the message names the one the machine would use.

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
platform family.

| Module | Providers specified | Implemented | Verified |
|---|---|---|---|
| `pkg` | apt, dnf, yum, zypper, apk, pacman, pkgng, brew, macpkg, winrepo, choco | pkgng, apt, dnf/yum, apk, brew, chocolatey | pkgng on FreeBSD; apt on Ubuntu, including every optional capability (holds, world upgrade, file ownership, repository listing); chocolatey on Windows 11 |
| `service` | systemd, sysvinit, upstart, openrc, launchd, freebsd_rc, smf, windows | freebsd_rc, systemd, sysvinit, launchd, windows | systemd over its real D-Bus API on systemd 255, start through mask, with the `systemctl` fallback (5.39); freebsd_rc on this host; windows against the real service control manager on Windows 11 |

The apt provider's base six functions and all four optional capabilities
were exercised against a real dpkg database on Ubuntu 24.04:
`pkg.list_holds`, `pkg.owner`, `pkg.file_list`, `pkg.list_upgrades`, and
`pkg.list_repos` all return what the equivalent `apt`/`dpkg-query`
invocation does. The dnf/yum and apk optional capabilities are written to
the same shape and remain unexercised — no such host. dnf holding is the
`versionlock` plugin, so it fails naming the plugin where it is absent
rather than silently doing nothing; apk implements neither `pkgHolder` nor
`pkgRepos`, because it has no hold in the dpkg sense and no command that
lists its repositories, and a caller gets the "the apkpkg provider
cannot …" refusal rather than an empty answer.

`pkg.version_cmp` is implemented for all three orderings: Debian and RPM
transcribed from dpkg and rpmvercmp, FreeBSD's asked of pkg(8). Doing the
first two directly rather than shelling out matters twice — it works on a
node that has neither tool, which is every node when the hub decides
whether an upgrade is needed, and it is one process rather than one per
comparison, which a `pkg.latest` over a few hundred packages notices.

The live differential against `dpkg --compare-versions` and
`rpmdev-vercmp` still needs a host that has them. See 5.2.

The D-Bus client SPEC 15.2 specifies for talking to systemd is written
(`internal/dbus`) and the systemd provider now uses it, with `systemctl`
as the fallback SPEC names. Both paths were driven against a real systemd
255. See 5.39.

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

### `pkg` — 23 of 26

Present: `install`, `remove`, `purge`, `upgrade`, `version`,
`version_cmp`, `latest_version`, `upgrade_available`, `list_pkgs`,
`list_upgrades`, `list_holds`, `list_repos`, `hold`, `unhold`,
`file_list`, `file_dict`, `owner`, `refresh_db`, `available_version`,
`info_installed`, `download`, `list_downloaded`, `autoremove`.
Absent: `mod_repo`, `del_repo` — the `pkgrepo` module already does that
job and is verified, so a second spelling on `pkg` is a decision rather
than a gap.

The optional capabilities — holding, upgrading everything at once, mapping
a package to the files it owns, listing repositories, describing an
installed package, fetching one without installing it, and clearing
unused dependencies — sit behind interfaces beside the provider one
rather than in it, because they are not universal: apk has no hold in the
dpkg sense, and pkgng's idea of a repository is a file rather than a line
in sources.list. A provider that cannot answer says so and names itself,
rather than returning an empty answer that a tree would read as "there
are none". `info_installed`, `download`, `list_downloaded` and
`autoremove` are apt-only for now (5.40); pkgng and apt implement the
older four; dnf/yum implements those four with holding routed through the
`versionlock` plugin; apk implements upgrading and file ownership but
neither holding nor repository listing. See 2.5 for what has been run
against a real system and what has not.

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

The last three rows are SPEC 27.1's tier 3, and "compiles" is the whole
of what that tier promises. Four of the nine targets in them did not
compile at all until 2026-09-06 — both OpenBSD architectures, Solaris
and illumos — which 4.10 records. All seventeen targets are in
`build-all` now, so that column is checked on every change rather than
asserted here.

| Platform | Compiles | Unit tests run | Verified against a real system |
|---|---|---|---|
| FreeBSD amd64 | yes | yes, and on every change — a QEMU virtual machine on a Linux runner, 1m44s for the suite; the race detector is left off it deliberately | yes — grains, highstate, drift reconvergence, requisites |
| Linux amd64 | yes | yes, under emulation — all but three packages, named in 4.1 | yes — a node enrolled with a hub, highstate applied, under systemd (Ubuntu; see 4.5) |
| Linux arm64 | yes | no — but macOS in CI is arm64, so the platform-neutral code now runs on that architecture somewhere | no |
| macOS | yes, since 2026-08-29, and built natively on one | yes, on every change — macos-15 (arm64) in CI, the suite and the race detector | no |
| Windows | yes | yes, natively on Windows 11 — every package, no skips | yes — grains from the registry and Win32, the file states, `cmd`, the Chocolatey provider, the job-object extension sandbox |
| OpenBSD, NetBSD amd64 and arm64 | yes, since 2026-09-06, and vetted with the tests — never before that | no | no |
| Solaris, illumos amd64 | yes, since 2026-09-06, and vetted with the tests — never before that | no | no |
| Linux riscv64, ppc64le, s390x | yes, and on every change | no | no |

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
executed** under the compat layer:

- the dnf/yum and apk providers of `pkg` — the apt provider has since
  been run against a real dpkg database, base functions and all four
  optional capabilities (2.5)
- ~~the systemd provider of `service`, and `service.masked`~~ — **done**
  (5.39): the whole systemd surface, start through mask, driven against a
  real systemd 255 over D-Bus with `systemctl` as the fallback
- the `useradd`/`groupadd`/`usermod` branch of `user` and `group`
- Linux `sysctl` handling, which differs from FreeBSD's

The dnf/yum and apk items need a real host of that family. Nothing short
of one will exercise them.

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
worth writing down. Almost nothing has been *run* there: `pkg`,
`service`, and most of `mac_*` are as unexercised as they were. The
exceptions are `mac_defaults`, whose `live_mac_defaults_test.go` drives
the real `defaults` against a throwaway domain, and `mac_power`, whose
`live_mac_power_test.go` reads the real `pmset` — both on a developer's
Mac, and no CI leg is one. `mac_defaults`'s live leg needs
`HALITE_SYSTEM_LIVE=1` because it writes; `mac_power`'s reads only and
runs on any `go test` on a Mac.

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

Since then, on 2026-09-03, the apt provider's optional capabilities were
run directly on an Ubuntu 24.04 workstation: `pkg.list_holds`,
`pkg.owner`, `pkg.file_list`, `pkg.list_upgrades`, and `pkg.list_repos`
each return what the matching `apt-mark`/`dpkg-query`/`apt-get` call does.
`pkg.hold` reaches `apt-mark hold` and fails only on the privilege check,
as it should for an unprivileged run.

That host also surfaced a defect the compat layer could not: a `Command`
with a `Timeout` that fired waited the runaway out instead of killing it.
`os/exec` signals only the direct child, and Linux's `/bin/sh` (dash)
forks for `sh -c`, so the shell died while the program it spawned kept
running and kept the stdout pipe open, blocking `Wait` until the program
finished on its own. FreeBSD's `/bin/sh` execs in that case, so the
development host never saw it. `OSRunner.Run` now starts every child in
its own process group and kills the group on a timeout, with a bounded
`WaitDelay` as the backstop; the error names the timeout rather than
reporting only `signal: killed`. `internal/exec` covers both the simple
and the forked-grandchild case.

What it does not establish, and 4.2 still stands for the rest:

- One distribution. Ubuntu chooses `apt`; the `dnf`, `zypper`, and `apk`
  providers remain unexercised, and `os_family` branching in a tree is
  the commonest thing to get wrong across them. The dnf/yum and apk
  optional capabilities added alongside apt's are written but unrun.
- One architecture. Linux arm64 still compiles and nothing more.
- The node only. The hub and the API have not been run on Linux, so
  their units, their sandboxes, and `StateDirectory=halite-api` are
  still only read rather than run.
- One run. Nothing here says what a restart, a revocation, a certificate
  renewal, or a week of scheduled highstates does on that host.
- Not FIPS. The `-fips` artifacts ship for Linux and have been run
  nowhere.

### 4.6 What running on Windows established

The suite had never been run on Windows. On the first native run it
failed **80 tests across 12 of 55 packages**; it now passes all 55 with
no skips. Almost none of that was test noise. What follows is what a
platform that had only ever been cross-compiled for was hiding, because
each item is a class of defect rather than a one-off.

**Checks written in unix terms that answered the wrong question.**

- A POSIX mode does not exist on Windows: `os.Stat` synthesises 0666 for
  anything writable. So `ReadSecretFile` refused every secret file on
  the platform and told the operator to run a chmod that changes
  nothing — a hub or node configured with a signing secret could not
  start. The return log, `cmd.script`'s temporary script, the enrollment
  key and everything else written at 0600 were readable by every account
  on the machine. Three tests asserted the mode and passed by agreeing
  with the wrong answer; a fourth made a directory unwritable with a
  chmod that returned nil and changed nothing, so it asserted a refusal
  that never came. `internal/winsec` and `internal/fileperm` carry the
  intent out with an access control list.
- `file.managed` compared the synthesised 0666 against the mode it was
  asked for, found them different, ran a chmod that changed nothing, and
  found them different again next run. **No file state on Windows ever
  converged**: every run reported a change, `state.apply` never returned
  the exit code for a converged run, and a highstate could not tell
  drift from noise.
- gitfs judged a remote local if it started with a slash, so a hub
  configured with `C:\srv\states` was told its own disk "is not an
  encrypted transport" — and `C:\Users\some.name@corp\states` satisfied
  the scp-style test and would have been handed to git as an ssh remote.
- `grains.d` decided what to run by the execute bit, so every provider
  script was parsed as YAML and the operator got a YAML error about a
  shebang line.

**Things that were absent rather than wrong.**

- `/bin/sh` was the shell everywhere, so `shell: true` and every
  `cmd.shell` failed. `cmd.script` wrote an extensionless temporary file
  that CreateProcess will not run. The clean environment carried four
  variables, and a Windows process without `SystemRoot` fails before
  `main`.
- The grains were a stub: `osrelease` empty, memory zero, every hardware
  field an empty string.
- There was no package provider, so `pkg.installed` answered with a list
  of six providers, none of them for this platform.
- The extension sandbox was the process boundary and a note saying so.

**Defects on every platform that only Windows made visible.**

- Six packages had their own copy of write-a-temp-file-and-rename. All
  six are correct on unix, where rename(2) does not care who holds the
  destination open, and all six raced on Windows, where MoveFileEx must
  open the destination for delete with no sharing. Measured here: 12 of
  200 replaces and 48 of 200 reads lost when one goroutine did each. The
  hub could not record a delivery while the API served the same job.
- `eventbus.Close` set the current segment to nil and the next append
  reopened it, so a closed bus went on accepting events and held the
  segment for the life of the process. Shutdown ordering was
  unenforceable. Unix hides it: an open file can be unlinked.
- A grain provider that timed out was killed and then waited on through
  `cmd.Output()`, which does not return until the output pipe closes —
  and a killed script's children hold it open. Measured at 61 seconds
  against a 300ms timeout, on any platform.
- A test that scripted `npm ls` output still asked the host whether npm
  existed, so it passed or failed on what the developer happened to have
  installed. `exec.Context` grew a `Lookup` seam beside `Runner`.

**Four of SPEC 15.3's eighteen Windows modules now ship.** `win_dacl`
reads and writes an access control list, so `file`'s `user:` sets an
owner instead of being refused, and four states declare permissions,
ownership and inheritance. `win_service` speaks to the service control
manager through its API rather than by parsing `sc.exe`, and it is also
the `windows` provider SPEC 15.2 names for the virtual `service`
module — which had none, so a node on this platform answered every
service call with "no init system was recognised on this node".
`win_registry` reads and writes values in either of the two registries a
64-bit Windows keeps. `win_task` manages scheduled tasks, with states for
them.

The two later ones reached their subsystem differently, and the
difference is the rule rather than a preference. The service control
manager has no machine-readable output mode: `sc.exe` writes a table and
its status words are localised, so parsing it means parsing prose — and
that is what made the API worth the vtable work. The task scheduler does
have one: `schtasks /query /xml` emits a published schema whose element
names are fixed in every locale, and `/create /xml` takes it back, which
is exactly SPEC 15.2's standard for reaching a subsystem through its
binary. So `win_service` speaks the API and `win_task` runs the binary,
and each is the shorter road to the same guarantee.

**What is still not built.** The other fourteen modules of SPEC 15.3's
Windows row: `win_pkg`, `win_file`, `win_useradd`, `win_groupadd`,
`win_shadow`, `win_network`, `win_firewall`, `win_disk`, `win_system`,
`win_timezone`, `win_wua`, `win_certutil`, and the two SPEC marks
bridged. There is no user or group provider, so `user.present` has
nothing to reach here. `runas` is refused, because starting a process as
another account needs that account's credentials. The restricted token
of SPEC 24.3 is absent, so an extension is bounded but not
de-privileged.

Three smaller ones worth naming rather than discovering. `service.reload`
is refused, because the manager's reload control is one almost nothing
implements and silently restarting instead would be a bigger change than
the state asked for. `group:` on a file state is refused, because a
Windows file has an owner and an access control list and no group, so
mapping it onto the primary group — which nothing on the platform
reads — would let a state pass while granting nobody anything. And
`win_registry` ships no state module, because SPEC 15.5 does not name
one: Salt has `reg.present` and an estate migrating from it will want
the same, so it is a decision for a person rather than a quiet addition.



### 4.7 What a node with ZFS established

ZFS is a kernel module and has no userspace stand-in, so none of `zpool`
could be verified where the rest of this project is. A container shares
the host's kernel, and the kernels a developer machine runs — Docker
Desktop's WSL2 kernel here, a VM kernel on macOS — carry no `zfs.ko`.
The `zfs` and `zpool` reading functions had been verified on FreeBSD, on
hardware; the writing half had nowhere to run.

So it is run in a virtual machine with its own kernel, booted under KVM
from an Ubuntu cloud image, against pools built on files. That is `make
zfscheck`, and the point of making it a target rather than an afternoon
is that the next person to touch this can repeat it in ninety seconds.

**It found two defects on its first run**, both in reading `zpool list`,
and neither of them reachable from a fixture written from memory — which
is what the fixtures were, until this replaced them with bytes captured
from the tool:

- Scripted mode indents **every** row under the pool by exactly one tab,
  whatever its depth. `-H` removes the header and the column padding and
  leaves the indentation, but it does not vary it, so the tree `zpool
  list -v` draws for a person is flat for a program. A reader that took
  the indentation for depth found every pool empty — and `zpool.present`
  then warned that a pool it had itself just created was "nothing".
- The `logs`, `cache` and `spare` section headers are printed
  **unindented and space-padded**, ignoring `-H` in the middle of an
  otherwise tab-separated listing. A reader that took every unindented
  row for the pool's own row swallowed the header and filed the pool's
  log device as a third leg of the mirror above it.

Both are now read the way the tool writes them: the layout is grouped by
name rather than by depth, and the section headers open a vdev of their
own. The fixtures in `zpool_test.go` are the real bytes.

**One shape still cannot be recovered**, and it is stated rather than
worked around: a bare device added as a stripe column beside a mirror is
indistinguishable from another leaf of that mirror, because the depth
that would separate them is what scripted mode drops. It reads as a
wider mirror. This is survivable only because the layout is never used
to act — see below — and it is one of the reasons it is not.

**What the run establishes.** A pool is created from a declared layout
and the run after it reports no change; `ashift` set at creation reads
back as the value it was given, so a property that can only be set once
still converges; a pool exported by `zpool.absent` and then declared
present again is *imported* rather than created over, with its data
intact — checked by digest, because creating a pool over the devices of
one that already holds data is the failure this ordering exists to
prevent; a property change is applied and then is not applied again; and
`zpool.absent` exports by default and destroys only when told, because
the two differ by whether the data survives.

**What it does not establish.** The vdevs are files, not disks: nothing
here exercises `ashift` against a drive that misreports its sector size,
device naming under `/dev/disk/by-id`, or a pool whose devices move
between controllers. Nothing exercises a degraded pool, a resilver, or a
scrub that finds something — `zpool.healthy` is tested against pools
that are healthy. And it runs on Linux with OpenZFS 2.2.2; FreeBSD,
where this project's ZFS reading was originally verified, is not covered
by the harness.

**`zpool.present` does not reshape a pool that exists**, and this is the
design rather than a gap. It creates a pool that is not there and
manages the properties of one that is. Where the declared layout differs
from the actual one it reports the difference as a warning and stops. A
top-level vdev cannot be removed from most pools at all; `zpool add`
aimed at what was meant to be a mirror turns it into a stripe with no
undo; and the failure mode of getting either wrong is the permanent loss
of everything on the pool. A warning an operator has to close is worth
more than a state that closes it for them.

### 4.8 What running the race detector more often established

`make check` runs `race`, and `race` sets `CGO_ENABLED=1` on purpose:
the detector is unavailable with cgo off, and a target that quietly ran
without it would report nothing it had not first made true.

**The detector is not new here.** `make check` and `make race` have been
run on FreeBSD, the development platform, and on Ubuntu Linux, and the
Go toolchain supports the detector on both. What had never happened is
`make check` completing on **Windows**: the cost of `CGO_ENABLED=1` is a
C toolchain, Windows ships none, and so the race leg could not run there
at all. `make racecheck` is for that case and that case only — the
`race` recipe verbatim, in a container that has a compiler, as an
unprivileged account. A host with a compiler runs `make race` and gets
the same answer faster.

So the three findings below are not what a first run turned up. Two of
them were reachable on FreeBSD and on Linux the whole time and had
simply never been hit; the third needed an account no developer uses.
That is the more useful lesson, and the sharper one: **a gate that runs
sometimes is not the same as a gate that runs.** The data race appeared
in one full sweep and not in the two that followed it, and the flaky
test loses about one run in eight — neither is found by running the
detector once on a new platform, and both are found by running it on
every change.

**A data race on the hub's clock.** `Server.Now` is a func field the
tests set, and `now()` reads it from background goroutines —
`deliverQueued`, reached from a subscribe handler. Two tests assigned it
on a hub that was already serving. Assigning a func field is not atomic,
so that is a write racing a read; it is test-origin, since `Now` is nil
in production and never written after startup, but the detector was
right about the access. The lab now installs the clock before `Serve`
starts and moves it through an `atomic.Int64`.

**A test that asserted an ordering the hub does not promise.** A return
is recorded in the job cache and *then* its `ret/<node>` event is
emitted. A test that waited for the returns and read the bus in the next
statement was reading it inside the window between those two writes. The
window is narrow enough on Windows that it had never been lost and wide
enough on Linux to lose about one run in eight under the detector —
green where it was written, flaky where CI would run it. The events are
late rather than absent, so the test waits for them now.

**Two permission helpers that cannot work as root.** `permtest.DenyWrite`
chmods a directory to 0500 and the caller asserts a refusal; root holds
CAP_DAC_OVERRIDE and writes anyway, so the condition never existed and
the assertion failed naming the code under test. `DenyRead` had the same
latent problem. Both skip now, saying why. It is the failure the
permtest package doc already describes for `os.Chmod` on Windows,
arriving from the other direction.

The general shape is not 4.6's. 4.6 and 4.7 are about a platform nobody
had run, and the defects there were unreachable until somebody did.
These were reachable all along on platforms that do run the suite, and
what found them was repetition and a different account rather than a new
system. The first two are the argument for running the detector on every
change rather than when somebody remembers; the third is the argument
for running it as the account a hub actually runs as.

### 4.9 What CI established on its first afternoon

The gates were stood up on 2026-09-05 and found four things before the
branch that added them was merged. Two were environment, and are
recorded because they had been silently true for the life of the
project; two were defects.

**Line endings were whatever each checkout said.** There was no
`.gitattributes`. Every machine here uses LF and GitHub's Windows
runners default to CRLF, and this project's tests read this project's
own files — so `buildpolicy` reported that `go.mod` had no `toolchain`
directive, `docsaudit` reported the generated pages as out of date with
code that had not changed, and `specaudit` made two accusations against
this document that were not true. Sharper than any of those: a shell
script under CRLF is `#!/bin/sh\r`, which no kernel will exec, so a
Windows clone with stock settings produced a `make racecheck` that could
not start.

**The runner is an administrator**, so `permtest.DenyRead`'s DENY entry
did not deny and a test reported the code under test for a condition the
environment never created. It is 4.8's root problem on the other
platform. The unix side skips because a container's account cannot be
changed from inside a test; CI runs the Windows suite as a standard
local account instead, which keeps the coverage and is the account a hub
runs as anyway.

**A spool cannot trust a nanosecond timestamp to be unique.** The
webhook returner named each spooled file by its timestamp alone; on a
clock with half-millisecond granularity three returns shared one, the
sort fell through to the content digest, and the backlog went upstream
in an arbitrary order against the oldest-first guarantee 6.1b
advertises. The **relay** spool had the same root cause and a worse
consequence — its name is the timestamp and the drop count, so two
returns inside one tick are the same file and the second silently
overwrote the first. Both carry a monotonic sequence now. The second was
found by inspection rather than by a test, and it is the more serious of
the two: silent loss in the mechanism whose whole purpose is that an
outage delays returns rather than losing them.

### 4.10 What compiling for tier 3 established

SPEC 27.1 puts OpenBSD, NetBSD, Solaris and illumos, and Linux on
riscv64, ppc64le and s390x in tier 3, whose whole promise is "compiles
and is published". Nothing had ever compiled for any of them. The
Makefile's `TARGETS` had eight entries — the tier 1 and tier 2
platforms — so `build-all` checked the claim it was not making and
skipped the one it was.

Compiled on 2026-09-06 for the first time: **four of the nine tier 3
targets did not build at all**, and had not for as long as the extension
sandbox has existed. Every failure was in `internal/bridge`'s resource
limits, and each was a real difference between the platforms rather than
a typo:

- **OpenBSD has no `RLIMIT_AS`.** `rlimit_bsd.go` claimed darwin,
  freebsd, netbsd, openbsd and dragonfly in one build tag on the
  strength of the BSDs spelling `RLIMIT_NPROC` alike. OpenBSD bounds
  memory with `RLIMIT_DATA`, which anonymous `mmap` counts against there
  and does not on Linux or the other BSDs — a real limit, but not the
  same limit, so the number an operator would choose differs.
  `Describe` now says "data segment" there rather than "address space",
  because saying the wrong one is how somebody sets a bound that does
  not do what they read.
- **Solaris and illumos have no `RLIMIT_NPROC` at all.** They bound
  process counts with resource controls — `project.max-lwps` and the
  zone's equivalent — which an operator sets on the project or the zone
  and a process cannot set on itself. There is nothing `Confine` can do,
  so it does nothing and `sys.list_extensions` says the limit is not
  enforced here. AIX is grouped with them: Go's `syscall` carries no
  `RLIMIT_NPROC` for it either, and declaring a limit nobody has watched
  take effect would be worse than declaring none. AIX is in no tier and
  was fixed anyway, because leaving one platform uncompilable is how
  this was arrived at.

This is the same shape as 4.4a, where macOS was grouped with the BSDs
for the width of `syscall.Rlimit` and the tree did not compile there at
all. A build tag is a claim about which platforms are alike, and the
platform nobody compiles for is the platform the claim is wrong about.

The fix that matters is not the four files. It is that
`limitsAvailable` now reads the same two declarations `Confine` applies,
so a limit reported as enforced and then skipped — or skipped and then
reported — is no longer expressible. Before, the description said all
four limits were enforced on every unix, which was a sentence and not a
consequence of anything.

`TARGETS` now carries all seventeen platforms of SPEC 27.1, so
`build-all` compiles and vets each one, and `cross` publishes it —
which is what tier 3 says. `internal/buildpolicy` reads the tier table
out of SPEC 27.1 and the target list out of the Makefile and fails if
they disagree in either direction: a platform the specification promises
and the build does not compile, or a target the build carries and no
tier covers. The platform cell of each tier row is matched in full, so
any edit to that table fails the test and puts the change in front of
somebody who has to decide what it means for the build. A row that
quietly grew a platform nothing compiles for is how this happened.

What this does **not** establish is that halite works on any of these.
Compiling is what tier 3 promises and compiling is what is now checked.
Nothing has run on OpenBSD, NetBSD, Solaris, illumos, or on riscv64,
ppc64le or s390x — the `RLIMIT_DATA` mapping in particular is read from
OpenBSD's documented behaviour and has not been watched take effect.

### 4.11 A queued job could be delivered twice

Found on 2026-09-06 by CI, as an intermittent failure of
`TestAQueuedJobWaitsForTheNodeToReturn` that had been showing up perhaps
once in twenty runs and then failed both `test` legs of one run. It is a
defect in the hub, not in the test.

**The job record is read, changed and written back by several
goroutines, and the write replaces the whole file.** When a node
reconnects, one goroutine gives it the jobs it missed and then clears
that node from each job's spool. The return the node sends back arrives
on another goroutine, which marks the job complete. Both read the
record; both write it. When the second read happened before the first
write, the second write puts back everything the first had changed — and
what it puts back is the spool entry.

The consequence is not a wrong number in a status page. A spool entry
that comes back from the dead means **the node is sent the job again the
next time it connects**: a second run of an instruction an operator
issued once. For the `test.ping` in the test that is nothing; for the
`cmd.run` or `pkg.installed` that a queued job usually is, it is the
thing SPEC 9.5's spool exists to make reliable, doing the opposite.

`job.Cache.Update(id, mutate)` reads, changes and writes under a per-job
lock, and the mutator is given the record as it is on disk rather than a
copy taken earlier. Every site in the hub that changes a record which
has already been delivered goes through it: the queued delivery, the
completion, the batch's delivery accounting, the state transitions,
`Resume` and `kill`. The four `Put` calls that remain are creates — the
job's first write and the two that follow it in `Dispatch`, all before
anything has been sent to any node, and the runner's own record.

Per job rather than one lock for the store, so a busy hub's bookkeeping
does not queue behind whichever record is slowest to write. It does not
make the cache safe for two hub *processes* sharing a directory; one hub
owns its job cache, and nothing in SPEC asks for more than that.

**This is the third defect of this shape in a fortnight**, and the
first two are in 4.9: the webhook returner's spool naming files by a
timestamp that is not unique, and the relay spool doing the same and
silently overwriting. All three are a durability mechanism that is
correct when read one operation at a time and wrong when two arrive
together. The common cause is that none of them was written with a
second writer in mind, and none of the tests had two.

The test that now demonstrates it is deterministic rather than lucky.
`TestGetThenPutLosesOneOfTwoChanges` forces the interleaving and asserts
that the change *is* lost, so it stands as the reason `Update` exists
rather than as a description of it; `TestUpdateKeepsBothChanges` forces
the same interleaving through `Update`. Removing the lock makes
`TestEveryWritersChangeLands` report **1 of 40** writes surviving, which
is the size of the hazard when more than two goroutines are involved —
a batched job's delivery accounting, for instance.

What found it was CI running the suite on four platforms on every
change. It had been in the tree since the queue policy was built and no
local run had ever failed on it.
### 4.12 A reader that fell behind the event bus was not told

Found on 2026-09-06 by the chaos suite, on its first run, which is what
the suite was built for.

SPEC 31's chaos row names "event bus at retention limit". Writing the
scenario down meant deciding what a reader should experience when the
history it was pointing at has been pruned, and the answer written first
was the one that seems obvious: it should be told its offset is gone.
The test then measured what the build does. **It skips forward and says
nothing.** `Read` resolves the offset, finds no surviving segment with
that sequence, and continues from the oldest one that does exist. The
scenario's own run reported **380 events silently skipped**.

That is not a wrong number in a status page either. The reader this
matters to is the reactor: it holds an offset, and a hub whose bus
turned over while the reactor was behind resumes it at a later point
with no error, no event and no log line. The reactions it never ran
leave no trace anywhere at all — the failure is indistinguishable from a
quiet estate.

**This is an unimplemented requirement, not an open question.** The
first version of this entry called it a decision somebody had to make.
That was wrong, and SPEC 17.2 says so in as many words:

> Subscribers register a set of tag globs and a starting position, which
> may be `latest`, `earliest`, or a specific offset. **A subscriber that
> falls behind is disconnected with an explicit `subscriber_lag` error**
> rather than causing the bus to buffer without bound.
>
> Replay from an offset is supported, which makes a reactor restart
> lossless and makes incident reconstruction possible. Salt's event bus
> is lossy by construction, and every mature Salt estate has learned
> this during an incident.

So the behaviour is named, the error is named, and the paragraph's whole
argument is that Salt loses events silently and this must not. Silently
advancing a stale reader is the Salt behaviour with a different
mechanism.

**Checked rather than recalled**, against Salt's own source in the
differential container, on 3007.1 and again on 3008.2: `salt/utils/event.py`
has no `offset`, `replay`, `resume` or `backlog` — not as an
implementation, not as a word. A Salt subscriber holds no position, so
it cannot be stale. The comparable condition is a subscriber that cannot
keep up, and ZeroMQ handles it by dropping at a high-water mark of 1000
(`pub_hwm`, set on `SNDHWM` and `RCVHWM` in `salt/transport/zeromq.py`)
without telling the subscriber. Unchanged between the two versions.

**How it survived.** `subscriber_lag` exists in this build — as a
*metric*. `halite_event_subscriber_lag_seconds` is registered, documented
in metrics.md, and carries a p95 alert in the shipped Grafana dashboard.
The observability half was built and the behaviour half was not, and a
row with a metric against it reads as done. Nothing caught the
difference because `internal/specaudit` holds module tables and counts to
SPEC and not prose requirements like this one — the same shape as the
tier 3 targets in 4.10, where the specification made a claim no test
made.

What the chaos scenario does now is assert the current behaviour and
name it as the gap it is. The assertion is written so that fixing it
fails this test with a message saying which two documents to rewrite —
which is the cheapest way to make sure the fix and the record move
together.

The distinction the build already drew is worth stating: a **malformed**
offset is refused. So the silence was about a well-formed offset whose
data had gone, not about parsing, and `TestABadOffsetIsRefusedRatherThan
SilentlyStartingOver` has covered the other half since before this.

**Fixed the same day.** `eventbus.Bus.Lag` reports whether an offset has
been pruned without reading anything, and `Read` calls it first, so the
silent advance is no longer reachable. The error is `ErrSubscriberLag`,
carrying the offset asked for, how many whole segments went, and the
oldest offset the bus still holds — because "your position is gone"
without somewhere to resume cannot be acted on. It is deliberately not
`ErrBadOffset`: the two say different things about where the reader was,
and the callers resume from different places because of it.

- **An operator streaming events** gets a **410** with
  `transport.CodeSubscriberLag`, and gets it *before* the success
  header. That is the half that matters. After a 200 there is nowhere
  left to put an error, and a reader handed fewer events than it asked
  for cannot tell that from a quiet hub — which is exactly how the
  original defect stayed invisible. 410 rather than 400 because the
  offset was well formed and this bus issued it; what is gone is the
  data behind it.
- **The reactor** resumes at the oldest surviving event rather than at
  the end. It knows exactly where it was, so the oldest event still held
  is the nearest surviving point to it and loses the least; nothing
  before it can be re-run, because everything it processed is older than
  the offset it was holding. A reactor with an *unreadable* offset still
  jumps to the end, and that asymmetry is the point: a corrupt file says
  nothing about where the reactor was, and replaying the whole retention
  window on the strength of one would fire every reaction in it.
- **The loss is recorded three ways**, on the same argument the reactor
  queue's overflow is: a warning naming the offset and the distance, a
  `halite/reactor/lag` event, and
  `halite_events_dropped_total{reason="subscriber_lag"}`. A reaction
  that did not happen leaves no other trace, and this is the only moment
  anything knows it did not.

`latest` and `earliest` cannot lag — they are resolved against what
exists now — so an operator who does not care where they were is never
refused.

**What is still true:** the loss is recorded rather than recovered. No
bus with a retention window can do better, and the count in the error is
segments rather than events, because the bus keeps no tally of what was
in a segment it deleted.

### 4.13 An older hub silently truncated a newer hub's job records

SPEC 31's Upgrade row — "Hub at version N with nodes at N-1 and N+1;
state and job cache format migration; certificate rotation across an
upgrade" — had nothing behind it. Asking what it was asking about, on
2026-09-06, produced one defect and one correction to an assumption
everybody arriving from Salt brings with them.

**The defect.** A job record round-trips through `job.Job`, and
`encoding/json` drops every field the struct does not have. The record
carried no version marker of any kind, so a hub could not tell a record
a *newer* hub had written from one of its own — it read it, changed one
field, and wrote it back without the rest. Measured on a record with two
extra fields: **eleven keys in, nine out**, silently, with nothing
anywhere recording that it had happened.

That is the rollback case, and it is not exotic on a fleet built from
source: going back a tag is one `git checkout` and one `make install`.
Every job record the newer hub had touched would be quietly truncated by
the older one, and rolling forward again would not bring the fields
back.

`job.Job` now carries `Schema`, and `JobSchema` is `halite.job/1`. An
empty schema is a record written before the field existed — this build's
own shape — and is accepted and stamped on the next write. A schema this
build does not know is **readable and not writable**: `jobs list` on a
rolled-back hub keeps working and an operator can still look at the job,
and both `Put` and `Update` refuse with `ErrForeignRecord`, naming the
record, the schema found, the schema this build writes, and what to do.
`Update` refuses *before* the mutator runs, so a caller with a side
effect in it does not have the side effect and then hear the write
failed.

Refusing rather than truncating is this project's answer everywhere else
it has faced the same choice — the pruned event-bus offset (4.12), the
snap version snapd will not hold (5.28), the pf anchor nothing
references (5.31). Truncating and reporting success is the one option
that leaves nobody able to find out.

**The correction.** Salt requires its server upgraded before its agents,
because the agent speaks a protocol the server defines. Halite does not
share that constraint, and this is the first time anybody checked rather
than assumed:

- A newer hub talking to an older node: an unknown message type reaches
  the node's `default:` branch, which logs it and carries on. The stream
  stays open.
- A newer node talking to an older hub: nothing in this tree calls
  `DisallowUnknownFields`, so a request carrying fields the hub has
  never heard of is accepted and the fields ignored.

So **neither upgrade order is forced**. What is worth saying plainly is
that the tolerance was *accidental*: it falls out of `encoding/json`'s
defaults and one `default:` branch, and nothing recorded it as a
guarantee or would have noticed it being taken away. It is a guarantee
now, with a test on each direction.

The one place skew is fatal is the **ALPN**. SPEC 6.4 makes `halite/1`
mandatory and rejects a peer that does not offer it, so moving it breaks
both directions at once — that, and not the message shapes, is where an
upgrade order would come from. It is frozen at 1, and a test says what
has to be rewritten together if it ever moves.

**The return schema was frozen and unenforced.** SPEC 9.4 freezes
`halite.ret/1` "so a dashboard built on it keeps working"; the hub
filled in a missing schema and validated nothing, so a return marked
`halite.ret/2` was recorded as though it were v1. It is now accepted,
stored as it arrived, warned about, and counted in
`halite_returns_foreign_schema_total`.

Accepted rather than refused, and the asymmetry with the job record is
deliberate. A record is bookkeeping the hub owns and can decline to
touch. A return is the only evidence that work already happened on a
node: refusing it loses that evidence, the node has nowhere to put it
again, and the job looks unanswered for ever. It is the same argument
`doctor`'s disk-full check makes — a write that fails after the
instruction has gone out must not become a second untruth.

**What is not established.** All of it is a lab. No two halite versions
have ever actually run against each other, because there has never been
a second version; every "older node" here is this build sending what an
older one would send. What the tests pin is the tolerance and the
refusal, not an upgrade anybody has performed. `internal/specaudit`'s
`TestEveryUpgradeClauseHasATest` holds SPEC 31's Upgrade row to the
tests that claim its clauses, in both directions, so a fourth clause
added to the row cannot sit there uncovered.

The fleet this was found for is five hosts and has had no upgrade
trouble — which its owner points out is too small to be evidence either
way.

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
stands. Seven are present, one of them stronger than specified, five are
partial, and one is absent. Nothing is unverified any more: the
reproducibility layer was, and it is now partial with the limit stated
rather than the question left open.

Four rows moved after this table was first written and each said
"blocked on phase 2" long after phase 2 landed: chaos and upgrade are
built, scale is partial, and integration is the one that is still
absent.

| Layer | Status |
|---|---|
| Conformance, YAML | **present.** All 402 cases of the suite's `data` branch run on every `go test`, vendored under `internal/yaml/testdata/yaml-test-suite/`. Each case is checked three ways: a document the suite calls invalid must be refused, one it calls valid must parse, and where the suite supplies `in.json` the parsed tree must match. Every disagreement has a row in a table giving its reason, enforced in both directions so a stale row fails as loudly as an unrecorded one. Standing: 330 of 402 agree, 36 disagree by design, 36 are gaps — see 5.4. The dialect SPEC 10.1 actually specifies is PyYAML's rather than the standard's, and that half is checked against PyYAML itself — see 5.8. |
| Conformance, templates | **present.** Two corpora under `internal/template/testdata/jinja-corpus/`, run on every `go test`. 198 cases are extracted mechanically from Jinja's own pytest suite, carrying each case's environment options; disagreements have a row apiece with a reason, enforced in both directions. 123 more are written here for what Jinja's tests cannot cover: Salt's added filters, the strict undefined of 10.2.6, the limits of 10.2.8, and the refusals the subset owes an operator — those carry no deviation table, because a case that fails there is one this project got wrong. Standing: 157 of 198 agree, 26 are outside the subset, 15 are gaps — see 5.5. |
| Differential against Salt | **partial.** `internal/saltdiff` compiles ten trees with both implementations and compares the low state: the chunk sequence first, then each chunk's arguments. It runs against Salt 3006.25 and 3008.2. The trees cover file and cmd states, a five-link requisite chain including a reversed requisite, Jinja loops and conditionals over pillar, include with extend, `names` expansion, explicit ordering, macros and filters, grain conditionals, and argument types end to end. Two deviations are recorded, each naming the Salt major it was observed under, because the majors disagree with each other about what `show_lowstate` projects. Standing: every tree agrees. It makes all three comparisons SPEC 31 asks for, with the third — the state results — compared as test-mode *predictions* rather than as the results of an apply, which still needs somewhere to apply a tree. See 5.7. |
| Differential, version comparison | **partial.** `pkg.version_cmp` exists, with the Debian and RPM orderings implemented directly and FreeBSD's asked of pkg(8), since libpkg is its own specification. The FreeBSD half of the differential is real and runs here: 14 pairs go to `pkg version -t` and to halite and must agree, and the test skips loudly rather than passing quietly where pkg(8) is absent. The Debian and RPM halves need a Debian or RHEL host for `dpkg --compare-versions` and `rpmdev-vercmp`; until then they are tested against those projects' own published vectors, which are the cases the algorithms are known to get wrong. |
| Conformance, state modules | **present** and stronger than specified — see 1.4. Covers 6 of the 46 state functions. |
| Property | **present** for all five named properties, each checked over generated input rather than a fixed corpus: path containment never escapes a root (`internal/fileserver/property_test.go`, 23000 generated paths plus the symlink cases), the topological sort is stable, requisite resolution terminates, and a requisite genuinely orders its target (`internal/state/property_test.go`, over random requisite graphs including cycles), the YAML parser never panics (`internal/yaml/property_test.go`, 50000 generated documents), and targeting is monotonic under grain addition (`internal/target/property_test.go`, 20000 expression and node pairs). Negation is asserted as the documented exception to monotonicity rather than left implicit. |
| Fuzz | **present** for three of the eight named targets: the YAML parser and its encoder, the template lexer and parser, and the compound target parser. `make fuzz` runs all seven functions; `make fuzz FUZZTIME=30m` is a campaign. The first run found four defects, listed in 5.3 below. Still absent: the wire message decoder, the cron parser, the roster parser, and the bridge protocol decoder, all of which belong to phases that have not started. |
| Integration | **absent.** No containerised hub-plus-nodes harness across the tier 1 matrix. The repository has two containers and a virtual machine — the Salt differential image, the Debian container of 5.35, and the ZFS machine of 6.1 — and each is a correctness harness for one subsystem rather than a matrix. This is the last layer with nothing behind it. |
| Scale | **partial.** `internal/perf` carries all thirteen rows of SPEC 30 with what measures each, held to SPEC's own table in both directions. The two rows SPEC measures with a benchmark are measured: the highstate compile of 500 states over 50 SLS files, and the cold pillar compile of 200 pillar SLS. `make perf` runs them and fails if either is over its target; `make perf-bench` is the raw measurement. The eleven rows that need the simulated node harness, a soak, or the integration matrix are still unmeasured, and so is the *cached* half of the pillar row, because no pillar cache exists to measure. See 5.57. |
| Upgrade | **present.** All three clauses of SPEC 31's row have tests and `internal/specaudit` holds the row to them in both directions, so a fourth clause cannot sit there uncovered. Writing them found a job record that silently dropped every field an older build did not know; the record now carries a schema marker. What is not established is two halite versions actually running against each other, because there has never been a second version. See 4.13. |
| Chaos | **present.** All eight of SPEC's scenarios are registered in `internal/chaos` with a defined behaviour and a stated limit, each with a test that names it, and a ninth is registered that SPEC does not name: the concurrent-writer shape that three defects in a fortnight all had. `make chaos` runs the layer with `-v`, which is the point of it. Its first run found a reader resuming from a pruned event-bus offset being silently skipped forward. See 5.29. |
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
collection on its key's line took it to 331 and 37. Measuring chomping
against PyYAML took it to **330 and 36** — the gap count down by one and
the agreement count down by one as well, which 5.56 explains: the
parser got more correct and the suite score got worse, because on two
cases the suite and both reference implementations disagree and SPEC
10.1 picks the implementations. Statement coverage of `internal/yaml`
rose to 96.1% along the way, but the suite is the thing actually
measuring correctness here.

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
| `gapDirective`, `gapMappingKey` | 2 | singletons. |

There is no cluster left to take. From here it is one case at a time, and
the value per fix is lower than anything else on the list in section 8.

Of the 36 deliberate disagreements, 20 are tags outside the nine types, 7
are tabs used for indentation, 6 are complex keys, 1 is a duplicate key,
and 2 are the end-of-input cases of 5.56. The first four groups are SPEC
10.1.2 working as specified and the last is SPEC 10.1's choice of
dialect; all of them are excluded from the conformance figure, since
halite does not claim to be YAML 1.2 there.

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

`make saltdiff` runs it in a container carrying Salt's own onedir
bundle, pinned. `make check` still runs it against whichever `salt-call`
is on PATH and skips where there is none — which is every machine this
project is developed on, so until the container the primary correctness
gate was green by not running.

It has been run against Salt 3006.25, 3007.1 and 3008.2. Running it
against 3007 for the first time found three things: `pillar.items`
refuses the `unmask=True` that 3008 needs and 3006 ignores, so the gate
could not run there at all; the `pillargrain` tree targeted
`kernel:FreeBSD`, so anywhere but the development host it compared two
rendering errors; and the one recorded low-state deviation named 3006
alone when 3007 does the same thing. A deviation row now lists the
majors it was observed under rather than one, so a version nobody has
tried fails and makes somebody look.

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
### 5.23 Two of SPEC 26.2's metric families are not registered

The specification's table names thirty-two; this build registers
thirty. Counted mechanically against the source, not read off the
table:

| Not registered | Why it matters |
|---|---|
| `halite_pillar_cache_hits_total` | The pillar cache is not instrumented, because there is no pillar cache: every request compiles. The counter waits on the cache. |
| `halite_pillar_ext_failures_total` | External pillar is not built at all, so this one waits on a feature rather than on the counter. |

SPEC 26.2 says "every bounded queue and every drop path in this
specification has a corresponding counter". That now holds: the
reactor, the event bus, the returner spools, the relay spool, a node's
beacon queue, a node's job queue, and a node's queue of returns waiting
for the hub all have theirs.

The gap was found while documenting the metrics rather than by anything
failing, which is the shape of it: an alert written from the
specification's table against a family that is not registered does not
error. It stays silent, and silence is what it would do if the estate
were healthy. Nine of the eleven that were missing are registered now,
and the arithmetic in this entry is held to the source by a test, so it
cannot go stale the way it was written to.

The operations guide lists what is registered, and names these two so
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

### 5.27 What building `apparmor` found

The module itself is in 2.3. Two things came out of building it that
are about this document rather than about AppArmor.

**The platform table in 2.3 said two modules were both present and
absent.** `ufw` and `netplan` were added to the Present column and left
in the Absent column of the same row. Nothing caught it:
`TestPendingPlatformModulesMatchTheSpec` holds the *registry's* pending
table to SPEC 15.3 in both directions, and had done since the row was
written, but says nothing about the markdown table a reader actually
reads — which is the only place the gap is broken down by platform, and
therefore the place a reader takes for the answer.

That is the audit's own failure mode rather than a new one: a guard
checks the thing it was pointed at, and the ledger's prose was never
pointed at. `TestTheLedgerPlatformTableMatchesTheRegistry` now reads
both columns and holds each name to the build — present means the build
answers to it, absent means it does not and is declared pending, and
every module SPEC 15.3 names appears in exactly one cell. It was checked
against the drift it was written for: restoring the two duplicated names
fails it four ways.

**A default Ubuntu cannot run `aa-status`.** It is in `apparmor-utils`,
which is not part of the base install, while AppArmor itself is on and
enforcing thirty-odd profiles. A module that shelled out to it would
have been unable to answer "what is confined here" on precisely the
nodes where it is worth asking. `/sys/kernel/security/apparmor/profiles`
is what `aa-status` itself reads, is two columns, and is always present;
this module reads it. The tools that *change* a mode really are in that
package, and the failure names the package rather than reporting a
binary that was not found.

Reading it directly has one cost worth stating: the file is root-only
while the enabled flag is world-readable, so an unprivileged
`apparmor.status` can answer whether the node is confined and not how.
It reports both facts separately rather than collapsing them, because
the collapse would answer "is this node confined" with "no" on a node
that is.

### 5.28 `snap.installed` does not take a version, and `pkg.installed` does

This is a deliberate difference from what an operator coming from `pkg`
expects, and from what Salt's community snap module offers, so it is
recorded here rather than left to be discovered.

snapd refreshes snaps by itself — four times a day by default — and that
cannot be turned off. It can be deferred, up to 60 days at a time, and
no further. A state holding a snap at a version would therefore report
the node as drifted after the first automatic refresh and on every run
after it, forever, while being unable to do anything about it. The
honest options were to refuse the argument or to hold the version with
`--revision` and a hold that expires; the second is a promise this build
would break on day 61.

So `snap.installed` manages **presence and the tracked channel**, and
refuses `version` with the reason. It is a declared parameter precisely
so that it can be refused with one: undeclared, the signature answers
"is not a parameter of this function", which reads as a typo — and a
tree carrying `version:` over from a `pkg.installed` state is not a typo
but an assumption that does not survive snapd.

**`--classic` is declared in the tree or the install fails.** A classic
snap runs with the host's own filesystem and devices; the confinement is
not weakened but absent. snapd refuses to install one without the flag
and says "repeat the command including --classic", which reads like a
formality. The obvious convenience — catching that and retrying with the
flag — would convert a confined install into an unconfined one on the
store's say-so, with nothing in the tree recording the decision. It is
not done, and the failure explains what the flag means rather than
repeating snapd's wording.

**Removal keeps the data unless told not to.** snapd saves a snapshot of
a removed snap's data, and reinstalling restores it, so a tree that
removes a snap in one state and installs it in another gets the old data
back rather than a fresh install. `purge: true` discards it. The state's
comment says which of the two happened on every run rather than leaving
it to whoever reads the argument.

**`snap list` is read by its header rather than by column position.**
snapd has renamed and reordered that table: `Tracking` used to be
`Channel`, `Publisher` used to be `Developer`, and they are in the other
order. A parser taking the fourth field as the channel reads a publisher
as one on an older node and reports every snap as tracking `canonical*`.
A row whose field count does not match the header is skipped rather than
guessed at.

None of this has been run against a real snapd. The tests supply
`snap list`'s output and record what would be run; whether `snap refresh
--channel=` switches a channel the way this expects is a question for a
node with snapd on it, which CI's Ubuntu runners have and this build
does not yet ask them.

### 5.29 The chaos layer, and what building it cost

SPEC 31's Chaos row names eight scenarios and says each must have "a
defined, tested, documented behaviour". Until 2026-09-06 `grep -i chaos`
over the tree returned nothing.

`internal/chaos` is the registry: for each scenario, SPEC's own wording,
what this build is defined to do, and what its test leaves
unestablished. Three guards hold it in place, and each was checked by
breaking it:

- Every scenario SPEC names is registered and nothing is registered that
  SPEC does not name. Adding a scenario to the Chaos row fails it.
- Every scenario has a test that names it through `chaos.Exercises`,
  found by reading the tree — the tests live in the packages whose
  machinery they drive, because the hub's lab is unexported. Pointing a
  test at the wrong scenario fails it.
- Every scenario says what its test does **not** establish. Emptying one
  fails it. This is the field that stops the layer becoming the
  reassurance its absence already was: every scenario here is a lab, and
  a lab is not an estate.

**Where the scenarios live.** Five in `internal/hub` (restart mid-job,
network partition, disk full, clock skew, certificate expiry), one more
there for the reactor's queue, one in `internal/eventbus`, one in
`internal/bridge`, one in `internal/job`. Two of the nine are existing
tests marked rather than rewritten — the extension hang and the
concurrent-writer shape — because a second test of the same thing would
be worse than a marker.

**A ninth scenario is not in SPEC.** `concurrent-bookkeeping` is the
shape all three of 4.9 and 4.11's defects had: correct read one
operation at a time, wrong when two arrive together. SPEC's eight are
all about the machine misbehaving, and none of this build's actual
concurrency defects would have been caught by any of them. It is
registered with `Spec` left empty and the guard permits exactly that,
so a scenario beyond the specification has to say it is one.

**What it found immediately.** Two things, on the first run:

1. A reader resuming from a pruned event-bus offset is silently skipped
   forward — 380 events, measured. 4.12 has it.
2. The behaviour written down for `hub restart mid-job` could not be
   tested the way it was first written, because **stopping a hub
   drains**: `Serve` waits for the batch goroutine, so a graceful stop
   always leaves a finished batch and never the half-done one the
   scenario is about. The interrupted state is now constructed rather
   than produced, and the Limit says so. That is a smaller finding and a
   more useful one than it looks: a test that stopped a hub and asserted
   partial delivery would have passed for the wrong reason on a slow
   machine and failed on a fast one.

**What it is not.** Nothing here injects a fault into a running estate,
and the disk-full scenario makes a directory that cannot be created
rather than filling a disk. `make chaos` runs the layer with `-v`, which
is the point of the target: each scenario prints the behaviour it holds
the build to and the limit of what it checked, so "what happens if the
hub restarts mid-job" is answered by the thing that tests it rather than
by a document beside it.

### 5.30 `doctor`, and the FIPS check that needed a fleet to design

SPEC 26.4 gives `halite-node doctor` and `halite-hub doctor` ten checks
— configuration validity, clock skew against the hub, certificate
validity and expiry, connectivity, file server reachability, pillar
compilation, disk space, queue depths, extension signatures, and FIPS
mode consistency — and argues for them in one line worth repeating:
"most operational tickets on a Salt estate are one of these checks, and
making them a single command is worth more than it appears."

The tree had one mention of the word, in a comment.

**The remediation line is the design.** SPEC asks for "a pass or fail
per check with a remediation line", and a check that says a certificate
expires in three days and stops has moved the problem rather than
answered it: the operator still has to know which command renews it.
`doctor_test.go` fails the build if any check can report something other
than a pass without saying what to do about it, driven through every
status each check can reach rather than through the happy path. A guard
also holds the check set to SPEC 26.4's own sentence in both directions.
Both were verified by breaking them.

**Four statuses, not two.** `skip` is the one SPEC does not name and the
one that makes the rest readable: a check that cannot run and reports a
pass is worse than one that says nothing, because it answers a question
it did not ask. A check that does not belong to the role is not run at
all rather than skipped — "queue depths: skipped, this is a node" on
every node run is noise on every run rather than information on any.

**A warning does not fail the command.** `doctor` belongs in a cron job
and in a state's `onlyif`, and a certificate three weeks from expiry
must not fail either; it is a thing to do this month, not a reason to
stop. Only a failure exits non-zero.

#### The FIPS check, and why the fleet's shape decided it

SPEC 27.4 gives the mismatch warning to `doctor`: "The `fips_mode` grain
reports both the host's kernel FIPS state and the binary's own mode, and
a mismatch is a `doctor` warning."

The facts were already reported — 1.11 records why they are separate
grains — and nothing correlated them. Both directions are worth a
warning and they are **different** warnings:

- A `-fips` artifact on a host whose kernel is not in FIPS mode reads as
  compliant and is not. Everything on the box says FIPS except the box,
  and this is the one that costs an assessment.
- An ordinary build on a host that *is* in FIPS mode makes halite the
  non-compliant component on an otherwise compliant host. An operator
  watching only the kernel's state will not think to look.

The third case is the one that needed knowing where this runs. On the
BSDs and macOS there is no kernel FIPS mode at all, and
`internal/grains/platform_bsd.go` reports `fips_mode` as a hardcoded
false so that a template does not have to guard for the platform — right
there, and wrong as an input to this check. A naive
`artifact && !kernel` would have warned on every one of those hosts.
**This project's own fleet is four FreeBSD hosts to one Linux**, so that
is a warning on four nodes in five, and a check that cries wolf on most
of an estate is a check nobody reads. The kernel state is therefore a
pointer: nil is "there is no such switch", which is a `skip` with the
reason, and it becomes a warning only if a FIPS artifact turns up there
anyway — which `FIPS_TARGETS` does not build.

The nil case carries the caller's own reason rather than one written in
the check, because "freebsd has no kernel FIPS mode" and "this command
does not read the Windows policy value" are both nils. The first version
guessed, and told a Windows operator that Windows has no kernel FIPS
mode in the same breath as saying the check applies on Windows.

#### What it does not do

The hub's pillar check compiles for a node with no grains, which reaches
the top file and every SLS matching `'*'` — the majority of a tree and
the part that breaks — and cannot reach an SLS behind a grain target.
Which files a real node gets is a question about that node, and
`halite-node doctor` compiles the whole of its own; the two together are
the answer. The hub's queue check reports the reactor's configured bound
rather than a live depth, because a diagnostic that needed the thing it
diagnoses to be running would be no use on the day it is not.

And free space is not reportable everywhere. `syscall` exposes `Statfs`
on Linux, macOS, FreeBSD and DragonFly; OpenBSD spells the same fields
`F_bavail` and `F_bsize`; NetBSD declares `Statfs_t` as `[0]byte` and
offers no `Statfs` at all, and Solaris, illumos and AIX have statvfs,
which Go does not expose. Those last four report a skip with the reason
rather than a pass on a disk nothing looked at. The first version of
that file claimed every unix and broke the build for two of SPEC 27.1's
tier 3 targets — 4.10's lesson, one working day later, caught by
`build-all` exactly as intended.

### 5.31 `pf`, and the second provider reshaping the interface

`firewallProvider`'s own comment said this would happen:

> **The interface is shaped by ufw, because ufw is the only provider.**
> That is worth stating rather than pretending otherwise. A second
> provider will probably reshape it: firewalld thinks in zones and
> services, nftables and pf in a whole ruleset that is replaced at once
> rather than a set of rules added one at a time, and neither maps
> cleanly onto "allow this port from that address".

`pf` is that second provider, and it went first — ahead of `iptables`
and `nftables` — because plan §7's re-rank found the fleet is four
FreeBSD hosts to one Ubuntu, so `firewall` shipping with a ufw provider
and nothing else meant the only host that could use it was the only host
that is not FreeBSD.

Three things came out of it.

**It manages an anchor, not `pf.conf`.** pf loads a ruleset;
`pfctl -f /etc/pf.conf` replaces the whole of it. A configuration
management system that owned that file would own every rule on the host
— including the ones an operator wrote by hand — and would discard them
on its first run. So halite loads into `anchor "halite"`, which
`pfctl -a halite -f -` replaces without touching anything else, and the
operator keeps pf.conf and decides where in the evaluation order the
managed rules sit. That last part is a decision only they can make: pf
is last-match-wins, so the anchor's position changes what it does.

**The cost of that is the worst failure this module could have, so it is
checked.** `pfctl -a halite -f -` succeeds whether or not pf.conf
contains `anchor "halite"`. Without the reference the rules load,
`pfctl -a halite -s rules` lists them back, and no packet is ever
matched against them — a firewall reporting rules it is not enforcing,
with everything looking correct. `Apply` reads pf.conf and refuses with
the line to add rather than writing rules into the void. An *unreadable*
pf.conf does not refuse: unreadable is not absent, and stopping a state
on a technicality is not the same as stopping it on a fault.

**`SetDefault` refuses, which is the interface reshaping.** ufw has a
default policy per direction as a setting. pf has whatever the last
matching rule of the ruleset says, which is a line in the file halite
deliberately does not own. The refusal names where the answer lives —
`block all` near the top of pf.conf — rather than pretending, and
`firewall.status` reports no defaults on pf for the same reason: a guess
presented as a fact is worse than nothing. That is the first place the
virtual module's shape has failed to fit a provider, and it failed in
the way the comment predicted, which is the useful outcome. It did not
require changing the interface: a provider that cannot do something says
so.

**Every rule is `quick`.** pf is last-match-wins and the module's shape —
and ufw's — is first-match-wins. Emitting `quick` makes the first match
decisive, which is what somebody writing `firewall.allowed` means. It
also makes a rule's effect independent of the anchor's order, which
matters because the whole anchor is rewritten and sorted on every
change: without `quick`, which rule won would depend on alphabetical
order.

**What a real pf established, and what this section claimed before it.**

This section originally ended: "None of it has run against a real pf.
The tests supply `pfctl`'s output and record what would be run; the
rendered rules are checked against the spelling `pfctl -s rules` prints
back, because this provider compares its own text with pf's."

The second half of that sentence was false, and it was the module's
central assumption. `pfRule`'s own comment stated it outright — "`any` is
written out rather than omitted because pf accepts both and the explicit
form is what `pfctl -s rules` prints back" — asserted, never checked.

**pf does not print back the text it was given.** It reprints from its
parsed form. `mail.edlitmus.info`, a FreeBSD host in this project's own
fleet, loaded two rules and returned them like this:

	loaded:  block drop in quick proto tcp from any to any port 9999
	printed: block drop in quick proto tcp from any to any port = 9999
	loaded:  pass in quick proto tcp from any to any port 9998
	printed: pass ... port = 9998 flags S/SA keep state

The `=` is pf writing back the port comparison it parsed. The flags and
state tracking are pf's defaults for a `pass` rule, applied whether or
not they were asked for and printed as though they had been.

So **no rule ever matched itself**. Both of mail's rules were reported as
added on every run, and `firewall.absent` could remove neither — the same
defect mirrored, because a rule that cannot be found cannot be taken
away. A firewall state that reports a change on every run is one an
operator stops reading, and one that cannot remove a rule is one they
have to reach past.

`normalizePFRule` now folds both sides into one spelling before
comparing: whitespace collapsed, `port = ` to `port `, `from any to any`
to `all` — because pf prints one or the other depending on what else the
rule constrains, so folding both sides means neither has to predict
which — and pf's default state-tracking suffixes removed.

**The test that should have caught it passed, and the reason is the
lesson.** `TestPFApplyIsIdempotent` supplied a fixture written in the
module's own spelling, so it compared this build's text against this
build's text and agreed. That is precisely the defect plan.md §1.4 generalised
after the macOS timezone and FreeBSD hostname fixtures — a fixture that
forces the shape the real thing does not take, passing while asserting
nothing — repeated in a section written by whoever had just finished
writing plan.md §1.4. The generalisation was about *code branches*; it applies
just as much to *output being parsed*, and nothing said so. It does now:
a fixture standing in for another program's output is worth as little as
its provenance, and the fixture here is what mail returned.

Two gaps remain, both needing more than a spelling change. A port list
renders as one rule and pf expands it into one rule per port, so
`{ 80, 443 }` cannot match what it is compared against; that wants the
list expanded at render time. And a rule carrying pf options this module
does not write — `modulate state`, an interface — would print back with
them and not match.

What is established now: `status`, `enabled`, `allowed` and `absent` on a
real FreeBSD host, idempotent across runs. What is still not: the anchor
refusal has not been seen refuse on hardware, and no test has watched pf
reject a rule this module rendered.

Two of the load-bearing behaviours were also checked by swapping in the
wrong implementation and watching the tests fail: dropping `quick`, and
skipping the anchor-reference check.

`jail` remains the FreeBSD row's one genuine absence.

### 5.32 `jail`, written against what `pf` cost

`jail` was the FreeBSD row's last absence, and it was written the day
after `pf`'s only real-hardware defect — a module that compared its own
rendered text against another program's output on an assumption nobody
had checked, with a test that agreed because its fixture was written in
the module's own spelling (5.31). Two things here are different because
of that, and they are the point of this entry.

**It reads `jls --libxo=json`, not the table.** `jls` prints columns that
depend on the flags, on whether a jail has an address, and on the
version; libxo's JSON exists precisely so a program does not have to
guess at a spelling. Where a platform offers a structured interface,
parsing the human one is choosing the surface that bit us.

**The envelope is checked against a real `jls`.**
`TestJailReadsWhatARealJlsPrints` runs the actual command on FreeBSD and
feeds its output to the same parser, and CI has a FreeBSD runner on every
change. This is the test `pf` did not have: every other test in that file
feeds the parser a document this build wrote, which proves only that the
parser reads its own spelling.

A host with no jails still settles the part that matters. The wrapper is
there whether or not the array has anything in it, and the wrapper — the
container names `jail-information` and `jail` — is what this build had to
guess. If the real `jls` wraps it differently the test says so by name
and points at the comment and this section, and the module keeps working
regardless: `jailEntries` looks for the container it expects and then,
failing that, for any array of objects carrying a `jid`, which is a
property of a jail entry rather than a guess about a name.

**What is still assumed, and marked as such.** The field names *inside* a
jail entry — `name`, `path`, `host.hostname`, `osrelease`, `state`. Those
need a host with a jail running and CI's FreeBSD runner has none. The
parser reads a field it does not find as empty rather than failing, so a
wrong guess costs a blank column and not a broken module, and the real-
`jls` test reports a jail parsed with no jid, which is the one field
whose absence would mean the wrong shape entirely. This project's own
fleet has FreeBSD hosts that could settle the rest in a minute.

**What it does not do.** Create or destroy a jail. A jail's definition
lives in `/etc/jail.conf` or `/etc/jail.conf.d`, which is a file, and a
module that wrote it would own every jail on the host including the ones
somebody else defined — the same argument `pf` makes about `pf.conf`, and
the same answer: `file.managed` owns the file and this owns the running
state. `jail.running` on a name nothing defines refuses and lists what
*is* defined, because the fix is a file and the tool's own error does not
say which.

The configured list comes from `jail -e ,` rather than from parsing
jail.conf here. That file has includes, variables and inheritance, and a
second parser for it in this module would disagree with the real one
eventually.

**`jail -c` and `jail -r` rather than `service jail onestart`.** The
service script honours `jail_enable` and `jail_list` in rc.conf, so a
state going through it would silently refuse to start a jail an operator
had deliberately left out of `jail_list`. Which behaviour an estate wants
is a real question; starting the jail the state names is the answer that
does what the state says, and the other is available through
`service.running` if that is what somebody means.

With this, **SPEC 15.3's FreeBSD row ships entirely**: `freebsdpkg`,
`freebsd_service` and `freebsd_sysctl` as aliases, `pf` as the
`firewall` module's second provider, and `jail`.

### 5.33 What has been demonstrated, and what has only been assumed

This build has 150 mutating execution functions across 40 modules, and
**21 modules with at least one function that changes a machine as
root** — `apparmor`, `debconf`, `dpkg`, `firewall`, `hostname`, `jail`,
`mount`, `netplan`, `pkg`, `pkgrepo`, `service`, `snap`, `sysctl`,
`sysrc`, `timezone`, `user`, `win_dacl`, `win_registry`, `win_service`,
`win_task`, `zpool`. Until now this document could say which *platforms*
had been run on and could not answer "has `apparmor.enforce` ever
enforced a profile", because nothing recorded it.

**Almost all of these modules work by running another program and
reading what it says back, and that is the half a unit test cannot
establish.** The test supplies the output, so what it checks is that the
parser reads what the test author believed the program prints. 5.31 is
what that costs: `pf` matched no rule at all on a real FreeBSD host,
reported both of the host's rules as added on every run, and could
remove neither — while `TestPFApplyIsIdempotent` passed, because its
fixture was written in the module's own spelling and agreed with itself.
plan.md §1.3 and §1.4 are the same mistake on `timezone` and `hostname`
one and two weeks earlier. Three instances is a pattern, and the pattern
is that a fixture standing in for another program's output is worth
exactly its provenance.

**So the claim is now made explicitly, per module, and it is mostly
unflattering.** `internal/builtin/evidence.go` declares one of three
levels for every module that changes something:

| Level | Means | Modules |
|---|---|---|
| hardware | the mutating path has been run against the real tool on a real machine | `firewall` (pf only), `pkg` (apt only), `win_dacl`, `win_registry`, `win_task`, `zpool` |
| captured | the module has been run against the real tool, but only reading it | `jail`, `mount`, `service`, `sysrc`, `user`, `win_service` |
| assumed | the fixtures were written from documentation or from expectation | `apparmor`, `debconf`, `dpkg`, `hostname`, `netplan`, `pkgrepo`, `snap`, `sysctl`, `timezone` |

`assumed` is the zero value, so a module nobody has classified reads as
undemonstrated rather than as absent. Each declaration carries a note
naming the tool and the doubt, and a guard requires one from every
root-mutating module: the default being right is not the same as the
default having been decided, and an operator is owed the difference
between "considered and unverified" and "nobody looked".

**Four places carry it, in the order an operator meets them.**

- `sys.evidence` answers per module, before anything is run. Not in SPEC
  15.6; the ledger's `sys` row records the addition.
- `doctor` gains a **module verification** check, which SPEC 26.4's list
  now names. It warns and never fails, and only for the modules that
  need root — a module that changes a machine with no privilege can be
  wrong without being dangerous, and grading those the same way is how a
  warning becomes something people scroll past.
- A **failing** mutation appends the note to its error. Only a failing
  one, and only a mutating one: a read that goes wrong is a question
  about the node, a change that goes wrong is a question about both, and
  that is the moment the second half is worth raising. It never appears
  on success, because a warning nobody can act on is a warning people
  learn to skip.
- `make release-gate` refuses a build in which any root-mutating module
  is still an assumption, and it is the release workflow's first job.

**The gate is the one control here that cannot make things worse.** It
has exactly one failure mode — a release does not happen — and a release
that does not happen breaks nothing. Everything else in this list
informs somebody who is already looking; a release reaches operators who
are not, because a fleet upgrades and whatever shipped is running as
root on every host in it. It sits behind a build tag so that ordinary
development is not blocked: a module written today is undemonstrated
today, and that is the normal state of new work.

There is no override, and that is deliberate rather than absolute. An
override is what gets used at five on a Friday. The two ways past the
gate are the two that leave an operator no worse off: run the module
against the real tool and say which machine, or take it out of the
build.

**As it stands the gate is red**, on three modules — nine when this was
written; 5.35 closed four and 5.36 two more. That is the honest
position rather than a defect in the gate: `netplan` reconfigures the
interface an operator is connected over and has never been run against a
real netplan, and `sysctl` sets kernel parameters and has never set one.
Neither is known to be wrong. Neither is known to be right, which is the
point.

**What this cannot do** is tell whether a declaration is true. No test
distinguishes a module verified on hardware from one whose row says so.
That is why every level above `assumed` requires a note naming the
machine or the captured output: the note is the part a person can go and
check, and a level with no note would be a claim that could not be
audited.

Writing the table also found three wrong cross-references in this
document, which is its own small argument for the exercise: `zpool`'s
hardware evidence is 4.7 and not the module table, the Windows DACL
runner is an administrator (4.9) so CI's DENY coverage is *weaker* than
assumed rather than stronger, and 5.31's reference to "1.4" meant
plan.md's section 1.4 and read as this document's.

### 5.34 Tracing, and the wiring that had to follow it

SPEC 26.3 asks for W3C Trace Context propagation, a span per job, per
state and per file transfer, and OTLP over HTTP with JSON encoding, off
by default and sampled when on.

**This shipped in two halves, and the first half was a mistake.** The
propagation, the span model, the sampler and the exporter landed as
`internal/tracing` with nothing starting a span: no job, no state and no
file transfer was traced, `tracing` remained an inert key, and an
operator who set `tracing: otlp` got what 4.x's inert-key table
promised — nothing, silently. That was documented rather than hidden,
and documenting it was not sufficient. A package carrying a
specification section's name reads as a feature whatever the ledger
says, and this section now records the completion rather than the seam.

#### The two external formats, owned rather than imported

SPEC chose OTLP/HTTP with JSON because it "needs no OpenTelemetry SDK",
which is a dependency decision under SPEC 4.2 as much as a wire one. The
cost is that nothing but this build's own tests stands between a mistake
in either format and a collector quietly misreading every span, so both
are checked against their specifications' own examples rather than
against what the code produces — the lesson 5.31 cost, applied before
rather than after.

Two things in OTLP/JSON are easy to get wrong in exactly the way that
produces a body a collector accepts and misreads:

- **Identifiers are hex, not base64.** Proto3's JSON mapping encodes a
  `bytes` field as base64; OTLP overrides that for `trace_id`, `span_id`
  and `parent_span_id`. A base64 identifier is a perfectly good string,
  so nothing errors and the trace never joins up.
- **64-bit numbers are strings.** A nanosecond timestamp is about
  1.7 × 10^18 and a float64 is exact to about 9 × 10^15, so a JSON
  number loses the last digits — which is the resolution a span duration
  is made of.

And a root span omits `parentSpanId` rather than sending a zero one: a
present-but-zero parent is read as a parent that does not exist, and the
span hangs off nothing instead of being a root.

#### Off means a nil pointer

SPEC has tracing off by default, and off here costs no goroutine, no
buffer and no allocation per job: `Tracer` and `Span` tolerate a nil
receiver on every method, so a call site never guards. A call site that
has to guard eventually forgets to, and the one it forgets is a nil
dereference in a hub.

**That property has exactly one hole, and the wiring found it.** A nil
span's *methods* are safe; its *fields* are not. `Dispatch` read
`span.Context` to put the identifier on the job message, and the first
test that ran a hub with tracing off panicked in an HTTP handler.
`SpanContextOf` existed for this and the call site had not used it.
Every propagation site goes through it now.

#### The sampling decision is inherited and never re-made

A trace sampled in at the hub and out at the node has a hole exactly
where somebody is looking, which is worse than sampling everything or
nothing. An unsampled span is still created and still propagates its
identifiers, because W3C requires a system that is not recording to pass
the context along — otherwise a sampled trace crossing it loses its
middle.

#### A slow collector costs spans, not a fleet

A finished span is queued and dropped when the queue is full, never
waited on: the caller is a hub dispatching a job, and telemetry is
allowed to lose data where a fleet is not allowed to stop. The drops are
counted rather than logged one at a time, on the argument the reactor's
queue overflow already made. Measured with the exporter deliberately
wedged: 197 of 200 spans dropped and nothing blocked.

The same argument decides what a misconfiguration does. A `tracing`
value that is not one of the two names, an endpoint that is not an HTTP
URL, and a sample rate outside 0 to 1 are each refused — but the refusal
is logged at error level and the process runs with tracing off, never
fatal. A node that will not start because a collector URL has a typo in
it takes a machine's management with it, and no telemetry is worth that.
The refusal exists so that the operator is told, not so that the node is.

**`tracing_sample_rate: 0` is refused rather than obeyed**, and it is
the one refusal that needs its own sentence. It is indistinguishable in
behaviour from `tracing: off` and distinguishable in intent: whoever
wrote it believed they had turned something on. An absent rate is the
default instead, because an operator who wrote nothing did not ask for
nothing. The default is a tenth rather than the conventional hundredth —
at 1% a five-host fleet applying a highstate every half hour records
roughly one trace a day, which is a feature that appears not to work.

#### Where the spans start, and where they deliberately do not

| Span | Where | Kind |
|---|---|---|
| `dispatch <fun>` | the hub, once the job exists and before delivery | server |
| `job <fun>` | the node, under the `traceparent` the message carried | server |
| `state <module.function>` | the node, per state that **executed** | internal |
| `file fetch` / `file hash` / `file list` | the node, per request to the tree | client |
| `file serve` | the hub, under the node's file span | server |

**The dispatch span ends when `Dispatch` returns, not when the job
does.** A job is asynchronous by design — the hub writes it to each
node's stream and the returns arrive minutes later — so a span held open
until the last return is a span held open across a node that never
answers. What it measures is the hub's part: resolving the target,
recording the job, writing it to every stream. The node's spans continue
the trace and outlive it, which is what an asynchronous fan-out looks
like in a trace.

**A state's span is started where the state is executed, not in the loop
that walks the chunks.** A highstate is mostly declarations that
converge or are held by a requisite, and a trace with a span per chunk
buries the four that did work among the four hundred that did not. Each
span carries `halite.state.changed`, because "which states actually did
something" is the question a highstate trace is opened to answer and a
converged run is nearly every run. The retry loop is inside the span: a
state that succeeded on its third attempt took as long as all three,
and that is the duration somebody is looking for.

**`file serve` is started only when the request carries a
`traceparent`.** A file transfer with no job above it is a fragment
nobody can use, so an untraced node — or one older than this — produces
no hub-side span rather than a root.

**What is not traced, and why.** `Exists` on the file tree, because it
is answered from the cache in the common case and a span per existence
check would bury the transfers in the checks that preceded them. Reading
an SLS out of the tree, because that is compilation rather than a
transfer and a span per included file is a span per line of a top file.
And `halite-node call`, which produces none of SPEC 26.3's three spans,
so there is nothing for it to export.

#### What the propagation costs, and what it does not

The `traceparent` reaches a node **on the job message rather than in a
header**, because a job crosses the subscribe stream — one long-lived
HTTP response carrying many messages. A header belongs to the stream and
a trace belongs to the job. An older node ignores the field, which is
the wire tolerance 4.13 turned from an accident into a guarantee, and an
untraced hub sends a message byte-for-byte identical to the one this
build sent before tracing existed. Both directions are held by a test.

**It is not written to the job record.** The field is `json:"-"` — it is
in-flight state rather than a record, and adding a field to the
persisted record would mean an older build silently dropping it on a
rollback, which is the defect 4.13 exists to have fixed once.

**Every HTTP request carries it through a round tripper, not through
each request builder.** There are ten builders and there will be more,
and a header that must be remembered at each one is a header that is on
nine of them: a file fetch traced end to end beside a pillar compile
that is not reads as the hub declining to take part. The round tripper
clones the request rather than modifying it, because `net/http` retries
one.

#### Two defects the wiring found

**`Tracer.Stop` panicked when called twice.** `close of closed channel`,
on the shutdown path. Shutdown is where two paths meet — a deferred
flush and a signal handler, a test's cleanup and its own explicit
drain — and a shutdown path that dies when both arrive is worse than one
that does nothing. It is idempotent now, and the second call still waits
for the drain the first started.

**A file transfer ignored its caller's context.** `Remote.cacheFile`
passed `context.Background()` to every fetch, so a job cancelled by
`jobs kill` while fetching a large file kept fetching, and there was
nowhere for a trace to travel. `Remote` now carries a context, set per
run through `WithContext`, and the digest cache moved behind a pointer
so that the copy is legal. The cancellation is worth more than the
tracing was.

#### What a real run established, and what it did not

The unit tests here check this build against the two specifications'
examples. They do not check that a `halite-node` binary, configured
from a file, exports anything at all — which is a different claim and
the one 5.33 is about. So it was run: `halite-node state apply --local`
against a two-state tree, with `tracing: otlp` and a collector process
listening on 4318, and the JSON that arrived was read.

It arrived correct. One trace, a `state apply` root of kind 2 with no
`parentSpanId`, two `state test.succeed_without_changes` children of
kind 1 both naming the root as parent, identifiers in hex, timestamps
as strings, `halite.state.changed` false on both, and the resource
carrying `service.name` and `service.version`.

**Every timestamp in it was identical**, which looked like a defect and
is not. Measured on this Windows 11 host: over 500,000 reads of
`time.Now()` with nothing sleeping, the wall clock advanced 8 times and
the monotonic reading advanced with it, the smallest step being 518µs.
Two `test.succeed_without_changes` states take less than that, so the
spans genuinely begin and end within one tick.

The first attempt at a fix — deriving the end from `Finish.Sub(Start)`
so that the monotonic reading survives `UnixNano` — was written, tested
and then reverted, because the measurement above shows the monotonic
clock is no finer here and the change fixed nothing that had been
demonstrated. It is recorded because writing it was the mistake this
document keeps describing, caught one step earlier than usual: a
plausible cause, a change that would have looked like a fix, and a test
that passed for an unrelated reason (`time.Sleep` raises the timer
resolution, so the test never entered the case it was written for).

**What an operator should expect from this**: on a platform whose clock
is coarse, a span shorter than one tick has a duration of zero. That is
the platform rather than halite, it does not affect ordering or
parentage, and it matters least where tracing matters most — a state
that took no measurable time is not the one being investigated.

#### What is still not established

**No real collector has read a span this build produced.** The one in
the run above was thirty lines written to capture the request body; it
proves the export path, the payload's shape and the configuration, and
it proves nothing about whether Jaeger, Tempo or an OpenTelemetry
Collector *interprets* it as intended. "A body a collector accepts and
misreads" is precisely the failure this section spends two paragraphs
on, and a collector that only records is not one that has read.

The hub's half is less established still: the propagation is held by a
test against a real HTTP boundary, and no hub-dispatched job has been
traced through to a node on real machines. That is the same gap the
module evidence table (5.33) exists to make visible, one layer up, and
it is what the first estate to set `tracing: otlp` on both ends will
settle.

### 5.35 A synthetic Debian, and four modules that have now met their tools

5.33 left nine root-mutating modules declared `assumed`, and the release
gate red on all nine. Four of them — `dpkg`, `debconf`, `pkgrepo` and
`timezone` — needed no machine that does not exist. They needed a Debian
with the real tools on it, and `make fleetcheck` is that.

**A container is not a stand-in here, and that distinction is the whole
argument.** `sysctl` in a container is not the real kernel and `netplan`
in a container is not the real network stack, so neither is covered
below. But `dpkg` in a container *is* Debian's own `dpkg`, at Debian's
own version, reading Debian's own package database — the same binary a
node runs, doing the same thing. The machine is disposable; the tool is
not synthetic.

That is what makes the run destructive on purpose. It holds a package
through `dpkg --set-selections`, writes an answer into the debconf
database, adds and removes a signed apt repository, and relinks
`/etc/localtime`. Reading is not enough: the modules in question are the
*mutating* ones, and a parser that reads correctly says nothing about
whether the write took.

#### It has no network, and the first attempt at saying so did not hold

Everything the run needs is baked into the image: a real `.deb`, a local
apt repository, a signing key, the zone files. `run.sh` asserts each of
them before starting, and asserts its own networking too — `--network
none`, so the container has loopback and nothing else, and the run
refuses if it finds an interface beside it.

This build compiles with `GOPROXY=off` from a vendored tree. A check
that reaches `archive.ubuntu.com` goes red for reasons unrelated to the
change under test, and a gate people learn to ignore is worse than no
gate — which is the argument this document has made about every other
guard here. It applies to this one too.

**The first version of that assertion did nothing, and said so.** It
used `unshare -n` inside the container, which needs `CAP_SYS_ADMIN`;
the container has neither that capability on this developer's Docker
nor on a GitHub runner, so the fallback path ran every time and printed
that the isolation had not been applied. It was noticed by reading a
green CI log rather than by anything failing — which is the argument for
making a check say what it did rather than only whether it passed.

`--network none` is enforced by the runtime instead of asked for by the
process, and the container cannot opt out of it. The cost is that the
image must carry the toolchain rather than fetching it, so it is built
`FROM golang:1.26.6-bookworm` with `GOTOOLCHAIN=local` — pinned to
`go.mod`'s own `toolchain` directive, and held there by
`internal/buildpolicy`, because a pin that moved would otherwise surface
as a network error rather than as a version mismatch.

**Debian's own sources are disabled in the image, after the build has
used them.** With no network, every apt call spent about nine seconds
per upstream source failing to resolve `deb.debian.org` before reaching
the local one — twenty-eight of the thirty seconds a run took — and
bought nothing, because those failures are warnings apt ignores anyway.
They are commented out rather than deleted, so `pkgrepo.list_repos`
still sees a realistic `sources.list.d`.

**The apt repository is signed rather than trusted, for the same
reason.** apt refuses an unsigned repository, and the way round that is
`[trusted=yes]` — which `pkgrepo` does not offer as a declared field.
The field it does offer is `signedby`, which names a keyring, and that
is what an operator actually sets. So the image generates a key, signs
the repository, and the test names the keyring: the field is exercised
rather than stepped past.

#### What the first run found

Ten tests, and on the first run against the real tools **six of them
failed**. Every one was the test's fault rather than the module's, and
that is worth recording rather than quietly fixing:

- `debconf.set` takes `question`, `type` and `value`; the test passed a
  `data` map.
- `pkgrepo.mod_repo` takes `baseurl`, not `uri`, and has no `opts`.
- `timezone.set_zone` takes `timezone`, not `name`.
- `timezone.list_zones` returns `[]string`, and the test asserted
  `[]any`, so it read zero zones from a listing of hundreds.
- `dpkg.search` is keyed by owning package holding the paths, not by
  path — because several packages can own one path.
- `dpkg.get_selections` is flat, package to selection word, rather than
  grouped by state the way Salt returns it.

Six wrong assumptions about this build's own interfaces, made by
somebody who had just read them. That is the same failure mode as a
fixture written in the module's own spelling, pointed the other way, and
it is the reason a live test is worth more than its unit test even when
it passes.

#### Every assertion was broken on purpose, and one break was not a break

Five deliberate defects, each reverted after it was seen to fail:

| Break | What happened |
|---|---|
| `signed-by=` dropped from the apt source line | `apt-get update` refused the repository |
| the state written before the package in a selection line | dpkg reported `install`, and the module reported no change |
| `debconf-set-selections` fields reordered | debconf said `warning: Unknown type true, skipping line 1` |
| the architecture and version columns swapped | both fields disagreed with `dpkg-query` |
| `linkZone` made a no-op | the zone read back as the old one |
| the repository written to a directory apt does not read | apt's index did not name it |

The debconf one is the most instructive. **A malformed line makes
`debconf-set-selections` warn and carry on**, rather than failing — so a
module that ignored its stderr would silently answer nothing and report
success. This one surfaces it as an error, which was true before this
test existed and was not known to be true.

**And one break was not a break.** Removing the trailing newline from
what `dpkg --set-selections` is given changed nothing, because dpkg
tolerates it. It is recorded because the first version of the
architecture assertion also did not bite: `the version is not empty`
passes with the columns swapped, since an architecture is a non-empty
string. That assertion now compares each field against what
`dpkg-query` says about the same package. An assertion with nowhere to
fail is the thing this whole section is about.

**The repository check had the same weakness and it took a measurement
to see it.** `apt-get update` was asserted to exit 0, which is
necessary and not sufficient: measured in this image, apt reports an
*unreachable* source as a warning and still exits 0. It exits non-zero
for a repository it *rejects* — an unsigned one — so that check does
bite for the case it was written for, and it would not have noticed a
repository apt had quietly ignored. Breaking `aptSourcesDir` to a
directory apt does not read demonstrated exactly that: the exit code
stayed 0. The test now also asks `apt-cache policy` whether the index
holds a package from the repository, which is the assertion that the
publish actually took.

Dropping `signed-by=` is worth noting for the opposite reason: it fails
*inside the module's own refresh* rather than in the test's check, with
apt's own words — `NO_PUBKEY`. The module surfaces what apt said, which
is what an operator needs and was not previously demonstrated.

#### What it establishes, and the four boundaries it does not cross

`dpkg`, `debconf`, `pkgrepo` and `timezone` move from `assumed` to
`hardware` in the evidence table, and the release gate is red on five
modules rather than nine. Each note carries its own scope, because the
scope is narrower than "verified":

- **One distribution, one version.** Debian 12, dpkg 1.21.23, debconf
  1.5.82, tzdata 2026b. Ubuntu's apt is close and not identical, and
  nothing here says anything about RHEL, SUSE or Alpine.
- **One init system, which is none.** `timezone`'s `timedatectl` branch
  needs systemd running and is not covered; the container takes the
  zone-file branch. That is the branch a FreeBSD node takes too, so it
  is the more useful half — but it is half.
- **The filesystem, not the network.** The apt repository is a `file://`
  one. A repository over HTTPS, with the redirects and mirrors that
  implies, is not exercised.
- **`dpkg.verify` is not driven**, because provoking a real checksum
  mismatch means damaging an installed package, and the cleanup is
  worse than the coverage.

#### Why it is not a pull request gate

`zfscheck` set the precedent and the reasoning is the same: it needs
Docker, it takes minutes, and a failure is worth a person reading rather
than a merge button going grey. A module's dealings with `dpkg` do not
change because somebody edited the YAML parser, so running it on every
push would mostly be running it for nothing — and a check that is
usually irrelevant is one people learn to skip.

It runs nightly, on demand, and on a push that touches the modules it
drives or the image itself. The evidence table's claim is only as good
as the last run, which is what makes the schedule part of the claim.

#### The five that were left, and what each actually needed

5.36 closed the first two of these, and it did so with no new
infrastructure: the machines were already there.

- **`snap`** — snapd is already on a GitHub Ubuntu runner. The obstacle
  is the network: `snap install` fetches, and there is no offline
  equivalent of the local apt repository above.
- **`apparmor`** — the runner's own kernel has it, and loading a profile
  needs `CAP_MAC_ADMIN`. Plausible in a privileged container or directly
  on the runner; unverified.
- ~~**`sysctl`**~~ — **done** (5.36), and the prediction here was
  half right: a container is indeed not honest for it, and the
  disposable virtual machine turned out to be the CI runner itself
  rather than one booted under KVM.
- **`netplan`** — `netplan apply` reconfigures the interface the job is
  running over. It needs a network namespace or a nested machine, and it
  is the module with the worst consequence if it is wrong.
- ~~**`hostname`**~~ — **done** (5.36). "The Linux branch is a container
  away" was wrong: Docker bind-mounts `/etc/hostname`, so the atomic
  replace cannot work there either. Both branches went to real machines.

### 5.36 The two modules a container could not reach

5.35 closed four of the nine root-mutating modules the release gate was
red on, and named the five that were left with what each would take.
`hostname` and `sysctl` were the first two on that list, and both needed
the same thing the others did not: **a machine, not an image.**

**`sysctl` is the kernel.** A container shares the host's, `/proc/sys`
is mounted read-only inside one, and a container that remounted it would
be writing to the kernel of whoever ran the test. There is no honest
version of this in a container: measured with `--cap-add=SYS_ADMIN`,
even a namespaced `net.*` parameter is refused, and the ones that are
*not* namespaced would have reached the developer's own machine.

**`hostname` got closer and still could not.** A container has its own
UTS namespace, so `hostname(1)` really does change the running name
there — with `CAP_SYS_ADMIN`, which the default container does not have.
But Docker bind-mounts `/etc/hostname` from outside, so this module's
atomic replace — write a temporary file beside it, rename over it —
fails with `device or resource busy`. That is the container rather than
the module, on a file that is an ordinary one everywhere halite actually
runs. And `hostnamectl` is not running in an image, which is the branch
a systemd node takes.

#### The machine that was already there

A GitHub runner is a fresh virtual machine per job with a real kernel,
destroyed minutes later. So is the FreeBSD virtual machine CI already
boots for every change. Renaming either and moving one kernel parameter
costs nothing, and needs no nested virtualisation, no privileged
container, and no new infrastructure at all — which was the more
elaborate answer this was about to reach for.

Two legs, and they exercise **different branches** rather than the same
code twice:

| | Linux (Ubuntu 24.04 runner) | FreeBSD 15.1 (CI's virtual machine) |
|---|---|---|
| `hostname` running + persistent | `hostnamectl`, systemd running | `sysrc`, rc.conf |
| `sysctl` persist target | `/etc/sysctl.d/` drop-in | `/etc/sysctl.conf` |
| `sysctlAssign` spelling | `sysctl -w name=value` | the bare BSD form |

The FreeBSD half matters most. `hostname`'s `sysrc` branch **had no test
at all behind a fixture that looked like one** — plan.md §1.4, the
finding that made this project audit its platform branching — and it is
now driven on a real FreeBSD. And `sysctlAssign` tries two spellings
because BSD and Linux disagree about `-w`; which one each platform
accepts had never been checked on the side that is not Linux.

Everything is captured and restored. Not for the runner's sake — it is
deleted — but because these are meant to be runnable on a real host by
somebody who wants the answer for their own platform, and a test that
leaves a machine renamed is one nobody runs twice. A separate CI step
asserts afterwards that the machine got its name back, so a cleanup that
stopped working would be visible rather than merely absent.

#### Three of the six passed for the wrong reason, and one was invisible

They passed on the first run, which after 5.35 was itself suspicious.
Two deliberate breaks were pushed to find out.

**`sysctl.present` persisting without setting the running value** was
caught twice on both platforms — by the kernel read-back and by the
convergence check. That is the assertion working.

**`hostname.get_persistent` replaced with `runningHostname()`** was
caught by nothing. Every test in the file passed. The reason is
structural rather than careless: `set_hostname` sets *both* halves, so
after it runs the two names are legitimately equal and a module that
confuses them agrees with a module that does not. Adding a direct read
of `/etc/hostname` beside the module's answer did not help either, for
the same reason — both were the string the test had just written.

**The two have to be pulled apart to tell them apart.** So there is now
a test that renames the machine with `hostname(1)` alone, leaving the
boot-time record untouched, and asserts that `get_persistent` still
reports the old name while `get_hostname` reports the new one. With the
break reapplied it fails and names the case in its own message:

> This is the node somebody renamed by hand, and the module cannot see it

Which is the case `hostname` exists for, in the module's own comment —
*"the node that was renamed by hand and would have gone back on the next
boot"* — and nothing had tested it. The state is asked about it too, and
has to report the running half as wrong and the persistent half as
already right, because reporting "both" there would tell an operator to
worry about a file that is fine.

#### The one deliberate step away from realism

`sysctl.present` is driven with its `config` argument pointed at a
temporary file rather than the platform's real `/etc/sysctl.conf` or
drop-in directory. The reason is specific rather than convenient:
appending to a machine's own `sysctl.conf` and then editing it back out
is a rewrite of a file an operator may have hand-maintained, and getting
that restore wrong costs them something a test has no business costing.
The path is the only difference — `writeSysctlConf` does not branch on
it — and the note in the evidence table says so rather than claiming the
real path was exercised.

#### Where the gate stands

`hostname` and `sysctl` move to `hardware`. **The release gate is red on
three modules**: `apparmor`, `netplan` and `snap`. It was nine.

Each of the three needs something the previous six did not:

- ~~**`apparmor`**~~ — **attempted** (5.37), and the forecast here was
  wrong in a useful way. A runner can load a profile; that was never the
  obstacle. The obstacle is that Ubuntu 24.04's own `aa-*` tools cannot
  parse Ubuntu's own profiles, so every mutating function in the module
  is inoperable on that platform. The reading half is verified; the gate
  stays red on it.
- **`snap`** wants snapd, which an Ubuntu runner already has, and the
  network, which the no-network rule of 5.35 refuses. Either a
  pre-seeded snap or an exception, and the exception is the wrong
  answer.
- ~~**`netplan`**~~ — **done** (5.38), and the forecast here was wrong
  in the way that mattered: it wants no network namespace and no nested
  machine, because the module never applies unless asked. Writing the
  document and having real `netplan generate` validate it is the half a
  fixture could not reach, and it ran on this project's own Ubuntu host
  in an afternoon, as predicted.

### 5.37 `apparmor`: the reading half is verified and the writing half cannot run

5.36 left three modules blocking the release gate and ranked `apparmor`
second, on the grounds that a GitHub runner's kernel has AppArmor and
the only open question was whether a job can load a profile. It can. The
answer to the question nobody asked is the finding.

#### What is now demonstrated

**securityfs parses.** `parseAppArmorProfiles` reads a file this build
had never opened, and its format was taken from documentation. Against
a real Ubuntu 24.04 it reads **123 profiles** correctly — 91
unconfined, 28 enforce, 4 complain, 0 kill — and every mode it produces
is one AppArmor actually has. The profile count agrees with the number
of non-empty lines in the file, checked directly rather than through the
module, so a parser that silently dropped a line it did not recognise
would be caught.

That was one of the three things 5.33 recorded as guesses, and it is a
guess no longer.

#### What cannot run, and it is not halite

The other two guesses were about `aa-enforce` and `aa-complain`. Neither
can be answered on Ubuntu 24.04, because **the tools do not work there
at all**.

The `aa-*` tools are Python, and before doing anything they parse every
profile under `/etc/apparmor.d` with their own parser rather than with
`apparmor_parser`. Ubuntu 24.04's apparmor-utils 4.0.1 cannot read the
profile set Ubuntu itself ships:

    aa-complain halite-live-probe:
      ERROR: Operation {'runbindable'} cannot have a source. Source = AARE('/')

The rule is in `abstractions/passt`, shipped by Ubuntu's own `passt`
package — nothing exotic, nothing Docker installed. And it fails
identically on `/usr/bin/man`, so it is not about the profile being
asked for. `aa-enforce` and `aa-disable` fail the same way. Every
mutating function this module has goes through those three tools, so on
that platform **`apparmor.enforce`, `apparmor.complain` and
`apparmor.disable` cannot work by any means.**

**Two workarounds were tried and both are recorded rather than kept.**
Moving the abstraction aside produced `ERROR: Include file
/etc/apparmor.d/abstractions/passt not found`, because other profiles
include it. Commenting out the single unparseable rule produced the next
one — `ERROR: Can't parse mount rule mount "" -> "/tmp/",`. At that
point the workaround is the story, and the story is that this platform's
own tools do not work.

So the CI leg does nothing to the machine and the tests say so and skip.
They **skip rather than fail** because a nightly that is permanently red
is a nightly nobody reads; what keeps that from being a silent pass is
the release gate, which still refuses to ship `apparmor` as
demonstrated. The colour of a test is not the enforcement here — the
gate is.

#### The one change this found

`apparmor.status` reported `tools: true` whenever `aa-enforce` was on
`PATH`. On Ubuntu 24.04 it is on PATH and cannot run, so that field was
answering a question nobody asked — "is the binary installed" — while
appearing to answer the one they did: **can a mode be changed on this
node.**

It asks now, by running the tool against a profile no machine has. The
tools parse the whole tree *before* looking up the name, so a tree they
cannot read fails at the parse and a tree they can read fails at the
lookup; the two are told apart by which error comes back, and nothing is
changed either way. A node where they cannot run reports `tools: false`
and a `tools_reason` saying why, in the operator's terms:

> the aa-\* tools cannot parse this node's profile tree, so no mode can
> be changed on it by any means

That is a field that was quietly wrong on the commonest Linux this
project targets, and only running it found that.

#### What would close this, and the decision it needs

Three routes, and the third is a question for a person rather than a
commit:

1. **A machine whose tools work.** Any distribution whose apparmor-utils
   can parse its own profiles — an older Ubuntu, or a Debian, or a
   Ubuntu 24.04 without `passt` installed. The tests are written and
   would run there unchanged; this is an afternoon on a host that has
   one.
2. **Wait for apparmor-utils.** The Python parser catching up with the
   profiles the same distribution ships is somebody else's fix, and not
   one to plan around.
3. **Stop depending on the `aa-*` tools.** `apparmor_parser` is C, is
   installed by default, parses everything the kernel does, and already
   does the loading here — `apparmor.reload` uses it and works on this
   machine. It can load a profile in complain mode directly.

   The catch is persistence, and it is the reason this is a decision
   rather than an obvious improvement. `aa-complain` *edits the profile
   file* to add `flags=(complain)` and then reloads it, so the mode
   survives a reboot. `apparmor_parser --complain` sets the running mode
   and changes no file, so the next boot loads the profile as written
   and the mode is gone. Reimplementing the file edit means this project
   parsing AppArmor profile syntax, which is a larger thing to own than
   it looks and is exactly the sort of surface 5.31 was about.

   Doing it would make the module work on a platform where it currently
   cannot, at the cost of either losing persistence or taking on a
   parser. That trade is not one to make quietly.

`apparmor` therefore stays `assumed` and the gate stays red on it — but
the note is now a fact with a reproduction rather than "nobody has run
it", which is the difference this whole exercise is for. The gate is
red on **three** modules: `apparmor`, `netplan` and `snap`.

### 5.38 `netplan`: the encoder writes netplan's YAML, and it is never applied

5.37 ranked the three remaining modules and said `netplan` was "the one
this project's own Ubuntu host could settle in an afternoon." This is
that afternoon.

#### What is now demonstrated

**The directory parses in the order netplan reads it.** `netplanFiles`
lists `/etc/netplan` lexically — the reason a tree numbers its files —
and against the real directory on Ubuntu 24.04 the module's listing
matches a direct read, suffix filter and sort included. One of the
machine's own `90-NM-*.yaml` files, mode 0600, parses to a document
rooted at `network:`, and `netplan get all` agrees the tree is well
formed.

**The document this module writes is the document netplan reads.**
`netplan.managed` renders its `config` with this package's own YAML
encoder. That was a 5.33 guess — "the YAML this writes has never been
round-tripped through a real netplan" — and it is the exact shape of
mistake 5.31 cost a firewall. Now: the state writes
`98-halite-live-probe.yaml`, real `netplan generate` (netplan 1.1.2)
accepts it, and `netplan get ethernets.hal0probe.optional` returns
`true` — the value that went in, read back out by netplan's own parser.
The file is 0600, and a second run reports no change.

**A document netplan rejects fails the state, with netplan's own words.**
A config carrying an unknown key (`addressess`) makes real `netplan
generate` fail; the module reports it with netplan's message and the
sentence "nothing has been applied", and the file is left on disk where
5.33's fixture said it would be. The fixture text and the real text
match.

#### What was not run, and that is the design

**`netplan apply` was not run, and no live test will run it.** Applying
reconfigures the interface the run arrives over, and whether the node
stays reachable afterwards is a fact about the network and not about the
file — there is no dry run that can prove otherwise. The module is built
around this: `netplan.managed` writes and validates and stops, and calls
`netplan apply` only when a declaration names `apply: true`. A live test
that applied would be exercising the one path the module exists to keep
behind an explicit request, on the interface the test is talking over.

So the bounded gap is `netplan apply` itself — nothing has watched it
run. What is demonstrated is `netplan.managed`, the state an estate
actually writes, minus that opt-in branch. That is the same shape as
`firewall`'s "still not seen on hardware: the anchor refusal refusing"
and `sysctl`'s redirected persist path: a gap named and reasoned about,
not one nobody looked at.

#### Where it runs

`HALITE_SYSTEM_LIVE=1`, the gate `hostname`, `sysctl` and `apparmor`
share — here on this project's own netplan-managed Ubuntu 24.04 host,
which is where the Linux provider work is done. Not the fleet container:
it has no netplan, and 5.35's no-network rule would refuse it anyway.
The CI `linux` leg gains it for free, because the test never applies —
a GitHub `ubuntu-24.04` runner has netplan, and writing a file plus
`netplan generate` touches no interface.

The probe brings its own file, like `apparmor`'s: numbered `98-` to sort
last, `renderer: networkd` so a NetworkManager host never sees it, an
interface name (`hal0probe`) that matches no hardware, and `optional:
true` so an apply that never happens could not stall a boot. It is
removed and the tree regenerated in a cleanup that runs whatever the
test did.

#### Where the gate stands

`netplan` moves to `hardware`. **The release gate is red on two
modules**: `apparmor` and `snap`. It was nine. `apparmor`'s red is a
documented platform defect with a reproduction (5.37), not an untried
path; `snap` still wants snapd and the network together, and the
network is what 5.35's rule refuses.

### 5.39 `service`: systemd over its own D-Bus API

SPEC 15.2: systemd "is spoken to over its D-Bus API where available,
falling back to `systemctl`; the D-Bus client is a direct implementation
of the wire protocol over a unix socket, since D-Bus marshalling is
well-specified and small." The build did only `systemctl` -- the same
thing Salt's `systemd_service.py` does -- and plan.md §6 item 9 and §4.5
both tracked it. This closes it.

#### The client, and what it is not

`internal/dbus` is about 470 lines: the SASL EXTERNAL handshake,
little-endian marshalling for the handful of types a systemd `Manager`
call needs (`y b u s o g a () v`), the four message types, and one
connection used one call at a time. It is not a general D-Bus library --
no session bus, no fd passing, no property `Set`, no async dispatch --
because nothing here needs them.

It is written rather than vendored because SPEC says to, and the reason
holds up: `github.com/godbus/dbus` and `github.com/coreos/go-systemd`
are a dependency to audit and a wire surface to track upstream, against
a protocol whose marshalling fits on a page. The *job-completion* model
is borrowed from `coreos/go-systemd` -- a `JobRemoved` subscription --
without the code.

#### Job completion, which is the awkward part

`systemctl start` blocks until the job finishes. The raw `StartUnit`
call returns a job object path immediately and the unit may not be up
yet. So the binding calls `Manager.Subscribe`, installs an `AddMatch`
for `JobRemoved`, and reads until the signal for its job path arrives --
which systemd can emit *before* the method return, so job results are
buffered by path until the return names one. `"done"` is success;
`"failed"`, `"canceled"`, `"timeout"` and `"dependency"` are the error.

The live leg makes the wait observable: the probe unit has a one-second
`ExecStartPre`, and a provider that did not wait would return while the
unit was still `activating` rather than `active`. It asserts `active`
the moment `service.start` returns.

#### The fallback boundary

Fall back to `systemctl` only when the bus was never reached -- no
socket, authentication refused, `Hello` failed. An error that came
*back* from systemd -- an unknown unit, a polkit refusal, a job that
failed -- is returned as-is and never retried on the shell, where it
fails the same way with a worse message. The three reads (`Status`,
`Enabled`, `Masked`) additionally fall through on a D-Bus read error, so
a malformed unit name behaves exactly as it did before this change.

#### What was demonstrated

Live on this project's Ubuntu 24.04 host, real systemd 255,
`HALITE_SYSTEM_LIVE`: a throwaway unit in `/run/systemd/system`,
attached to nothing, started / stopped / restarted / enabled / disabled
/ masked / unmasked through the module, each step checked against
`systemctl show` and `systemctl is-enabled` run directly rather than
against the module's own read-back. `service.get_all` reads the
unit-file list over D-Bus. The `systemctl` fallback drives the same unit
when the dial is forced to fail. Alongside, `internal/dbus` carries
marshalling round-trips, a hand-computed `Hello` byte layout, a variant
unwrap, and a full handshake-and-call over `net.Pipe`.

#### Where the gate stands

`service` moves from `captured` to `hardware`. It was `captured` on the
strength of "halite's own units run under a real systemd" and the
Windows provider's reads -- nothing had watched this module start or
stop anything. Now the systemd branch has been driven end to end over
its real API. Uncovered: the launchd, sysvinit and openrc providers,
which have not been run at all, and the FreeBSD rc branch, which still
only reads. The release gate is unchanged -- `service` was never on it
-- and the CI `linux` leg gains the live `service` test for free, safe
there because a GitHub runner has systemd and the probe unit confines
nothing.

### 5.40 `pkg`: five more functions, apt

§3 had `pkg` at 18 of SPEC 15.2's 26. Five of the eight absent were
work not done rather than choices: `info_installed`, `file_dict`,
`download`, `list_downloaded`, `autoremove`. A tree migrating from Salt
that called any of them got "unknown module function".

They are implemented in the **apt provider**, each behind an optional
interface beside `pkgProvider` -- `pkgInspector`, `pkgDownloader`,
`pkgAutoremover` -- the way holding, world upgrade and file ownership
already were, so a non-apt provider is refused by name rather than
answering with an empty map. `file_dict` needs no interface: it is
`file_list` over several packages, so the module loops the existing
`pkgOwner`.

#### What was verified, and where

Live on this Ubuntu 24.04 host, `HALITE_SYSTEM_LIVE`:

- `info_installed` for real packages, every field checked against
  `dpkg-query -W -f=` run directly -- not the module's own read-back.
  Records whose dpkg status is not `installed` (a package in
  `config-files`) are dropped, because a tree asking what is installed
  does not want them.
- `file_dict` for two packages, cross-checked against `dpkg -L`.
- `download` of a small real package into a redirected archive
  directory, then `list_downloaded` reading it back with the version
  `dpkg-deb` reports from the file itself. The archive path is a
  variable so the test writes nowhere near `/var/cache/apt`.

`autoremove` really removes packages, so it is not run on the host the
work is done on. Its live leg is in the fleet container
(`HALITE_FLEET_LIVE`), which is Debian with real apt and is thrown away:
a leaf package and an autoremovable dependency are installed, then
`pkg.autoremove` is asked to clear the dependency and the change map is
checked against `dpkg`. In `--test` mode it parses `apt-get autoremove
--simulate` and changes nothing.

`mod_repo` and `del_repo` stay absent: the `pkgrepo` module already
does that job and is verified (5.35), so a second spelling on `pkg` is a
decision for a person, not a gap.

### 5.41 `mac_defaults`: the first macOS module

SPEC 15.3's macOS row had eight modules and this build had none of them
as functions — `mac_brew_pkg` and `mac_service` are aliases onto the
virtual `pkg` and `service`, and the other six were pending by name.
`mac_defaults` is the first to arrive, and it is the one that row shares
with SPEC 15.5's core state list, so building it closes a row in two
tables rather than one. It ships `mac_defaults.read`, `read_type`,
`write` and `delete` as execution functions and `mac_defaults.write`
and `mac_defaults.absent` as states.

**It drives `defaults(1)` and touches no plist directly.** The files
under `Library/Preferences` are a cache `cfprefsd` owns; a process that
edits one behind the daemon's back has its write dropped at the next
flush. This is the same shape as `pkg` and `service` — the subsystem is
a program, and the module is a careful client of it.

**Reading goes through `defaults export`, not `defaults read`.**
`defaults read <domain> <key>` prints a value in a display format that
cannot be parsed back without guessing: a boolean is `1`, a float's
precision is trimmed, an array is a parenthesised list. `defaults
export <domain> -` writes a real XML property list, and exits 0 with an
empty dict for a domain that does not exist rather than failing. A
minimal reader for that plist — dict, array, string, integer, real,
true, false — is in the module, because the alternative was shelling
`plutil` or taking a plist library, and the element set `defaults`
emits is small and fixed.

**The comparison is typed.** `defaults` keeps `-int 1`, `-bool 1` and
`-string 1` as three different things, and a state that wrote a string
where the tree asked for an integer would report the key converged
while a program reading it as a number found nothing. The reader returns
`int64` for `<integer>` and `float64` for `<real>`, the wanted value is
produced in the same Go type the declared `vtype` implies, and a
mismatch of kind rewrites — so the run after a type change still
converges.

**`user` becomes that account.** A preference domain is per-user,
resolved from `$HOME`, so managing a real user's Finder or Dock setting
means running `defaults` as them. The parameter maps to the command's
`RunAs`, which is setuid/setgid with the account's full group set and
its `HOME` — not `su -c` and not `sudo`.

#### What was verified, and where

`live_mac_defaults_test.go` drives the real `defaults` against a
domain named `com.halite.selftest.<pid>` in the invoking user's own
store, and a cleanup removes it. No root: a user domain does not need
it, and it is the only path a Mac CI runner could ever take. It checks
that the reader agrees with what `defaults export` actually writes for
each scalar type, an array and a nested dict; that a second `write`
with the same value is the no-op the state reports as converged; that a
string-to-int change on one key is seen as a change; and that `absent`
removes a key and is then converged.

It is gated on `HALITE_SYSTEM_LIVE=1`, the switch the hostname and
sysctl live tests use, because it writes to a real preferences store
even in a private corner of one. No CI leg sets it on a Mac, so nothing
watches this module converge unattended: `evidence.go` records it
`assumed`, and `make release-gate` is red on it alongside `apparmor`
and `snap`. The `user` path has not been run at all.

### 5.42 `mac_power`: `pmset`, per power source

The second module of SPEC 15.3's macOS row. It drives `pmset(8)` and
ships the getter/setter pairs Salt's `mac_power` has, so a tree calling
`mac_power.set_display_sleep` keeps working: `computer_sleep`,
`display_sleep`, `harddisk_sleep`, `wake_on_network`, `wake_on_modem`,
`restart_power_failure` and `sleep_on_power_button`, plus Salt's
combined `get_sleep`/`set_sleep` over the three timers — sixteen
functions. SPEC 15.5 names no `mac_power` state, so there is none, the
same as `win_registry`; a tree that converges a power setting reaches
these through `module.run`.

**Reading is from `pmset -g custom`, not `pmset -g`.** `-g` prints the
settings *in use*, which a `caffeinate` assertion changes; `-g custom`
prints what is *configured*, per power source, which is what a state
would converge against. A getter takes a `power_source` — `ac`,
`battery` or `ups` — so a laptop whose profiles differ can be asked
about either, and a desktop's missing `battery` section falls back to
`ac` rather than failing. The setters run `pmset -a`, every source,
matching Salt.

**One label is not its key.** `pmset` takes `powerbutton` and prints it
back as `Sleep On Power Button`; the setting table carries both. A
setting `pmset` does not report on a given Mac — `ring` on a machine
with no modem — is an error from the getter that names it, not a false
zero. Timer values are 0 to 180 minutes, with `Never` and `Off` for 0;
flag values take `on`/`off`, `yes`/`no`, `true`/`false` or `1`/`0`, and
`value.Truthy` is deliberately not used for them because it reads `off`
as true.

**Which settings a Mac reports is hardware.** The three sleep timers are
everywhere. `womp` and `autorestart` are not: the CI runner — an Apple
Silicon macOS 15 VM — reports neither, and the first live test to
require them failed there rather than on any code fault. What is
universal is the three timers and the shape of the output; the flags
are whatever the machine has.

#### What was verified

`live_mac_power_test.go` runs the getters against the real `pmset` on
whatever Mac runs the suite — no gate, because `pmset -g custom` reads
and changes nothing, and this makes it the one live macOS leg CI
actually runs. It requires the `AC Power` section and the three sleep
timers as `0`–`180` integers, and for each optional flag it checks
against what `pmset -g custom` reports: present, the getter returns a
bool; absent, the getter fails rather than inventing one. `get_sleep`'s
keys are asserted to be Salt's.

The setters are not exercised: `pmset -a` needs root and rewrites a real
Mac's power policy, and no CI leg runs as root on a Mac. `evidence.go`
records the module `assumed` for that reason, and `make release-gate`
is red on it.

### 5.43 `mac_user`, `mac_group`, `mac_shadow`: accounts on a Mac

SPEC 15.3's macOS account row, and the change that gives `user.present`
and `group.present` something to reach on a Mac — the same "nothing to
reach" this document records for Windows in 2.3, closed on the platform
that has the hardware.

**Why the account tool does not stretch.** `user.go` models an account
tool as one binary and one argument vector per operation — `pw usermod
-n x -s /bin/sh`, `useradd -m x`. macOS keeps accounts in Open
Directory, and `dscl . -create` writes one attribute per call, so
creating a user is six of them in sequence. The `accountTool` struct
cannot express that, and bending it to would change `freebsdTool` and
`linuxTool` and their tests for a third platform's benefit. Instead the
five state entry points — `user.present`, `user.absent`,
`group.present`, `group.absent`, `user.chgroups` — branch on `darwin`
to helpers in `mac_user.go`, and the `useradd`/`pw` path is untouched.
`userInfo` through `os/user` already worked on a Mac, so only the writes
are new.

**Reads reuse mac_defaults' plist reader.** `dscl -plist . -read` emits
the same Apple XML plist `defaults export` does; `dscl -read` without
`-plist` wraps a multi-word value onto a continuation line and cannot be
told from two values. A record that is not there exits 56 with
`eDSRecordNotFound`, which `c.Run` would turn into an error, so every
directory call sets `IgnoreExitCode` and inspects the code itself —
that was the first bug the live test found.

**Groups go through `dseditgroup`.** `dseditgroup -o create -i <gid>`,
`-o delete`, `-o edit -a/-d <user> -t user`. `user.chgroups` without
`append` removes membership of a group not in the list; `dscl -search
/Groups GroupMembership <user>` is how the current set is read.

**No password hash.** macOS stores a SALTED-SHA512-PBKDF2 dictionary in
a binary plist in Open Directory; there is no `/etc/shadow` line and no
portable hash a tree can carry. `user.present` refuses a `password:` on
darwin, pointing at `mac_shadow.set_password`, which takes a plaintext
and runs `dscl . -passwd` — as Salt's does, and with the same cost: the
plaintext is briefly in the process table, because macOS provides no
standard-input path for it. `mac_shadow.info` can report whether a hash
is present but never compare one; dscl answers "No such key" for
`ShadowHashData` whether or not one is there.

#### What was verified

`live_mac_user_test.go` runs the read side against the real `dscl` on
whatever Mac runs the suite — no gate, since it only reads. It parses a
real `dscl -plist . -read` for the running user and checks the uid
against `os/user`, reads `staff` back with gid 20, lists `/Users`,
confirms a missing account reads as absent rather than erroring, and
runs `user.present` in test mode against a name that is not there and
gets a predicted creation with no command run.

The writes are not exercised: the `dscl . -create` sequence,
`dseditgroup`, `createhomedir`, `dscl . -passwd` and the guarded
recursive home removal all need root and change Open Directory, and no
CI leg runs as root on a Mac. `evidence.go` records all three modules
`assumed`, and `make release-gate` is red on them.

### 5.44 `mac_softwareupdate`: `softwareupdate`, minus what macOS removed

SPEC 15.3's sixth macOS module. It drives `softwareupdate(8)`:
`list_available`, `update_available`, `list_downloads`, `download`,
`download_all`, `update`, `update_all`, `schedule_enabled` and
`schedule_enable` — twelve functions with the three below.

**Three of Salt's functions describe a mechanism macOS took out.**
`softwareupdate --ignore`, `--reset-ignored` and the per-update ignore
list were deprecated years ago and are gone from the binary on current
macOS — `softwareupdate --ignore` answers "unrecognized option".
Withholding an update is an MDM control now, through a
`com.apple.SoftwareUpdate` configuration profile, which is outside what
this module drives. `ignore`, `list_ignored` and `reset_ignored` are
registered anyway, and refuse by name with that explanation, so a tree
carrying them from Salt is not told "unknown function" — the same
choice `apt_key` and `win_registry`'s absent state make.

**The check schedule is a preference, not a subcommand.** `softwareupdate
--schedule on|off` is also gone; `--schedule` with no argument still
prints the state, which `schedule_enabled` reads. `schedule_enable`
writes `AutomaticCheckEnabled` under
`/Library/Preferences/com.apple.SoftwareUpdate`, through the same path
`mac_defaults` writes. `list_downloads` reads the `ProductPaths` keys
out of `/Library/Updates/index.plist` with the plist reader
`mac_defaults` already carries.

#### What was verified

`live_mac_softwareupdate_test.go` runs the two safe reads against the
real tool on any Mac: `softwareupdate --schedule` parses to a bool, and
the `/Library/Updates` index reads without erroring. `--list` is left
out on purpose — it contacts Apple's update service, takes tens of
seconds, and fails with no network, none of which belongs in the
default suite; its parser is covered by a fixture in both the current
and the older output form.

The install and download paths are not exercised: `softwareupdate
--install` needs root, reboots the machine, and no CI leg is a Mac.
`evidence.go` records the module `assumed`, and `make release-gate` is
red on it.

### 5.45 `mac_keychain`: `security`, and the passphrase in the process table

SPEC 15.3's seventh macOS module, and Salt's `keychain`. It drives
`security(1)`: `list_keychains`, `default_keychain`, `list_certs`,
`get_hash`, `friendly_name`, `install` and `uninstall` — seven
functions. No state; a tree that wants a certificate present uses
`module.run` with `unless: mac_keychain.get_hash ...`.

**`security import` takes the passphrase in the argument vector.** `-P
<passphrase>` is the only form — there is no standard-input path — so
for as long as the import runs, the PKCS#12 file's passphrase is
readable in the process table by any account on the machine. That is
`security`'s design, and it is the same shape `mac_shadow.set_password`
and Salt's own `keychain.install` carry; the module's doc comment says
so, and a tree that cannot accept it installs the certificate out of
band. `friendly_name` shells to `openssl pkcs12 -passin pass:...` and
has the same exposure.

**Names come from `labl`.** `security find-certificate -a -Z` prints a
`SHA-1 hash:` line before each certificate's attribute block and the
name as `"labl"<blob>="..."`, or `0x...` hex when it is not plain
ASCII; both are read, and the hash is paired with the name that
follows it. `list_certs` sorts and de-duplicates, because a keychain
routinely holds a leaf and its issuer under related names.

#### What was verified

`live_mac_keychain_test.go` runs the reads against the real `security`
on any Mac: the search list and default keychain come back as file
paths, `find-certificate -a -Z` on the System keychain — which every
Mac has, populated — parses into name/hash pairs whose hashes are 40
hex digits, and a by-name lookup finds the same hash the bulk read did.

`import` and `delete-certificate` change a keychain and need root for a
system one, so nothing has watched a certificate go in or out.
`evidence.go` records the module `assumed`, and `make release-gate` is
red on it.

### 5.46 `mac_assistive`: `TCC.db`, and the schema Salt's module missed

SPEC 15.3's eighth macOS module, the one that finishes the row, and
Salt's `assistive`. It manages which applications may drive the machine
through the Accessibility API — the list System Settings shows under
Privacy & Security ▸ Accessibility. Six functions: `list`, `installed`,
`enabled`, `install`, `enable` and `remove`. No state; SPEC 15.5 names
none, and a tree reaches `install` from `module.run` with `unless:
mac_assistive.enabled ...`.

**It writes `TCC.db` directly, and that database is SIP-protected.** The
grants live in the `access` table of `/Library/Application
Support/com.apple.TCC/TCC.db`, keyed by `kTCCServiceAccessibility`.
There is no supported tool that edits it — `tccutil` only resets — so
this module does what Salt's does and drives `sqlite3(1)` against the
file. On a modern macOS that database is readonly for any process
without Full Disk Access, root included, and `sqlite3` reports that as
"attempt to write a readonly database" or "unable to open database
file". `install`, `enable` and `remove` translate either into an error
that names Full Disk Access, because the underlying message does not. A
tree that runs this has granted the agent binary Full Disk Access out of
band; the module cannot grant it to itself.

**It targets the modern schema, where Salt's does not.** Salt's
`assistive` still queries an `allowed` column and writes `1` or `0` to
it. That column was renamed `auth_value` in macOS 10.15, and it holds an
enumeration rather than a flag: `0` is denied, `2` is allowed. Salt's
module therefore reads nothing on any macOS from Catalina on. This one
reads and writes `auth_value` with the `2` / `0` meaning and records the
`auth_reason` 4 / `auth_version` 1 pair that System Settings itself
writes for a hand-set grant, so it works on the versions Salt's does not
and does not work on the ones predating the rename — all long out of
support. `client_type` is inferred from a leading `/`: a path to a
binary is type 1, a bundle identifier type 0. Salt branches on a `.app`
suffix, which neither form has.

#### What was verified

`live_mac_assistive_test.go` reads this host's real `access` table
through the real `sqlite3`: the rows parse into client / client_type /
`auth_value` triples on the live schema, `client_type` agrees with
whether the client is a path, and `installed` / `enabled` agree with a
row the bulk read returned.

`install`, `enable` and `remove` write a database SIP holds readonly, so
nothing has watched a grant be added or removed. `evidence.go` records
the module `assumed`, and `make release-gate` is red on it.

### 5.47 `pam`: two include mechanisms, and a chain that is never one file

SPEC 15.3's Common Linux row, first of three. Seven functions —
`list_services`, `read_file`, `rules`, `has_module`, `services_using`,
`set_module` and `remove_module` — declared for Linux, the BSDs and
macOS rather than for Linux alone. The row's name is a filing decision;
whether a FreeBSD node can be asked what authenticates a login on it is
not.

**A PAM service is almost never one file, and the two platforms pull in
the others differently.** FreeBSD's `su` is four rules and an `auth
include system`; Debian's `sshd` is mostly `@include` of the four
`common-*` files. These are different mechanisms rather than spellings
of one: the typed `include` contributes *one* chain, and `@include`
takes no type and contributes all four. A resolver that knew only the
first would report Debian's sshd as having almost no rules; one that
knew only the second would report FreeBSD's `su` as running
`pam_lastlog` at session time, which it does not. `pam.rules` follows
both, keeps the file and line each rule came from, and breaks a cycle
by keeping the offending include as written so the answer still shows
where the loop is.

**The bracketed control flag contains a space.** Linux-PAM's
`[success=1 default=ignore]` is one field with a space in it, so
`strings.Fields` on a Debian `common-auth` files `default=ignore]` as
the module path — and the module path is what every other function here
matches on, so the rule becomes invisible to `has_module` and to the
sweep. On Debian 12 that rule *is* the auth chain. The reader keeps the
brackets whole and refuses an unclosed one rather than swallowing the
rest of the line.

**Nothing here converges.** There is deliberately no `pam` state: SPEC
15.5 names none, and what a tree usually wants to assert about PAM is
the whole file, which `file.managed` already does with a template and a
backup. The two mutating functions are narrow on purpose — the caller
names the type, the module and the position, a new rule is placed
*within its own chain* rather than at the end of the file, the write is
atomic, and the result is re-read through the same parser before the
call returns. `remove_module` refuses to empty a chain, because PAM
denies a request whose chain has no rules and that is how a node stops
authenticating anybody, including whoever would repair it.

#### What was verified

`live_pam_test.go` reads *every* service the machine running the tests
has, and checks the parse against the file rather than against an
expectation. Two invariants, neither of which needs a fixture: every
line PAM would act on becomes exactly one rule, and every control flag
read off the machine is one PAM accepts. On the FreeBSD host this
project is developed on that is 13 real services and 53 real control
flags. A third check holds the sweep and the per-service answer to each
other on the same machine.

Both invariants were broken on purpose before being believed. A parser
that quietly dropped the `include` lines — the plausible slip, since
they are not modules — was caught by the line count on three of this
host's own files.

Run since on a real Ubuntu 24.04 host, the other mechanism this section
opened with: 33 real services, 191 real control flags, and 420 rules
that arrived through Debian's untyped `@include` fan-out rather than
FreeBSD's typed one — an order of magnitude more include resolution
than the FreeBSD corpus exercises, on the platform the mechanism
differs on.

Nothing has watched this module write to a real /etc/pam.d, and nothing
will: the mutating half runs against a throwaway tree and against
nothing else. `evidence.go` records the module `captured` with that
said in full.

### 5.48 `quota`: two tools, and a report that cannot be parsed on one platform

SPEC 15.3's Common Linux row, second of three. Six functions —
`report`, `get_mode`, `stats`, `set`, `on` and `off`.

**Linux's fixed-width report is ambiguous, and this build refuses it
rather than guessing.** quota-tools leaves a grace column *blank* when
nothing is over its soft limit, so a row carries between six and eight
whitespace-separated numbers depending on the state of the filesystem —
and a grace is not always distinguishable from a number, because a grace
period of under an hour prints as a bare count of minutes. Seven fields
after the status characters is therefore genuinely ambiguous: either the
block grace is running and the file grace is not, or the reverse, and
nothing in the row says which. Reading it anyway would file one
account's inode count as another's block limit. Linux is read through
`repquota -O csv`, which quota-tools grew for exactly this, and a node
whose tools are too old to have it is told so.

The BSDs need no such thing and are read from the report:
`usr.sbin/repquota/repquota.c` prints `-` in a grace column that is not
running, so a row always has ten fields. The parser is written to that
file's `printf` calls, read off /usr/src on this project's own host,
rather than to remembered output.

**The two tools take the limits in a different order with a different
separator.** Linux has `setquota -u alice 1024 2048 100 200 /home`;
FreeBSD has no `setquota` at all and spells it `edquota -u -e
/home:1024:2048:100:200 alice`. Three chances to write one platform's
form and have it look right, and the failure is silent: both tools
accept a transposed vector and set an inode limit as a block limit. The
argument vector is a table keyed by platform and every row of it is
checked from any host, which is plan.md §1.4's lesson applied before it
cost anything.

**A ZFS filesystem is not an unquota'd filesystem.** ZFS quotas are
dataset properties and `repquota` on one reports nothing at all.
Reporting that as "this filesystem has no quotas" would be false on this
project's own fleet, which is entirely ZFS — 55 of this host's 62
mounted filesystems. `report`, `get_mode` and `set` say so by name and
point at `zfs.get` for `userquota@<name>`.

#### What was verified, and what has not been

The ZFS diversion is checked against this host's real mount table, and
the transposition was introduced on purpose and caught by the platform
table.

**No `repquota` has been run against a filesystem that has quotas, and
no quota has been set.** There is no such filesystem on this fleet.
`live_quota_loopback_test.go` is written to close that — an ext4
filesystem in a file, mounted through the loop driver with quotas on, a
limit set through `setquota` and read back through `repquota` — and it
is wired into the Linux leg of `fleet.yml`.

**It took six CI runs to reach its first assertion, and five of those
were about the machine rather than about the module.** That is the part
worth writing down.

The first two stopped at `quotaon` with ESRCH — `No such process` —
against a filesystem that was mounted and whose quota files `quotacheck`
had just written. The first diagnosis was wrong: ext4's *quota feature*
was blamed on the strength of the symptom alone, and the next run
printed the feature list and showed the filesystem had never had it.

Only when the leg was made to **probe instead of guess** did the machine
say what was actually wrong:

```
modprobe: FATAL: Module quota_v2 not found in directory
          /lib/modules/6.17.0-1022-azure
quotaon: Quota format not supported in kernel.
```

**ext4 has two quota mechanisms and both need the same format driver.**
The classic mechanism keeps `aquota.user` and `aquota.group` in the
filesystem root and is switched on by `quotaon`; the *quota feature*
keeps the same data in hidden inodes and is on from the moment the
filesystem is mounted. Neither works without `quota_v2` — the fifth run
established that by trying the feature route and having `mount(2)` fail
with the same ESRCH one layer earlier, because `usrquota` asks for the
classic format at mount time.

And the driver was not a missing kernel feature at all. It was a missing
**file**: `CONFIG_QFMT_V2=m`, with the module in `linux-modules-extra`
and absent from the runner image. One `apt-get install` and the leg went
green on the first try.

#### What is verified now

`setquota` set limits on a real ext4 filesystem with quotas switched on,
and the real `repquota -O csv` read all four back in the right
positions — four distinct numbers in four positions, so a transposed
pair would have shown as a wrong value rather than as two that match.
`quotaon -p` was read in both states. `evidence.go` records the module
`hardware` and the release gate is no longer red on it.

**The BSD parser has still never run.** It is written to the `printf`
calls in FreeBSD 15.1's own `usr.sbin/repquota/repquota.c` rather than
to remembered output, which is better than a fixture and is not a
demonstration: this project's fleet is entirely ZFS and has no UFS
filesystem to make one on, so `edquota -e` is checked only as an
argument vector. Also uncovered: ext4's quota feature route, which the
leg falls back to and no runner has needed.

#### The lesson, which was cheaper than the six runs

Three things had been one, and they are separate now: the test
**existing** is not the demonstration, the test **running** is not
either, and only the test **reaching its assertions** is. Five of these
runs produced a green Fleet job with a skipping test inside it — which
looks like progress in a log and is not.

The module gained the part that outlives the leg. Both of the kernel's
answers name a cause an operator cannot act on, so `quota.on`,
`quota.off` and `quota.get_mode` translate them, and a test pins that an
unrelated failure is not given the same explanation.

### 5.49 `openssl_cert`: the four things crypto/x509 will not do

SPEC 15.3's Common Linux row, third of three. Five functions —
`version`, `verify`, `crl_info`, `pkcs12_info` and `pkcs12_create` —
and none of them duplicates `x509`, which stays this build's
certificate module and needs no external program at all.

What it adds is the four things the standard library will not do:
chain verification against the machine's own trust store, which is how
an expiring internal CA is found; PKCS#12, which Go reads only through
a third-party package and this build has no dependencies; revocation
lists, where crypto/x509 will parse one but will not say when it was
issued or what is on it; and reporting which openssl the node actually
has, because LibreSSL is a different program with the same name and no
`-show_chain`.

**The passphrase never reaches the argument vector.** Every account on a
node can read another's command line, so `-passin pass:secret` publishes
the passphrase of the bundle it is opening for as long as the process
runs, and `-passin env:VAR` does the same through /proc. Both password
paths pass `stdin` and write down the pipe. A test sweeps the module's
own source for the other three forms, because that is the only way to
assert about a spelling that must not appear.

**An unreadable trust file is not an untrusted certificate.** That was a
defect, found by the live test on its first run: an empty `-CAfile`
makes openssl exit 1 without verifying anything, and the module was
reporting it as `verified: false` — which would send an operator to
renew a certificate that is fine. It is now a refusal that says which of
the two happened.

`-crl_check_all` rather than `-crl_check`, because checking only the
leaf leaves a revoked intermediate trusted, and an intermediate is what
gets revoked when a CA is compromised.

#### What was verified

`live_openssl_cert_test.go` drives the real openssl end to end, and it
needs no gate and no container: this module changes exactly one thing, a
file it is told to write, and everything happens inside `t.TempDir()`
with no root and no network. So the *mutating* path is demonstrated
rather than assumed — a real PKCS#12 bundle written by this module and
read back through the tool that made it, with the wrong passphrase shown
to fail, which is what establishes that the right one is really being
delivered on standard input rather than quietly ignored. A real
revocation list is generated and read, and a chain is verified and then
refused against a different CA with openssl's own numbered reason read
back.

On OpenSSL 3.5.6 / FreeBSD 15.1. `evidence.go` records the module
`hardware`. Not covered: LibreSSL, whose `verify` has no `-show_chain`
and whose wording is its own, and OpenSSL 1.1.1, whose spelling is in
the fixtures and on no machine here.

### 5.50 `lvm`: the JSON report, and the two things it will not do

SPEC 15.3's Common Linux row, fourth of eight and the first of the
three plan §7.12 named as worth taking next — each closes a 15.3 module
*and* a 15.5 state. Twelve execution functions (`version`, `pvs`,
`vgs`, `lvs`, `pvcreate`, `pvremove`, `vgcreate`, `vgextend`,
`vgremove`, `lvcreate`, `lvresize`, `lvremove`) and six states
(`pv_present`, `pv_absent`, `vg_present`, `vg_absent`, `lv_present`,
`lv_absent`).

**The reports are read as JSON, not as the padded table.** `pvs`, `vgs`
and `lvs` print a table for a person by default, and a column's width
is whatever the widest value on the machine happened to be — a
`vg_name` with a space in it, which LVM allows, tears a
`strings.Fields` row in half, and a long device path pushes every field
after it left by one. That is the shape 5.31 found in `pf`. LVM2 has
had `--reportformat json` since 2.02.107 (2014), which every targeted
distribution is far past, and asked with `--units b --nosuffix` every
size comes back as an exact byte count with no `1.50g` to read as one
and a half. `jail` made the same call against `jls --libxo=json` (5.32),
and SPEC 15.3's `journald` row asks for it in as many words.

**It grows and extends; it does not shrink.** `vg_present` adds a named
device a group does not span yet and never removes one — removing a
disk from a volume group moves whatever data is on it, which is not a
thing a state should do as a side effect of a list that got shorter.
`lv_present` grows a logical volume to a larger declared size and
refuses to shrink one: a smaller size is reported as a warning and left
alone, because a shrink that outruns the filesystem on top of it
destroys data. `lvm.lvresize` is the deliberate path for a shrink, and
it refuses one too unless the call passes `force` *and* names the
smaller absolute size — the guard is in this build, before LVM is
invoked, not left to LVM's own prompt. This is the stance `zpool.present`
takes: it "does not reshape an existing pool, and reports a layout that
does not match as a warning instead".

**Every function needs root, the reads included.** A non-root `pvs`
prints a warning and an empty report rather than an error, and an empty
report read as "this node has no volume groups" is a false answer to
give about a node whose root filesystem is on LVM. So the reads are
declared `root` alongside the writes rather than left to hand back a
hollow answer.

#### What was verified

The `pvs`/`vgs`/`lvs` JSON parser is checked against real reports
captured from LVM2 2.03 on Ubuntu 24.04, and every argument vector is
pinned field by field in `lvm_test.go` — the fix plan §1.4 drew out of
two fixtures that had each forced a branch their platform does not take.

The **mutating** half is demonstrated. `live_lvm_loopback_test.go`
drives the real `pvcreate`, `vgcreate`, `vgextend`, `lvcreate`,
`lvresize` and `lvremove` against two loopback block devices backed by
files in `t.TempDir()` — nothing the machine came with is touched —
and it ran on this project's own Ubuntu 24.04 host (kernel 6.18)
against LVM2 2.03: a group built from one device and grown onto the
other, a volume carved and grown, the shrink guard shown to refuse a
smaller size *before* `lvresize` was called, and the whole stack torn
down through `lvremove` and the `vg_absent`/`pv_absent` states. Every
step was checked against a fresh `pvs`/`vgs`/`lvs` read rather than the
module's own answer. It needs root and the loop driver, so it is gated
behind `HALITE_SYSTEM_LIVE=1` and also runs in the fleet workflow's
linux leg, the same gate as `quota` (5.48). `evidence.go` records `lvm`
`hardware`.

That first run found a defect — in the harness, not the module. The
live helper passed the device list as a Go `[]string`, and the
signature's List coercion wraps a non-`[]any` value into a one-item
list, so `pvcreate` was handed the single argument `[/dev/loop4
/dev/loop7]` and exited 5. Real arguments arrive from YAML as `[]any`
and never hit this; the helper now converts. Not covered: thin pools
and thin volumes (the argument vectors are pinned but nothing has built
one), striping, and any filesystem on a volume — so `--resizefs` is
still checked only as an argument.

### 5.51 `iptables` and `nftables`: the two layers under `firewall`

SPEC 15.3's Common Linux row, modules five and six, and SPEC 15.5's
`iptables` and `nftables` states — building the module and its state
was one piece of work, as §2.2 said it would be. Thirteen execution
functions each; seven `iptables` states and nine `nftables` states.

**Neither is a `firewall` provider, and that is a decision.** `firewall`
is the virtual module whose Linux provider is `ufw`, which is itself a
front end for one of these two. `iptables` and `nftables` are the layer
under that — the escape hatch a tree reaches for when it needs a `nat`
rule, a `mark`, or a jump to a hand-built chain that `ufw` has no
spelling for. Wiring either as a second `firewall` provider would make
`firewall.allow` ambiguous on a node that has both, and would put the
abstraction back over the tool whose reason to exist here is that the
abstraction ran out. §7's prediction that these would *reshape* the
provider interface came from the same place as the `pf` one, and `pf`
reshaped nothing (5.31); the relationship here is `dpkg` to `pkg`.

**Idempotence is where the two tools diverge.** `iptables -C` asks the
kernel whether a rule is already present, using iptables' own matching —
so `append` and `delete` put the question to the tool rather than
re-deriving it from the text of `iptables-save`, which is only read to
*report*. `nft` has no `-C`. So an `nftables`-managed rule **must carry
a comment**, and that comment is its identity: `append` adds the rule
unless a rule in the chain already has that comment, and `delete` finds
it by comment and removes it by handle. A change to a rule's body under
an unchanged comment is deliberately **not** applied — re-declare with a
new comment. This is stricter than Salt, which appends every time, and
it is the cost of idempotence on a tool that does not offer it. The
`nftables` states refuse to run without a comment and say why.

**Both refuse the destructive scopes without `force`.** Flushing a
built-in / base chain whose policy is `DROP`/`drop` strips the rules
that were letting traffic through, and the node goes off the network
mid-run; a whole-table flush, or `nft flush ruleset`, empties chains
this build never wrote. Each is refused unless the call passes `force`.

**Reading is structured; writing avoids the argument vector where it
bites.** `nftables` reads `nft -j list` — a JSON document, never the
indented reprint that DIVERGENCE 5.31 was about — and *writes* by
feeding nft's own syntax to `nft -f -` on standard input, because nft
re-tokenises its argv and a colon in a comment then breaks its lexer.
The rule body is passed through untouched: reassembling nft's expression
grammar would be a second parser for the most intricate surface either
firewall has.

#### What was verified

Both mutating paths ran against the real tools — `iptables` 1.8.10
(nf_tables backend) and `nft` 1.0.9 — inside a **throwaway network
namespace**, so nothing touched the host firewall. The test re-executes
itself under `unshare --net --map-root-user`, which needs no privilege
on a kernel with unprivileged user namespaces (Debian and Ubuntu since
about 2023); where the kernel forbids one, the test skips rather than
falling back to the host's real ruleset. So `live_iptables_test.go` and
`live_nftables_test.go` run in the ordinary suite, not behind a gate,
and both modules are `hardware` in `evidence.go`.

What ran: rules appended, inserted and deleted with the idempotence
each tool allows; user chains and nft tables created and removed;
built-in and base chain policies set and updated in place; `nftables.check`
against nft's own `--check`; the flush guards shown to refuse; and
`nftables.save` writing a self-contained restore script that flushes
first. Every result was checked against a fresh read of the tool.

Not covered: `ip6tables` and nft families other than `inet` (the
argument vectors are pinned, nothing has driven them), the
`nat`/`mangle`/`raw` tables, nft sets and maps, and `iptables.save`
against a real `iptables-persistent` layout.

### 5.52 `journald`: structured reads, and control over the varlink socket

SPEC 15.3's Common Linux row, module seven. Nine execution functions:
`query`, `fields`, `field_values`, `list_boots`, `disk_usage`,
`rotate`, `flush`, `sync`, `vacuum`. No state — SPEC 15.5 names none,
the way it names none for `pam`; a log query has nothing to converge,
and journald's settings live in journald.conf, which `file.managed`
already manages.

**The reads do not parse the human `journalctl`.** SPEC's row asks for
the journal "over a socket rather than by parsing `journalctl` output",
and the literal reading of that is not reachable without a dependency:
the journal has no cgo-free read API, its varlink socket does only
rotate/flush/sync, and the file format's data objects are LZ4/XZ/ZSTD
compressed — three decompressors this build has no dependency for. So a
`query` runs `journalctl -o json`, the same call `jail` makes to `jls
--libxo=json` (5.32): a documented one-object-per-line serialisation,
not the aligned `-o short` columns that SPEC's sentence is really
about. `fields` and `field_values` read `-N` and `-F`, which emit one
bare value per line. `query` carries the last entry's `__CURSOR` back
out, so a caller can poll from where it left off.

**The control verbs do go over the socket.** `rotate`, `flush` and
`sync` are `io.systemd.Journal.Rotate`, `FlushToVar` and `Synchronize`
on `/run/systemd/journal/io.systemd.journal`, spoken by a new
`internal/varlink` — a ~130-line client for the wire protocol
(one JSON object plus a NUL byte, each way), the same
"implement it rather than depend on it" call `internal/dbus` makes one
layer up. A `journalctl --rotate` / `--flush` / `--sync` fallback takes
over when the socket cannot be reached (older systemd, a namespace
without it), which is the shape the systemd `service` provider uses for
D-Bus (5.39). Which path ran is in the result's `via` field. A varlink
*error* reply — the service answered and refused — is a real failure
and does not fall back.

#### What was verified

Against real systemd 255 on Ubuntu 24.04. The reads ran in the ordinary
suite (`live_journald_test.go`, no gate, no root — `journalctl` shows a
non-root caller its own entries): `query` with a `_UID` match and its
cursor round-trip, `fields`, `field_values`, `list_boots` and
`disk_usage` all parsed field by field against the host's own journal.
The control verbs ran as root against the real varlink socket —
`sync`, `rotate` and `flush` each reported `via: varlink` — and the
`journalctl --sync` fallback was forced by pointing the socket path at
nothing and shown to take over. `internal/varlink` has its own tests
against an in-process fake service, covering the reply, the typed error
reply, an unreachable socket, and a context deadline.

Not covered: `vacuum`, which deletes archived journal files and no test
has been willing to run against a real machine; the varlink error-reply
path with a real service; and any systemd older than 255, whose varlink
interface may be absent — the fallback exists for that and has only
been exercised through a bad path.

### 5.53 `mdadm`: `--detail` and /proc/mdstat, and a `create` that refuses

SPEC 15.3's Common Linux and Storage rows, module eight. Thirteen
execution functions: `version`, `list`, `detail`, `examine`, `mdstat`,
`create`, `assemble`, `stop`, `add`, `fail`, `remove`, `grow`,
`save_config`. No state — SPEC 15.5 names one for `lvm`, `zfs` and
`zpool` and none for this, and that is right: building or reshaping an
array is a careful, one-time, destructive operation, not a target a
convergence loop re-checks. `pam` and `journald` have no state for the
same kind of reason.

**mdadm has no JSON, so `detail` parses the human report.** `--detail
--export` is a `KEY=value` list but carries no health — not degraded,
not which member failed. So `detail` reads `mdadm --detail`, whose
header is `Label : Value` with a closed label set (the split is on the
first colon, because a creation time carries its own) and whose member
table is fixed-column with the device path taken from the end, because
a `removed` slot has no path. This is a format, not the drifting `-o
short` shape 5.31 was about. `mdstat` reads `/proc/mdstat` directly for
the one thing `--detail` shows poorly: the resync, recovery and reshape
percentages, the way `zpool status` reads a scrub.

**`create` refuses without `force`.** `mdadm --create` overwrites the
member devices. The module refuses a device that is already an array,
and refuses a member whose `mdadm --examine` finds an md superblock —
it may belong to another array — unless `force` is set. `--run` is
always passed so mdadm does not stop on its interactive prompt.

#### What was verified

Against a real mdadm 4.3 on Ubuntu 24.04. `live_mdadm_test.go` builds a
RAID1 with a spare across three loop devices, then runs `fail` ->
`remove` -> `add` on a member with idempotence checked each way,
`save_config` (which wrote an ARRAY line while keeping a hand-added
MAILADDR line), and `stop` — every step checked against a fresh `mdadm
--detail` / `--examine` / `/proc/mdstat` read, and `create` shown to
refuse both an existing array and a member that already carries a
superblock. It needs root and the loop driver, so it is gated behind
`HALITE_SYSTEM_LIVE=1` and runs in the fleet workflow's linux leg, the
same as `lvm` and `quota`. `evidence.go` records `mdadm` `hardware`.

Not covered: `grow` (a reshape takes hours), `assemble --scan` (it
reads every superblock on the host), RAID levels other than 1, and
metadata 0.90.

### 5.54 `modprobe` and `udev`: the last two Common Linux modules that need no RHEL host

SPEC 15.3's Common Linux row, modules nine and ten. `modprobe`: ten
execution functions (`list`, `is_loaded`, `info`, `is_denylisted`,
`load`, `remove`, `persist_load`, `persist_remove`, `denylist`,
`allowlist`). `udev`: six (`version`, `info`, `list`, `trigger`,
`settle`, `reload_rules`). Neither has a state; SPEC 15.5 names one for
none of `pam`, `journald`, `mdadm` or these, and for `modprobe` in
particular that is a decision already on record — plan.md §6 item 2
lists Salt's own `kmod` state among the "modules SPEC never planned
for" that the estate's tree uses, and building one unasked here would
be answering that question by accident rather than on purpose.

**Both read a tool's own machine format, not its table.** `modprobe`
parses `/proc/modules` directly — name, size, use count, a comma list
of dependents or `-`, state, address, a fixed six fields — and
`modinfo`'s `label:\s*value` lines, where `alias` and `parm` commonly
repeat and are collected as lists rather than each overwriting the
last; a continuation line, such as the hex dump `signature:` wraps
across, carries no label and is dropped rather than glued onto the
wrong field. `udev` reads `udevadm info --export` (one device,
shell-quoted `KEY='value'`) and `--export-db` (every device, grouped
under `P:`/`N:`/`U:`/`S:`/`E:`) — udevadm's own documented modes, not
the column-aligned default DIVERGENCE 5.31 was about.

**`modprobe` persists in two files because the kernel keeps two ideas
of "this module matters".** Loading now is `modprobe`; loading at every
boot is a bare name in `/etc/modules-load.d/<name>.conf`, which is
systemd-modules-load.service's whole input format; refusing to load a
module at all is a *different* file,
`/etc/modprobe.d/denylist-<name>.conf` — the directive inside it is
still spelled the old way in every modprobe.conf(5), which is the one
place this module's own source quotes it rather than this project's
word for the idea, marked as a deliberate quotation. `allowlist`
removes only the file this module would have written — a stock denylist
file Debian ships under its own historical name is left alone, and
`allowlist` refuses by name rather than editing a file it did not
create.

**`udev.trigger` re-runs rules; it does not write them.** A rule file
is `file.managed`'s job. What `trigger` and `reload_rules` add is what
a file write does not do by itself: fire udev's rules against devices
already present, and tell the running daemon to re-read the rule files
before the next event.

#### What was verified

Both against a real kernel and a real udev (systemd 255) on Ubuntu
24.04. `modprobe.list`/`info`/`is_denylisted` read this host's own
loaded modules; the mutating half loaded and unloaded `netdevsim`, the
kernel's own simulated networking device for testing — it creates no
interface merely by loading — with idempotence checked on both load
and remove, then wrote and removed
its own modules-load.d and modprobe.d files in a redirected directory.
`udev.version`/`info`/`list` agree with each other on a device found by
one and queried by the other, and `trigger`/`reload_rules` ran as root
against a throwaway loop device and the real daemon. Both are
`hardware` in evidence.go; the mutating halves of each need root, so
`live_modprobe_test.go` and `live_udev_test.go`'s control tests are
gated behind `HALITE_SYSTEM_LIVE=1`, wired into the fleet workflow's
linux leg, while every read runs in the ordinary suite.

Not covered: a module with real dependents refusing `modprobe.remove`
(checked only against a fixture); `persist_load`'s options file
surviving an actual reboot; and a device `udev.trigger` actually
creates or removes, rather than one that already exists.

**`authselect` is left pending, not built from documentation.**
SPEC 15.3 files it under Common Linux, but authselect itself is
Fedora/RHEL 8+ only — Debian and Ubuntu manage PAM through
`pam-auth-update`, which `pam`'s own module already reads and writes.
This project's fleet, and every host this work has been able to reach,
is Debian/Ubuntu or FreeBSD; there is no RHEL machine to run authselect
against. Every other RHEL-only module in SPEC 15.3 — `yumpkg`,
`dnfpkg`, `rpm`, `firewalld`, `subscription_manager`, `dnf_module`,
`chattr` — is pending for the same reason, and shipping fixtures for a
tool nobody here has ever run would be exactly the mistake this
project's own evidence system exists to catch: DIVERGENCE 5.31 found a
fixture written in a module's own spelling that agreed with itself and
disagreed with the real tool. `authselect`'s entry in
`exec/platform.go` names this reason rather than "phase 5, with the
Linux platform work".

### 5.55 `apparmor`: closed on a machine whose own tools can read its own profiles

5.37 ranked three routes to close `apparmor` and named the first "an
afternoon on a host that has one" — a machine whose `apparmor-utils`
can parse the profile tree it ships. This project's own development
host turned out to be that machine.

The obstacle 5.37 found was never AppArmor, or this module, or even
`apparmor-utils` in general: it was one file, `abstractions/passt`,
shipped by Ubuntu's own `passt` package on the GitHub runner 5.37 used,
containing syntax the Python parser in apparmor-utils 4.0.1 cannot
read. That parser runs over *every* profile under `/etc/apparmor.d`
before doing anything, so the one file broke `aa-enforce`,
`aa-complain` and `aa-disable` against every profile on that machine,
`/usr/bin/man` included. This host has no `passt` package installed,
so it has no `abstractions/passt`, so the same `apparmor-utils` 4.0.1
that failed there works here — confirmed directly (`aa-complain
/usr/bin/man` and `aa-enforce /usr/bin/man` both succeed) before
trusting it with this module's own live test.

#### What was verified

`live_apparmor_test.go` was already written for exactly this run and
needed no change: `TestLiveAppArmorReadsWhatSecurityfsPrints` against
148 real profiles in all four modes, cross-checked against a direct
line count of `AppArmorProfilesPath` rather than through the module;
`TestLiveAppArmorMovesAProfileThroughEveryMode` loading a throwaway
profile that confines nothing, moving it complain → enforce by name
through the real `aa-complain`/`aa-enforce`, disabling it and
confirming both that securityfs shows no mode at all (not a mode
*called* `disable` — the distinction the module is most careful about)
and that the reboot-persistence symlink `aa-disable` leaves in
`/etc/apparmor.d/disable/` is really there, then unloading it;
`TestLiveAppArmorStateConvergesAndPredicts` showing `apparmor.mode`
predicts in test mode without touching the kernel, applies for real,
and reports no change on a second run; and
`TestLiveAppArmorRefusesAProfileThatIsNotThere` naming the missing
profile in its refusal. All four green, root, Ubuntu 24.04, real
`apparmor-utils` 4.0.1 and `apparmor_parser` 4.0.1.

`apparmor` moves to `hardware` (DIVERGENCE table, evidence.go). The
release gate drops from ten to nine — `snap` and the eight-strong macOS
row remain.

#### What this does and does not settle

This closes the module on the platform it was run on. It does not
touch the question 5.37 actually raised for a person to answer: a node
that *does* have `passt` — or any other package whose profile syntax
this Python parser cannot read — still has an `apparmor` module whose
`enforce`/`complain`/`disable` cannot work by any means, and
`apparmor.status`'s `tools_reason` is what tells an operator that
rather than a silent failure. Route 3 — teaching this module to change
a profile's persisted mode by editing the file directly, the way
`aa-complain` does, so it stops depending on `apparmor-utils` at all —
is still on the table and is still the larger undertaking 5.37 said it
was. It just is not the blocking question anymore: a module an operator
can run on a stock Ubuntu host, with a named and detected failure mode
on the hosts where it cannot, is a materially different thing to ship
than one that has never been run at all.

### 5.56 Chomping: the gap the table named was not the defect it had

Block scalar chomping had one row in the conformance table of 5.4,
`gapChomping`, and the reason beside it called it "the most damaging gap
in this table" because chomping is what `file.managed` contents is
written as. Two things were wrong with that row and one of them was a
real defect, so the order they came out in matters.

**The row pointed at the wrong thing.** Its case, 565N, is `!!binary`
over a literal block scalar. halite decodes a binary scalar to bytes,
which is what PyYAML does and what SPEC 10.1.3 asks for; the suite's
`in.json` keeps the base64 *source*, because JSON has no binary type,
and in 565N that source is wrapped over four lines. The comparison
re-encoded halite's bytes and compared the text, which works only where
the source is one line — so the two differed in every line break the
source had, and the trailing one got read as a chomping fault. The
comparison now decodes the suite's text instead, which is the right
equivalence for binary in both directions, and the case agrees. Nothing
in the parser changed.

**Chomping itself had never been measured.** It has five inputs — two
styles, three indicators, an optional indentation indicator, the shape
of the trailing lines, and whether the scalar is a mapping value, a
sequence entry or the whole document — and what existed was a handful
of cases in `parse_test.go` written from the specification. The matrix
in `internal/yaml/chomping_test.go` is the product instead: 126
documents, each in the PyYAML differential of 5.8 *and* in a captured
table, so the coverage holds on a machine with no Python and the
differential re-derives it where PyYAML is installed. Breaking the fix
on purpose fails 20 assertions across both halves, and fails the
captured half alone with the differential skipped, which is the check
that matters on a machine that has no reference to compare against.

**It found the defect on its first run.** Four documents: a block
scalar whose last line is not terminated — the file simply ends — came
back with a line break that is not in the file, under clip and under
keep. `contents: |` over a file with no final newline wrote a file with
one. That is a file that differs from the one the state describes, on
every run, in the direction nothing notices, and it is the same class as
the `--- |` defect 5.4 already records. The parser counted the trailing
breaks it was going to emit from the line *count*; it now counts the
breaks the file actually has.

**The suite and the reference implementations disagree about this, and
SPEC picks the implementations.** The YAML test suite expects the break
to be added at end of input — L24T/01 and JEF9/02 both assert it. PyYAML
does not add it, and neither does libyaml, which is a separate
implementation in a different language. SPEC 10.1 specifies PyYAML's
dialect, for the stated reason that it is the dialect every existing
Salt tree was written against, so the two suite cases move to the
deliberate side of the table as `specEndOfInput`. **This is why the
suite score went down while the parser got more correct**: 331 agreeing
became 330, and the gap count 37 became 36. A score that can only go up
is a score that is not measuring anything.

**And a fact about the reference that was being stated wrongly.** Six
documents in the matrix put a tab at the head of block scalar content.
Pure Python PyYAML reads them; libyaml refuses them as "a tab character
where an indentation space is expected". Salt takes
`getattr(yaml, "CSafeLoader", yaml.SafeLoader)`, so which one an estate
gets depends on whether libyaml is installed beside it — and the
differential's shaper carried a comment saying `safe_load` "is what Salt
uses", which is true only of the half that has no C extension. The
shaper stays pinned to pure Python, deliberately, so the comparison is
the same on every machine; the comment now says which implementation
that is and where they part.

Running the matrix through Salt's own loader on this host — Salt
3006.25, with libyaml present — agreed with the captured PyYAML answers
on 120 of the 126, the six differences being exactly the tab cases. That
was a run by hand and is not committed: `make saltdiff`'s container is
where a Salt comparison belongs, and it was not re-run for this.

### 5.57 SPEC 30: two rows measured, eleven tracked

SPEC section 30 opens by saying its targets come "with the measurement
method, so they can be tested rather than asserted". Until now every one
of the thirteen was asserted: `grep "func Benchmark"` over the tree
returned nothing, and no target was known to be met or missed.

`internal/perf` carries the table. Every row has a method and either the
benchmark that measures it or a note saying what it waits on, and
`TestTheTableIsSpecsOwn` holds the row names to SPEC.md's own table in
both directions, so a row renamed in the specification fails here rather
than quietly stopping being tracked.

Two rows name a benchmark as their own method and both are now measured:

| Row | Target | Measured here |
|---|---|---|
| Highstate compile, 500 states, 50 SLS files, heavy Jinja | under 2 s on the node | **96 ms**, 21 MB, 172,000 allocations |
| Pillar compile, 200 pillar SLS, cold | under 500 ms on the hub | **154 ms**, 38 MB, 187,000 allocations |

Measured on the FreeBSD development host, a Xeon E5-2620 v3 at 2.4 GHz,
Go 1.26.8, three runs of twenty; the spread between runs is under 4%.
Both targets are met, the first with twenty times the headroom and the
second with three.

The trees are generated to the shape SPEC names rather than vendored
from the estate, for a reason worth stating: a generated tree can be
*asserted*. `TestTheTreesHaveTheShapeSpecNames` checks that the
compilation really produced 500 chunks over 50 SLS files, that the macro
import ran, and that the loop over pillar ran — because a benchmark
cannot fail, and a generator that quietly stopped rendering would report
a very good number for compiling nothing. That is the lesson of the
`quota` leg in 5.48 applied to a benchmark: a thing that runs and
reaches no assertion looks like progress in a log and is not.

`make perf` runs the two and fails if either is over its target;
`make perf-bench` is the raw measurement for comparing two revisions.
Neither is in `make check`, deliberately: a wall clock on a shared CI
runner measures the runner, and a gate that fails for that reason is one
people learn to ignore.

**What is not measured, and named as such.** The pillar row states two
numbers — under 500 ms cold and under 5 ms cached — and only the cold
one has anything behind it, because this build has no pillar cache at
all: `pillar_cache_disk` is inert and `halite_pillar_cache_hits_total`
is one of the two metric families of SPEC 26.2 that nothing registers.
Benchmarking a second compile and calling it "cached" would report the
cold number twice under two names. The other eleven rows need the
simulated node harness, a soak, or the integration matrix, and each
carries which.

**What a number from here means.** One machine compiled one generated
tree. It is not a claim about a fleet, and a compile is not an apply:
what is measured is the parse, the render, the requisite resolution and
the ordering, which is what SPEC's row names and what a node does before
it touches anything on the host.

### 5.58 The render sandbox: parsing where there is nothing to take

SPEC 25.4's first bullet, built. YAML parsing and template rendering
happen in a child process; the node keeps module dispatch, template
loading and gpg decryption. `render_sandbox: true` turns it on, and it
is off by default while the path is new.

**The argument, which is SPEC's own.** A node runs as root because
package and service management require it. A state tree arrives from a
file server, from gitfs, or from whoever can commit to the repository it
lives in. Between those two facts sit the lexer, the template parser,
the evaluator and the YAML reader -- the largest and most
attacker-adjacent code in the system, and the code that needs no
privilege at all. So it runs somewhere that has none.

**What crosses.** Data, both ways: the body, the stage list, and the
context a template may read going in; the parsed value with its source
positions, the rendered text, and the warnings coming back. Two things
the child cannot do for itself come back as callbacks -- a template
`include`, which only the parent can resolve through the file server,
and `salt['pkg.version']`, which only the parent can dispatch. That is
SPEC's shape rather than a concession to it: "module execution happens
in the privileged parent ... the sandbox returns data, never a callable
and never a command to run without validation".

**Both kinds of rendering cross, not just the SLS kind.** A
`file.managed` with `template: jinja` renders bytes fetched from the
file server, which is the same attacker-adjacent input by another route,
so it goes through the same child. It is the jinja stage alone over the
bytes as they are: a managed file's first line is content, and reading
`#!/bin/sh` there as a renderer pipeline would deliver something other
than the file. That is why `render.Template` exists separately, and
expressing it as a one-stage run is what let it cross the same boundary.

**The pipeline is split rather than shipped whole**, and it is the
decision worth recording. A pipeline is template stages, then one
serializer, then data stages -- `checkStages` already enforced exactly
that -- and the only data stage this build supports is `gpg`. So the
child runs the head and the parent runs the tail. Decrypted pillar never
enters the unprivileged process at all, and the child is never told
where the keyring is: `TestTheChildIsNeverToldWhereTheKeyringIs` checks
the encoded request for the words.

**The codec, and why `value.EncodeJSON` was not enough.** It drops
source positions, and a compiler that has lost them says "this state is
wrong" without saying which line. JSON's number is a float64, so an
int64 past 2^53, a `.nan` and an `.inf` do not survive it. And
`encoding/json` replaces invalid UTF-8 with U+FFFD, which matters
because `cmd.run` output reaches a template. So values are tagged with
their kind, numbers travel as text, a string that is not valid UTF-8
travels as base64, and positions index a file table. A round-trip test
covers every type in the model and a fuzz target holds the pair to being
exact -- 250,000 executions clean.

#### What it enforces, per platform

Reported by `Describe`, in the manner of the bridge sandbox of 5.21, and
logged once by the node that starts one.

| Platform | Boundary | Identity | Network |
|---|---|---|---|
| Linux, as root | process | drops to `render_sandbox_user` | **denied by the kernel**: the child is in a network namespace of its own, loopback down |
| Linux, not root | process | not dropped, and says so | not denied; a namespace needs CAP_SYS_ADMIN |
| FreeBSD, macOS, other unix | process | drops to `render_sandbox_user` when root | not denied; jails, pledge and Capsicum all need cgo, which SPEC 4.2 rules out |
| Windows | process | not dropped; a restricted token is not built | not denied |

No syscall filter anywhere: the seccomp allowlist SPEC 25.4 asks for on
the *parent* is a separate item and is not built. No filesystem
restriction: "read access to the cached tree and nothing else" is the
account's business today, not the kernel's.

**And the limit that matters most, stated where an operator will read
it:** a template that calls an execution module still causes the parent
to run it. `{{ salt['cmd.run']('...') }}` has the same effect sandboxed
or not, because that is what the template language is; the controls for
it are RBAC's separate `arbitrary_code` permission and the signed-tree
work of 25.1. What moved into the child is the code that parses and
evaluates the attacker's text.

#### What it cost, measured

The same 500 states over 50 SLS files the SPEC 30 benchmark of 5.57
compiles:

| Engine | Compile |
|---|---|
| in process | 96 ms |
| sandboxed | 217 ms |

FreeBSD, Xeon E5-2620 v3, three runs of twenty. It is 2.3 times the
work and still an order of magnitude inside SPEC 30's 2 s target, which
is the number the decision to make this the default will turn on. The
parent's own allocations fall by half, because the parsing is no longer
happening there.

#### Two defects, both found by running it rather than by testing it

The tests passed on both.

**Every render error read "web.sls: render failed: web.sls:22:1: ..."**
The sandbox wrapped the child's message to mark it as a render failure
rather than a transport failure, which reads fine in isolation. In place
it was wrong twice: the compiler prefixes a diagnostic with the file
unless the message already names it, so the file appeared twice and
three words were added that mean nothing to an operator. The marker is
now a type that answers `errors.Is` and prints only the child's words.
The test that missed it asked whether the message *contained* the
renderer's words; it now compares it with the in-process message
exactly, and fails on the old behaviour.

**`render_sandbox_user: no-such-account` rendered happily on a node
that was not root.** The account was looked up only on the branch that
could act on it, so on any node that could not drop privilege the
setting was accepted and ignored. That is the worst of the three
available outcomes: a control that reports itself as configured and does
nothing. The account is now resolved whether or not it can be applied,
so a typo fails on every machine, and `Unenforced` reports a control
that was asked for and cannot be applied -- which the node logs as a
warning rather than leaving in a description nobody reads.

#### What is not established

The unprivileged account has not been run: this project's hosts render
as the developer's own account, so `Credential` and the Linux network
namespace are both written and unexercised. A node running as root with
`render_sandbox_user` set is what closes that, and it is an afternoon on
any of the fleet's machines rather than a missing mechanism. The hub is
not wired: it renders the pillar top file and every pillar SLS in
process, which is where the secrets are, and SPEC 25.4 is titled for the
node. Neither of those is a gap in the mechanism, and both are named
here rather than left for somebody to discover.

### 5.59 A nil pointer behind a nil error, three times over

`halite-hub runner pillar.show_pillar node=ref-salt1` panicked the hub
on 2026-09-11, against a node that had been accepted and had not yet
connected. The handler died with "invalid memory address or nil pointer
dereference" and the operator got a stream error.

**The cause is one line and it is a contract rather than a mistake in
arithmetic.** `NodeCache.Get` reported absence as `(nil, nil)`: no data,
no error. Three of its four callers checked the error and then
dereferenced the nil:

| Command | What it dereferenced |
|---|---|
| `pillar.show_pillar` | the node's cached grains |
| `cache.grains` | the same, one function further in |
| `manage.versions` | the reported version, for every accepted node in the survey |

Each is an ordinary command against an ordinary state -- the window
between `halite-hub keys accept` and a node's first subscribe -- and
each took the hub's handler with it rather than returning an error.

**The fix is the contract, not the call sites.** `Get` now reports
absence as `ErrUnknownNode`, wrapped with the node's name and what is
missing. Every caller that was wrong already checked the error, so all
three became correct without being touched; the two that genuinely want
"no data is fine" -- `Matchable`, which must still match a node on its
own ID, and the relay's grain forwarding -- say so with `errors.Is`. A
caller added tomorrow cannot forget a nil check it does not have to
make.

**The convention already existed and this was the one store outside
it.** Six stores in this tree read a record by identifier, and five of
them -- the job cache, the keystore, the API token store, the mine, and
the orchestration store -- already reported absence as a named error
(`ErrNoJob`, `ErrNotFound`, `ErrNoMineData`, `ErrNoOrchRun`). Only the
node cache returned a nil pair. That is the shape this ledger keeps
recording: two paths that must agree, where one of them was written
later and nobody compared them.

#### The sweep that followed

Every function in the tree that returns a pointer or a map together
with an error and can return both as nil, found by walking the syntax
tree rather than by grep: **seventeen**, of which the node cache was the
only defect.

- Eleven nil-check at every call site: the apt, yum and choco
  repository providers, `lvmFindLV`, `lvmFindPV`, `macAssistiveFind`,
  `firstBrewFormula`, `loadFile`, and the render sandbox's own
  encode and decode helpers.
- Three are nil-safe by design, which is the other correct answer:
  `*tracing.Tracer` hands out nil spans whose methods do nothing,
  `*policy.Policy` denies everything when nil, and `requireProtocol`
  returns nil to mean "use the base configuration", which is the
  standard library's own contract for that callback.
- Two carry a nil map, which Go reads as empty: the orchestration
  resume seed and the sandbox's keyword arguments.
- One was the node cache.

What the sweep cannot do is prove the general rule, and saying so is
part of the finding: deciding whether *this* call site dereferences
*that* function's result needs type resolution rather than syntax, so
the seventeen were read by hand. The guard that remains is behavioural
and specific --
`TestARunnerAnswersForANodeThatHasNeverConnected` calls all three
commands against a node in exactly that state, and it reproduces the
original panic exactly when the contract is put back.

### 5.60 The staging directory an agentless run cannot execute from

SPEC 21.1 caches the pushed binary under `thin_dir`, which defaults to
`/var/tmp/halite-thin`, and runs it from there. A host hardened to a CIS
benchmark commonly mounts `/var/tmp` with `noexec`.

**Every step of an agentless run up to the last one succeeds on such a
host.** The directory is created, `chmod 700` applies, the binary
copies, its SHA-256 verifies against the local one, and it is moved to
its cached name. Then it does not run, and what the operator is handed
is a bare "Permission denied" about a file that was installed
successfully a moment earlier, or the run's own "answered without a
framed return; the binary may not have run".

`mkdir -p` succeeding says the directory exists. It does not say this
account can use it, and the dimension that decides this path is
execution. So the preparation script now proves it: it writes a two-line
probe, runs it, removes it whatever happened, and reports three
different failures apart --

| Exit | Meaning |
|---|---|
| 1 | the directory could not be created |
| 2 | nothing could be written into it |
| anything else | the probe would not run, which is what `noexec` looks like from here |

-- so a target that will not execute is refused by name, with `noexec`
and `thin_dir` in the message, rather than diagnosed as a corrupt
transfer. It is the check `OpenNodeCache` already makes for itself on
the hub, asked about execution rather than about writing, and it rides
in the round trip that was already being made.

**And a diagnosis that was too confident, which CI found.** The first
version treated anything that was not exit 1 or 2 as "the probe would
not run", including a command that never ran at all. The Windows leg
could not find `ssh` and was told its `/var/tmp` was mounted `noexec`.
A command with no exit status has established nothing about the
directory, and now says so.

**What is established and what is not.** The script is run through a
real `/bin/sh` against a real directory, including both of the failures
that can be produced without root, because the shell is the thing being
programmed and a script checked against what its author meant is what
5.31 is about. Two things came out of running it rather than reading it:
`chmod 700` on a directory the caller *owns* repairs it, so the
unwritable case is only reachable when the directory belongs to somebody
else; and the probe's own name being taken is the way to reach that
branch locally. What has not happened is a `noexec` mount refusing it,
which needs root on a hardened host and is plan.md's item 19.

This package had no tests beyond framing before this. It has five now,
and they are the first coverage the agentless transport has had.

### 5.61 `ps`: the process table, without a C library

SPEC 15.2 names `ps` and this ledger recorded it as not implemented with
the reason "process enumeration is per-platform; FreeBSD needs `kvm` or
`sysctl kern.proc`". Both halves of that were true and neither was
reachable: `libkvm` is C, and `sysctl kern.proc` from Go needs cgo or
golang.org/x/sys, which SPEC 4.2 rules out. Linux's `/proc` could have
been read directly and no other platform has one, so a reader written
that way would have been a Linux module wearing a portable name.

So it asks `ps`, the program every unix has, with the column names POSIX
specifies. On FreeBSD it asks for libxo JSON instead, which is the `jls`
decision of 5.32 for the same reason 5.31 gives: parsing a
human-aligned table where a structured interface exists is choosing the
surface that bit `pf`. Elsewhere the columns are requested explicitly
and the command is taken as everything after the eighth field, because
it is the only column that can hold a space.

Seven functions, under Salt's names so an existing tree keeps working:
`pid_list`, `proc_info`, `pgrep`, `psaux`, `top`, `kill_pid`, `pkill`.

Three decisions worth recording:

- **`proc_info` refuses a process that is not there** rather than
  answering with an empty mapping. A tree acting on a pid it read
  somewhere else needs to tell "gone" from "here with nothing to say".
- **`pkill` refuses a pattern that matches nothing.** A pattern matching
  no process is a misspelling far more often than a tidy machine, and
  reporting success is how a tree comes to believe it stopped something
  it never named. `pgrep` is how to ask without acting.
- **A signal this build does not know is refused rather than passed
  through.** `kill -0` and `kill -9` differ by one character and one of
  them is a question. The set is the signals every unix spells the same;
  `SIGINFO` would work on a BSD and fail on Linux, at the moment it was
  wanted.

No Windows: `ps` is a unix program, and a `ps` that quietly meant
something else on one platform is worse than one that says it does not
run there. No CPU or memory totals either -- `status.loadavg`,
`status.meminfo` and `status.uptime` already answer those, and a second
spelling of the same number is a thing to keep in agreement for no gain.

**It is `hardware`, and cheaply.** The mutating half is demonstrated
against processes the test starts and marks with a string that exists
nowhere else on the machine: killed by pid, killed by pattern, and shown
to change nothing in test mode. That needs no root and touches nothing
an operator is using, which is the rule rather than a convenience -- a
test that pattern-matches a live process table can kill something
somebody is relying on. What is *not* covered is signalling another
account's process, which is the case that needs privilege.

Writing the test found the thing worth knowing about the process table:
**a shell `exec`s the last command of a `-c` string**, so
`sh -c "sleep 300 # marker"` leaves `sleep 300` in the table and the
marker is gone with the shell that held it. The children here block on
`read`, which is a builtin, so the shell stays with its own command line
intact and there is no grandchild to leak.

A second defect came from a unit test written against a shape Linux
really prints: `[kworker/0:1]`. The name was taken as the last path
element, which turns a kernel thread into `0:1` and makes
`pgrep kworker` match nothing. The slash inside a bracketed name is part
of the name; the brackets come off and the rest is left alone.

One thing the first real run showed and no fixture would have: a
process that rewrites its own argument vector has whatever it wrote
there as its command, so `smbd: client [10.0.0.1] (smbd)` reports the
name `smbd:`, colon included. That is what the machine says the process
is called and it is left alone rather than tidied, because the tidying
would be a guess about somebody else's title format. Matching is by
regular expression, so a pattern of `smbd` finds it either way.

**What this unblocks.** Two of SPEC 16.2's beacons, `proc` (a process
appearing or disappearing) and `ps` (a process crossing a resource
threshold), were registered as pending "a later phase, with a portable
reader for it". That reader now exists, and a beacon in this build is a
function over the node's own execution modules.

### 5.62 The two process beacons

`proc` and `ps` were registered as pending "a later phase, with a
portable reader for it". The reader arrived with the `ps` module of
5.61, and these are functions over it -- which is what a beacon is in
this build, and why one is portable wherever its module is.

**Two beacons, because SPEC gives them different jobs**, and the
difference is the one an operator cares about. `proc` answers "is it
there", which is a question about a thing that should be running and a
page when it is not. `ps` answers "is it behaving", which is a question
about a thing that is running and a page when it eats the machine. Salt
has both names and uses them for nearly the same thing; here they mean
what the inventory says.

    beacons:
      proc:
        - processes:
            sshd: running
            oldthing: stopped
      ps:
        - processes:
            nginx:
              cpu_percent: ['>', 80]
              rss_kb: ['>', 500000]

Three decisions:

- **Every key is a pattern, matched against the process name or the
  whole command line.** A daemon is often several processes, a
  supervisor and its workers, and a beacon that could only name one of
  them would answer a different question from the one asked.
- **A wanted state decides whether to speak at all**, which is what
  makes `stopped` useful: the event is the absence. With no wanted
  state the reading is reported every poll and `onchangeonly` decides,
  which is how the `service` beacon beside it already behaves.
- **The `ps` thresholds compare the process table's own fields**, under
  the comparison form the `load` beacon already uses. Nothing is
  derived on the way, so a threshold means what `ps.psaux` reported. A
  field the table does not have is refused with the fields it does,
  because the alternative is a beacon that never fires and never says
  why.

Both are exercised against processes the tests start and mark, for the
reason 5.61 gives: a pattern loose enough to match somebody's editor
will eventually be handed to something that kills. The wiring in those
tests is the node's own, a dispatcher over the execution registry, so
what is checked is the arrangement a beacon actually runs under rather
than a function called directly.

### 5.63 The six `file` functions SPEC names and this build did not have

`patch`, `sed`, `seek_read`, `seek_write`, `list_backups` and
`restore_backup`. The module goes from 40 of the ~50 SPEC 15.2
enumerates to 46, and three of the six are worth more than their size.

#### `patch` reverses a file if you let it

SPEC says "`patch` uses the system `patch` binary", which is right for
the reason `ps` shells out: agreeing with GNU patch about fuzz, offsets
and reversed hunks is a large program to write and an unbounded one to
keep right.

What running it found is the whole reason this entry is here. **A patch
applied twice, with no terminal, reverses the file and exits zero.**
`patch` detects the condition, asks "Reversed (or previously applied)
patch detected! Assume -R? [y]", gets no answer, takes its own default,
and undoes the change. A state run's second pass is exactly that
situation, so a tree that applied a patch would have had it silently
reverted on the next highstate, with a success reported. Measured on
FreeBSD patch 2.0-12u11.

So the question is never asked: `--batch` refuses every prompt and
`--forward` ignores an already-applied patch and says so. A caller who
means to reverse one passes `-R`, and then `--forward` is left off,
because otherwise it would refuse their own request.

Two smaller things came from the same run. `--forward` writes a
`<name>.rej` beside the file every time it ignores a patch, which is
litter in a directory a tree manages, so the reject file is discarded
with `-r -`. And `patch` puts its verdict in the *middle* of its output
-- it opens with a commentary on what it thinks the file is and ends
with "done" -- so the error carries every line rather than the first or
the last.

Test mode runs `--dry-run`, which is what makes SPEC 11.6's contract
satisfiable for a patch without a second opinion about what one would
do.

**And a platform where there is nothing to get right.** CI's Windows
runner resolves `patch` to Strawberry Perl's 2.5.9, which aborts on an
ordinary unified diff -- "Assertation failed! ... patch.c, Line 354;
Expression: hunk" -- under `--dry-run` as well as for real. A module
cannot be correct against a binary that asserts, so the live test skips
there, naming the tool and its message rather than the platform. A
Windows node with a working `patch` is served by the same code; what is
recorded is that this project has never seen one.

#### `sed` does not run sed, and does not delegate either

Salt's `file.sed` shells out to `sed -i`. This one does the work in Go,
against a regular expression this project's own engine compiled and with
the atomic write the rest of the module uses, so nothing runs an editor
over a file as root.

The obvious implementation is a thin front door onto `file.replace`, and
it is wrong for one reason: `limit` is a **per-line** filter and
`file.replace` works on the whole file. Passing it through would either
ignore it, which replaces more than the caller asked and is the
accept-but-do-nothing defect this project keeps finding in its own
settings table, or quietly mean something else. So the line walk is
here, and `g` means what sed means by it: without it, one replacement
per eligible line.

#### The backup cache

`list_backups` and `restore_backup` need somewhere to list, and this
build had a `backup` argument that wrote `<path><suffix>` beside the
file -- useful, and not something either function can enumerate.

`backup: node`, with Salt's own value accepted beside it, now keeps a
timestamped copy under `<cache_dir>/file_backup/<the file's own absolute
path>/`. The path is mirrored rather than flattened so two files with
the same basename do not share a history and an operator can find a
backup with `ls`. The timestamp format sorts lexically as well as
chronologically, which is what lets the listing sort by name and be
sorting by time, and it holds no colons, because a Windows path cannot.

Two decisions: a restore **keeps the current contents first**, so
choosing the wrong backup is itself undoable; and a backup identifier is
a name in the cache and nothing else, so one holding a separator is
refused rather than resolved into a path outside it.

A node with no `cache_dir` keeps no backups and says so, rather than
writing them relative to whatever the working directory happens to be.

**And the fourth instance of a defect this project has already recorded
twice.** The first version named each copy by `time.Now` on the
reasoning that two backups cannot be taken in the same instant. They
can: the clock is only as fine as the platform's, and on Windows that is
about half a millisecond. CI's Windows leg turned three keeps into two
files -- the second silently replacing the first, which is the whole
point of a backup, lost.

The webhook returner's spool and the relay's spool both did this, both
lost returns for it (4.9), and the concurrent-writer scenario in the
chaos layer of 5.29 exists *because* of them. Knowing about a defect
class is evidently not the same as not writing it again. The name is now
claimed with `O_EXCL` and takes a suffix on collision, so two processes
keeping a backup of one file in one tick cannot both win, and the test
for it holds the clock still rather than racing it -- which is the
difference between a test that would have caught this and one that
happened to run on the right machine.

#### What is still not there

`get_selinux_context` and `set_selinux_context`, deliberately. There is
no SELinux on any machine this project has, and a context reader written
from documentation is the mistake 5.31 is cited for. They belong with
the `selinux` core module and with the RHEL host of plan.md item 14.

`file.accumulated` is also still absent and is a different shape: SPEC
15.5 promises it as a *state*, one that other states append to and that
a `file.managed` renders, so it needs the compiler to carry accumulated
data across chunks rather than a new file operation.

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
`cert_info`, `status`, and the two process beacons `proc` and `ps`. The controls of SPEC 16.3 are all there — a
token bucket per instance, coalescing with a count, a bounded queue that
reports what it dropped, and `disable_during_state_run`.

What is **not** built in beacons:

- **`inotify` and `fanotify`.** Both need raw syscalls through
  `golang.org/x/sys`, which SPEC 4.2 records as an open question and
  which this build does not admit. `filechanges` polls on digest and
  metadata, which is the portable answer SPEC 16.2 names for exactly
  this case; it is slower, and a change that is reverted between two
  polls is one it never sees.
- **Fifteen of SPEC 16.2's inventory**: `swapusage`, `cpuusage`,
  `network_info`, `network_settings`, `pkg`, `journald`, `log`, `wtmp`,
  `btmp`, `sh`, and the four platform notifiers. Each is registered and
  answers with when it arrives, so a configuration naming one is refused
  with a reason rather than skipped. `proc` and `ps` have left this list
  — see 5.62.
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
- ~~**Tracing** (SPEC 26.3), the one part of section 26 still unbuilt.~~
  Built: `doctor` (26.4) ships, see 5.30, and tracing ships with it, see
  5.34. **Section 26 is complete.**
- **`mtls` hook authentication.** The mode is implemented and refused
  when no client certificate is presented, but it has never been
  exercised against a real sender.

Phase 5 is part built — gitfs, s3fs, the agentless path, relays and the
FIPS artifact set are in, and 6.1b says what each covers. What is
absent from 5 and 6: Windows and macOS parity, detached job signing,
signed state trees, node-side evidence, and the backtracking regex
engine. The render sandbox is built: see 5.58.

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
now exists and carries it (5.30).

What is **not** built in phase 5:

- **The reverse tunnel** of SPEC 21.1. Pillar and tree go inline, and a
  tree larger than 4 MiB is refused rather than transferred on every run
  against every target.
- **The `scan`, `cloud`, and `terraform` rosters** of SPEC 21.2, each
  refused by name.
- **macOS parity, in evidence rather than in inventory.** The package
  and service providers ship, and all eight of SPEC 15.3's macOS
  modules — `mac_defaults` (also a core state under SPEC 15.5),
  `mac_power`, the `dscl`-driven `mac_user`, `mac_group` and
  `mac_shadow` (with which `user.present` and `group.present` work on a
  Mac), `mac_softwareupdate`, `mac_keychain` and `mac_assistive`. What
  is missing is a Mac in CI: every one of these is `assumed` in the
  evidence table because its read side has a live test but its mutating
  side changes a real Mac and no CI leg is one, and `make
  release-gate` is red on the set.
- **Windows parity, in part.** The suite now runs natively there and
  passes: see 4.6. What is built is the platform-neutral half — grains,
  the file states, `cmd`, the Chocolatey provider, the extension
  sandbox — and four of SPEC 15.3's eighteen Windows modules:
  `win_dacl`, `win_service`, `win_registry` and `win_task`. What is not
  is the other fourteen, listed in 2.3. There is still no user or group
  provider for the platform, so `user.present` has nothing to reach.
- **`minionfs`/`nodefs`**, which SPEC 13.2 marks a subset and disables
  by default.

### 6.2 Build and release

- `make release` has never been run. The vendoring step now has something
  to vendor — `golang.org/x/sys`, from SPEC 4.2's allowlist, for the
  Win32 bindings — and `go mod vendor` runs; all eight targets still
  cross-compile from the vendored tree.
- Cross-compilation is verified for the four target platforms; nothing beyond
  compilation is verified.
- **CI runs the gates**, in `.github/workflows/`. SPEC 4.2 says the
  dependency policy "has teeth: CI enforces it", and until 2026-09-05
  nothing enforced anything: every gate was a make target or a Go test,
  run when somebody remembered. `ci.yml` runs each leg of `make check`
  on push and on every pull request, on Linux and Windows, with the
  Salt differential and the race detector among them. `release.yml`
  builds a tag on two runner images and compares the digests, which is
  SPEC 4.3's two-builder check and the one property `make repro` cannot
  establish on its own.
- The two Windows legs do not go through `make`. GNU make on Windows
  runs recipes through `cmd.exe` unless it finds a POSIX shell, and
  these recipes are `sh`; provisioning one is more moving parts than the
  single `go test` line each target expands to. The workflow says so at
  the point it does it, and says what to do if either grows past a line.
- The Makefile is written for BSD make and uses `!=` rather than
  `$(shell ...)`. GNU make handles `!=` from 4.0 onward, and it has now
  been run under one: every target expands under GNU Make 4.4.1, and
  `racecheck` was run through it end to end rather than only expanded.
  What that establishes is that the file parses and its recipes are
  well formed there; it is not a claim that every target's *result* has
  been compared between the two makes.
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

**Superseded, 2026-09-11 — read plan.md §7 instead.** This section is
what "ranked by correctness value" looked like before this project had
a Linux host at all: item 1 below asks for one and forecasts "60 of the
62 platform modules of SPEC 15.3 wait behind it". That host has
existed and been used continuously since, SPEC 15.3 counts 65 rather
than 62, and 40 of those 65 now ship (plan.md §2.3) — the Common Linux
row is eleven of twelve, `apparmor` and `netplan` are both `hardware`,
and the correctness-value ranking has moved on through several
revisions plan.md tracks and this file does not. Kept rather than
deleted because items 2-4 (real trees, the documents-accepted set, the
template conformance gaps) are narrower claims that have aged better —
a reader wanting *today's* ranking, including what platform each
remaining item needs, wants plan.md §7 and its "Blocked on platform
access" items.

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
   that could be got cheaply. The apt provider has since been run on a
   real Ubuntu host, base functions and all four optional capabilities
   (2.5). What is left needs a host of another family: the dnf/yum and
   apk providers, the systemd provider, and `useradd`. 60 of the 62
   platform modules of SPEC 15.3 wait behind it.
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
