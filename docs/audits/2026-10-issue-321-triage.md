# Triage of the issue 321 audit (High and Medium findings)

Source: <https://github.com/edlitmus/halite/issues/321>, an external audit
filed 2026-10-14 against commit `a0d5344`. This file records what was
done to check its 20 High and 55 Medium findings (plus the design note
M-56) on 2026-10-15, at the same commit. It is a working document for
choosing and tracking fixes, not a ledger: each fix still gets its own
`DIVERGENCE.md` section, and a `CHANGELOG.md` entry where an operator
would notice.

## Fixes opened so far

One PR per logical change, all referencing issue 321 and none closing
it. "Before merge" names a run on real hardware that the PR's own
author could not do.

| Finding | PR | Before merge |
|---|---|---|
| H-01 RBAC target kind | #344 | |
| H-03 archive symlink escape | #333 | |
| H-04 template limits (+ L-engine-1, L-engine-3) | #332 | |
| H-05 background `runas` | #327 | |
| H-06 `listen` reactions | #347 | |
| H-07 agentless cache trust | #326 | |
| H-08 `file.line` regex (+ empty delete) | #343 | |
| H-09 `ArbitraryCode` flags | #330 | |
| H-10 token revoke race | #335 | |
| H-11 cert revoke race | #338 | a FreeBSD hub run of the file lock |
| H-12 compound target depth | #324 | |
| H-13, M-18, M-41 `--test` mutations (+ beacons, x509) | #345 | |
| H-14 dotted systemd unit names | #350 | |
| H-15 mount device identity | #328 | |
| H-16 `cpan.module_version` | #341 | |
| H-17 mdadm create guard | #349 | the fleet `linux` leg |
| H-18 zfs/zpool properties | #348 | the fleet `freebsd` leg |
| H-19 gitfs verifies the served commit | #325 | |
| H-20 aws-secrets region | #329 | |
| M-02 signed-job replay | #339 | |
| M-04 typed job signing (breaking) | #337 | |
| M-32 package name injection | #331 | |
| M-46 timezone traversal | #336 | |
| M-49 gitfs keeps served trees | #346 | |
| M-50 pillar `S@` targets | #334 | |
| M-54 SLS name spellings (state half) | #340 | |
| M-55 `file_ignore_glob` fnmatch | #342 | |
| M-08, M-09 login backoff by (source, name) | #352 | |
| M-10 login timing, equal work | #353 | |
| M-38 pf anchor order | #354 | |
| M-37 firewall value validation | #355 | a grep of the FreeBSD trees for host names in `from`/`to` |
| M-36 netplan refused file | #356 | |
| M-39 hostname short vs FQDN | #357 | |
| M-28 agentless `oneshot` tree | #358 | |
| M-16 webhook ingress (`auth: mtls` now refused) | #359 | |
| Section 1.2 toolchain (go1.27.2) | #360 | |
| M-01 `allow_sls` refuses the highstate (operator-visible) | #366 | a hub-to-node run under a narrowed role |

Not yet started: H-02 (extension manifest signing; a format decision),
and the Medium findings not listed above.

## How this was checked, and what that does not cover

Eight reviewers each took a batch of findings. Each one read the cited
code, followed the callers, and where it was cheap reproduced the claim
with a throwaway test in a scratch copy of the repository (never in this
tree). Each also grepped `DIVERGENCE.md` for an existing record.

The labels below mean:

- **CONFIRMED (repro)**: reproduced by a test calling the real functions,
  usually with the package's fake or recording runner.
- **CONFIRMED (trace)**: read end to end with callers checked; not run.
- **PARTLY**: the mechanism is real, but part of the audit's claim is
  wrong or overstated. The correction is given.

**Not verified, for every row:** the checks ran on macOS, as a
non-root user. No real apt, dnf, pkg, apk, pfctl, nft, iptables, ufw,
sysrc, systemd, LVM, mdadm, zfs, netplan or Linux/FreeBSD `mount` was
driven. Nothing ran on FreeBSD. Nothing ran as root. No hub, node and API
were run together, and nothing ran against S3, AWS, an IdP or LDAP. So
whenever a row says what a real tool *does* with the argv halite builds,
that is the reviewer's knowledge of the tool and has not been demonstrated.
Rows that turn on such behaviour are marked **needs a host**.

The ~90 Low findings in the audit's section 5 were **not checked**.

## Section 1 of the audit: toolchain

- `go.mod` pins `toolchain go1.26.6`; go1.26.9 is published (checked
  against go.dev's release list). The ten advisory IDs themselves were not
  looked up.
- No workflow runs `govulncheck`; `make vuln` runs `govulncheck@latest`,
  unpinned. DIVERGENCE (near line 1904) explains why it is not part of
  `make check`. Revised: Low as a process gap, but the toolchain bump
  itself is worth doing first because it is cheap and several of the
  advisories are on the hub's server paths.

## Summary

| | Count |
|---|---|
| High confirmed | 20 of 20 (3 wider than the audit says) |
| Medium confirmed | 46 of 55 |
| Medium partly confirmed | 9 of 55 |
| Medium refuted | 0 |
| Already recorded in `DIVERGENCE.md` | M-15 only (as accepted trust); M-03 and M-16a argued; M-41 deferred |

Revised severities moved 8 Highs down (H-04, H-05, H-06, H-13, H-14,
H-15, H-18, H-20 to Medium or Medium-High) and one Medium up (M-04). Low
was assigned to M-03, M-16, M-26, M-28, M-31, M-34, M-37, M-39, M-40,
M-47 and M-48.

## Ledger and comment statements the checks found to be false

These are the "exculpatory conclusions" CLAUDE.md warns about: written
reasons that something is fine, which are not. Each needs correcting with
the fix that touches it.

| Where | Says | Reality | Finding |
|---|---|---|---|
| `internal/keystore/keystore.go:105-107` | a lock file guards against a second process | no lock file exists anywhere | H-11 |
| `internal/builtin/file_line.go:105-108` | substring match is "the comparison Salt uses"; makes `^PermitRootLogin` work | Salt uses a regex; a literal `^` never matches | H-08 |
| `internal/builtin/mdadm.go:55-57`, `force` doc at :149 | refuses a member mdadm flags as holding a filesystem | no such check; `--run` suppresses mdadm's own | H-17 |
| extension comments (`runtime.go:72-75`, `runtime_test.go:179`, `bridge/doc.go:22`, `manifest.go:136`, `sync.go:192-194`) | declarations are signed | only file digests are signed | H-02 |
| DIVERGENCE 5.39, "The fallback boundary" | a D-Bus error fails the same way under systemctl | false for dotted unit names; systemctl succeeds | H-14 |
| DIVERGENCE 5.232 | "a backoff, not a lockout"; known and unknown names cost the same hash | an attacker holds a name locked indefinitely; unknown names cost 210k iterations vs 600k | M-09, M-10 |
| DIVERGENCE 1.9 | a relay cannot claim a node connected directly to the upstream | true for routing, false for cached grains and for returns via a stale `via` entry | M-15 |
| DIVERGENCE 5.31 | `quick` makes a rule's effect independent of anchor order | only for non-overlapping rules | M-38 |
| DIVERGENCE 5.38 | leaves the rejected netplan file on disk (deliberate) | does not mention that run 2 then reports success | M-36 |
| DIVERGENCE near line 11961 | the test-mode audit "holds all 248" mutating functions | it reports a "quiet" bucket it cannot judge; seven `file.*` and `service.mask` hide there | H-13, M-18 |
| DIVERGENCE near line 11076 | agentless runs on a machine whose staging directory is discarded | the oneshot temp tree is never removed | M-28 |
| DIVERGENCE near line 24206 | mtls hooks are refused when no client certificate is presented | the API never requests one, so no mtls hook can ever succeed | M-16c |
| `internal/job/guard.go:44-46` | replay window bounded by the job's own expiry | not across a restart, and expiry may be zero | M-02, M-04 |

## High findings

| ID | Verdict | Revised | Ledger | Notes and corrections |
|---|---|---|---|---|
| H-01 RBAC target kind | CONFIRMED (repro, policy half) | High | not recorded | **Wider:** the hub's own check (`internal/hub/jobs.go:204-210`) has the same gap, so direct hub submitters are affected; `mine.go` reads too; a `pcre` target that looks literal widens through `.`. `{id}` comma route traced, not run. |
| H-02 manifest unsigned | CONFIRMED (repro) | High | not recorded | Signed bundle with rewritten `declares:[root,network]`, version 9.9.9 loaded under `RequireSignature` and a full pin. Pin's version half is unsigned too. |
| H-03 archive symlink chain | CONFIRMED (repro) | High (third-party archives) | not recorded | Wrote outside dest, and replaced a file outside dest with a symlink under `overwrite=false`. DIVERGENCE near 24258 ("refuses symlinks") is about gitfs, not this. |
| H-04 template crash/OOM | CONFIRMED (partly repro) | Medium | not recorded | 100k-deep parse succeeded (no counter); `indent(width=80000000)` blew past `MaxOutput`; `indent(-1)` panics out of `RenderString`. `range` overflow traced. 1e11 cases not run. |
| H-05 `bg` drops runas | CONFIRMED (repro) | Medium-High | not recorded | `RunAs:"no-such-user"` errors in foreground, returns a pid with `Background`. |
| H-06 `listen` skips runas/gates | CONFIRMED (repro) | Medium-High | not recorded | Listener with `creates:` an existing dir ran anyway. Salt's `call_listen` keeps the whole low chunk and runs gates and retry. |
| H-07 agentless cache by name | CONFIRMED (trace) | High on multi-user targets, Low otherwise | not recorded | DIVERGENCE near 2740 ("unsafe thin_dir refused by name") is a character check, not ownership. |
| H-08 `file.line` substring | CONFIRMED (repro) | High | not recorded | Three runs left `PermitRootLogin yes` first with three `no` lines after; sshd keeps `yes`. Existing test uses a bare word. |
| H-09 missing `ArbitraryCode` | CONFIRMED (registry dump) | High | not recorded | **Wider:** the execution-module `cmd.script` lacks the flag too; the hub merges registries by name and hides it, the node's `require_job_signature` does not. So four functions run unsigned on such a node. 5.16 fixed the same class for six names only. |
| H-10 token revoke race | CONFIRMED (trace) | High | not recorded | |
| H-11 cert revoke vs renew | CONFIRMED (trace) | High | not recorded | Plus the false lock-file comment above. |
| H-12 compound target recursion | CONFIRMED (repro, reduced stack) | High | not recorded | ~170 bytes per level; ~6 MB of `(` at the 1 GB default stack. Needs a role covering `*`, or a node `mine.get` grant. |
| H-13 `service.mask` under `--test` | CONFIRMED (repro, fake provider) | Medium-High | not recorded | **Correction:** the harness misses it because it lands in the "quiet" bucket (no systemd, or D-Bus bypasses the recording runner), not because helper-built signatures hide it. |
| H-14 dotted unit names over D-Bus | CONFIRMED (trace) | Medium | contradicts 5.39 | Reads fall back to systemctl; mutations do not. systemd's rejection of `php8.2-fpm` **needs a host**. |
| H-15 `UUID=` remount every run | CONFIRMED (trace) | Medium | not recorded | No blkid or by-uuid resolution anywhere. `mount` is `Captured`: no mutating run exists. |
| H-16 `cpan.module_version` injection | CONFIRMED (repro, local perl) | High | not recorded | Read-only, test-mode-safe label on arbitrary code. |
| H-17 `mdadm --run` | CONFIRMED (trace) | High | ledger 5.53 accurate; code overclaims | mdadm's prompt-suppression from source knowledge; **needs a host**. `--examine` error path fails open. |
| H-18 zfs property compare | CONFIRMED (repro, scripted runner) | Medium-High | not recorded | `atime: off` became `zfs set atime=false`. Real zfs rejecting it **needs a host**. `zfs` evidence note excludes properties beyond creation. |
| H-19 gitfs verifies tag name | CONFIRMED (trace + git resolution repro) | High | not recorded | `shortRef` strips `refs/heads/`, so a same-named tag wins. Signed-pass not run (gpg-agent unavailable). |
| H-20 aws-secrets region host | CONFIRMED (URL parse repro) | Medium (High if the hub's network position makes SSRF valuable) | not recorded | **Wider:** with `node_grain`, the node-supplied entry's own `region` field is unchecked too. Secret key does not leak; key id and session token do. |

## Medium findings

### Authorization, auth and hub (M-01 to M-17, M-56)

| ID | Verdict | Revised | Ledger | Notes and corrections |
|---|---|---|---|---|
| M-01 `allow_sls` with no args | PARTLY (repro) | Medium | not recorded | Bypass is real and contradicts SPEC 23.5. **Wrong:** `docs/configuration.md:442` is about `state_allowlist`, which is enforced per SLS during highstate, so that sentence is not contradicted. Bypass reaches only what the top file assigns. |
| M-02 replay guard | CONFIRMED (repro) | Medium | not recorded | Same JID re-accepted after 4,096 junk admits. |
| M-03 no govulncheck in CI | CONFIRMED | Low | argued near 1904 | |
| M-04 signing expiry and scalar encoding | CONFIRMED (repro), **worse** | Medium-High | not recorded | The node honours test mode only as a Go bool (`execute.go:80`), and `"true"` and `true` sign identically, so a compromised hub can turn a signed dry run into a real run. Traced end to end; canonical equality reproduced. |
| M-05 superseded certs after restart | CONFIRMED (repro) | Medium | not recorded | CRL also omits superseded serials. |
| M-06 unbounded enrollment | CONFIRMED (repro), wider | Medium | 5.284 mentions pending records as operability only | **Wider:** in token mode a bogus token still leaves a pending record that locks out the real node. |
| M-07 token lookup cost | CONFIRMED (repro) | Medium or higher | not recorded | ~4.2 s per garbage request at 5,000 tokens on macOS (the audit measured 78 ms). |
| M-08 unbounded login username | CONFIRMED (trace) | Low-Medium | not recorded | PBKDF2 slot limit slows filling the table. |
| M-09 backoff is a lockout | CONFIRMED (simulation) | Medium | contradicts 5.232 | 105 requests per day held a name blocked for the whole day. 5.232 already records the cross-name half. |
| M-10 enumeration by timing | CONFIRMED (repro) | Low-Medium | contradicts 5.232 | ~121 ms vs ~45 ms. |
| M-11 1 MiB line limit | CONFIRMED (trace), corrected | Medium | not recorded | **Correction:** one reconnect per node per oversized job, not a loop; the job is silently lost. Event half not reproduced. |
| M-12 returns from non-targets | CONFIRMED (trace) | Low-Medium | not recorded | `Missing()` filters correctly; the API's synchronous count does not. |
| M-13 flood erases history | CONFIRMED (trace), easier | Medium | not recorded | Caller-chosen `Chunk` lets one node write unlimited 64 MiB files against any existing JID. |
| M-14 ssh option injection | PARTLY (repro on OpenSSH 10.3) | Medium if the hub's ssh is older than 9.6, else latent | not recorded | halite's argv shape is rejected by 10.3; older OpenSSH **needs a host**. scp path is not a vector as written. Only the cache roster is node-controlled. |
| M-15 relay claims node IDs | CONFIRMED (trace) | Medium | **recorded** as accepted trust in 1.9 | 1.9 is wrong about grains and stale `via` returns (see above). Needs `accept_relays`. |
| M-16 webhook ingress | CONFIRMED (trace) | Low | (a) argued, not the race | (c) fails closed: no mtls hook can succeed today. |
| M-17 `/v1/orch` nil kwargs | CONFIRMED (trace) | Medium | not recorded | `deny_sls` still applies to `sls`. |
| M-56 runners unscoped | design note | n/a | not recorded | SPEC 23.5 does not address it; a sentence there, or scoping, is owed. |

### Node and core modules (M-18 to M-31)

| ID | Verdict | Revised | Ledger | Notes and corrections |
|---|---|---|---|---|
| M-18 `file.*` exec under `--test` | CONFIRMED (repro, 7 of 9) | Medium, higher for `rmdir`/`truncate`/`move` | false claim near 11961 | **Correction:** the static leg is per-`Fn`; the blind spot is the dynamic leg's nonexistent paths. |
| M-19 `file.managed` on a symlink | CONFIRMED (repro) | Medium | not recorded | 0755 on macOS (link perms), 0777 on Linux. Target left stale; run 2 reports converged. |
| M-20 edits drop owner/group | CONFIRMED (repro, group only) | Medium, maybe higher | not recorded | Same shape as the 5.253 outage. Root user change and ACL loss not run. |
| M-21 String-typed modes | CONFIRMED (repro) | Medium | not recorded | `0640` → `-r----xrw-`; `file.serialize` widens 0600 to 0644. |
| M-22 `file.recurse` | CONFIRMED (repro, all three) | Medium | not recorded | `clean` deleting `exclude_pat` matches is the serious part. |
| M-23 setuid dropped | CONFIRMED (repro) | Medium | not recorded | setuid can never be removed by a state. |
| M-24 `git.latest user` ignored | CONFIRMED (trace) | Medium | not recorded | Modern git's dubious-ownership check usually refuses rather than running hooks. `git` has no evidence entry. |
| M-25 `enroll --force` pair | CONFIRMED (trace), **worse** | Medium | not recorded | Every later `enroll` dies (with or without `--force`). Commonest trigger: `--force` on an accepted node is refused outright. |
| M-26 renewal ordering | PARTLY (trace) | Low | not recorded | **Wrong:** not unrecoverable. Delete `node.crt` and re-run `enroll`; the hub returns the stored cert for the matching key. |
| M-27 script with runas | CONFIRMED (trace) | Low-Medium | not recorded | Fails closed. Needs root to run. |
| M-28 oneshot tree kept | CONFIRMED (trace) | Low | false claim near 11076 | |
| M-29 recurse chmod follows symlinks | CONFIRMED (repro), **wider** | Medium | not recorded | **Wrong:** ownership is not safe; `Lchown` protects only the last component, so a parent-directory swap redirects it. Chown half not run. |
| M-30 `~/.ssh` symlink | CONFIRMED (trace) | Medium | not recorded | Needs root to run. |
| M-31 directory umask | CONFIRMED (repro) | Low | not recorded | Converges on run 2. |

### Package, service, firewall, storage and system (M-32 to M-48)

| ID | Verdict | Revised | Ledger | Notes and corrections |
|---|---|---|---|---|
| M-32 package argv injection | CONFIRMED (repro, fake runner) | Medium | not recorded | `dnf_module` already validates for exactly this reason. apt acting on the `-o` **needs a host**. |
| M-33 pkgng/apk ignore pins | CONFIRMED (repro) | Medium (FreeBSD tier 1) | not recorded | `pkg` evidence note does not mention pins. |
| M-34 pin without release | CONFIRMED (repro) | Low | floor path recorded near 17267, exact pin not | |
| M-35 fstab without fstype | CONFIRMED (repro) | Medium | not recorded | Boot effect **needs a host**. |
| M-36 netplan run 2 succeeds | CONFIRMED (repro) | Medium | 5.38 omits run 2 | |
| M-37 pf newline injection | CONFIRMED (repro), partly | Low | not recorded | pfctl may reject the whole load rather than load two rules; **needs a host**. |
| M-38 pf sort order | CONFIRMED (repro) | Medium or slightly lower | contradicts 5.31 | Fails closed. ufw outcome depends on declaration order. |
| M-39 short name vs FQDN | CONFIRMED (repro), deliberate | Low | not recorded | A test asserts it on purpose. |
| M-40 `iptables.save` | CONFIRMED (repro); nftables PARTLY | Low | gap disclosed in 5.51, defect not | |
| M-41 dry run mutates (`pro`, `dnf_module`, `dnsutil.hosts_*`) | CONFIRMED (repro) | Medium | ambiguity recorded and deferred (near 12036) | **Narrower:** inside a state run `module.run` checks test first; the exposure is a direct exec call. `hosts.*` siblings check, `dnsutil` aliases do not. |
| M-42 LVM alias and `-ff` | PARTLY (argv repro) | Medium (alias + force) | not recorded | Whether LVM then wipes the label **needs a host**. `lvremove -f` is Salt parity. |
| M-43 FreeBSD rcvar | CONFIRMED (trace) | Medium, leaning higher | 5.123 covers something else | Live test's script uses `<name>_enable`, so it cannot catch this. **Needs a host.** |
| M-44 BSD/macOS remount and `defaults` | PARTLY | Medium if FreeBSD rejects `defaults` in fstab | not recorded | macOS `mount` rejects both `remount` and `defaults` (run here). FreeBSD **needs a host**. |
| M-45 free-form values in configs | PARTLY (most repro) | Medium | not recorded | modprobe `install` line and path traversal, nftables `flush ruleset`, fstab, pkgrepo reproduced. **Partly refuted:** debconf validates question and value; only `Package` is open. sysctl, sysrc, ufw traced only. |
| M-46 timezone traversal | CONFIRMED (repro, FreeBSD semantics) | Medium | not recorded | Linux with timedatectl likely validates first; FreeBSD always reaches it. |
| M-47 firewall lockout, no `-w` | PARTLY | Low | not recorded | Lockout is a design gap Salt shares; `-w` matters on legacy iptables only. |
| M-48 D-Bus 90 s deadline | PARTLY (trace) | Low | cited as a design bound near 10563 | "Shared" overstated: per operation. Per-state `timeout:` may override. |

### Engine, pillar, gitfs, s3fs, templates (M-49 to M-55)

| ID | Verdict | Revised | Ledger | Notes and corrections |
|---|---|---|---|---|
| M-49 gitfs sweeps served tree | CONFIRMED (repro, real git) | Medium | not recorded | Existing test compares the path string, not that the files exist. |
| M-50 pillar `not S@cidr` | CONFIRMED (repro) | Medium | not recorded (related to 5.136) | Trusting `ipv4` would not fix it; the node controls it. Refuse Subnet terms. |
| M-51 s3fs staleness | CONFIRMED (trace) | Medium | not recorded | The ETag comment is not what the code does. |
| M-52 template vs Jinja2 | PARTLY (halite side repro) | Medium | not recorded | **Wrong:** `10**20` saturates rather than wrapping; `.center()` errors rather than rendering None; tuples render as tuples. jinja2 not run. |
| M-53 gitfs buffers archive | CONFIRMED (trace) | Medium | not recorded | |
| M-54 denylist spellings | CONFIRMED (repro, compiler) | Medium | not recorded | `secret/./x` also bypasses. Policy half traced. |
| M-55 `file_ignore_glob` | CONFIRMED (repro) | Medium | not recorded | Symlink sub-claim not checked. |
