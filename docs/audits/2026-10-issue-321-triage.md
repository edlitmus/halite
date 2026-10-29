# Triage of the issue 321 audit (High and Medium findings)

Source: <https://github.com/edlitmus/halite/issues/321>, an external audit
filed 2026-10-14 against commit `a0d5344`. This file records what was
done to check its 20 High and 55 Medium findings (plus the design note
M-56) on 2026-10-15, at the same commit. It is a working document for
choosing and tracking fixes, not a ledger: each fix still gets its own
`DIVERGENCE.md` section, and a `CHANGELOG.md` entry where an operator
would notice. The status section below was brought up to date on
2026-10-16. The triage under it is still as of 2026-10-15. Nothing holds
this file to the code, so it drifts as work lands.

## Status on 2026-10-16

First checked against `main` at `d8ceba1`, after every PR that
references issue 321 had merged. Rows for work merged since were added
as it landed, the latest being M-06's per-source limit (#441). The
status is as of #441, checked again on 2026-10-19; PRs after it that change only
this file or other documentation do not move it. The rows up to `d8ceba1` were first
rechecked against each PR's body for parts it said it left unfixed, and
the later rows (#375 to #426) were rechecked the same way on
2026-10-18, which added the caveats now in their "Left by the PR" and
"Done" cells. H-02 was fixed
on 2026-10-16, at `9ce448e` (#372). The status comes from the merged PRs
and the commit log.
The fixes were not re-run for this update, except where a row says so.
Issue 321 stays open, because what is below is not finished.

| | Fixed | Not fixed |
|---|---|---|
| High (20) | 20 | none |
| Medium (55) | 54 | 1, half done (M-47) |
| M-56 (design note) | done | runners scoped by their rule's target, stricter than Salt (#426, 5.391); the API does not pass who it acts for |
| Low (section 5) | 23 | 60 bullets listed (the audit says about 90); being taken one at a time |
| Low (section 5) | 18 | 60 bullets listed (the audit says about 90); being taken one at a time |

### Fixed

"Left by the PR" is what the PR itself recorded as found and not fixed.
"Before merge" was a run on real hardware that the PR's author could
not do. Each has been checked to see whether it happened.

| Finding | PR | Left by the PR | Before merge |
|---|---|---|---|
| H-02 extension manifest signed (breaking: re-sign every bundle, move every pin) | #372 | | DIVERGENCE 5.333. On untouched main all eight tamper cases loaded as signed; the real CLI then refused a main-signed bundle by name, loaded a branch-signed one, refused a manifest rewritten after sync, and refused a pin of the old root. Extension packages pass natively on FreeBSD. No node synced from a hub's tree |
| H-01 RBAC target kind (also `{id}` lists, mine, `DispatchAs`) | #344 | the reactor's `wouldAuthorize` output and resume are untested | |
| H-03 archive symlink escape | #333 | | |
| H-04 template limits (+ L-engine-1, L-engine-3) | #332 | a value nested in a loop (`{% set ns.x = [ns.x] %}`) still overflows the stack; chained `set` filters, `truncate` leeway, `zfill`; SPEC 10.2.8's sandbox claim | |
| H-05 background `runas` | #327 | | |
| H-06 `listen` reactions | #347 | | |
| H-07 agentless cache trust | #326 | M-14 (below) | its directory had the login's umask until `chmod`, so under umask 002 a concurrent run, or a missing parent, was refused: fixed in #456 (5.402) |
| H-08 `file.line` regex (+ empty delete) | #343 | | |
| H-09 `ArbitraryCode` flags | #330 | | |
| H-10 token revoke race | #335 | M-07 (below) | |
| H-11 cert revoke race | #338 | M-05 (below) | **done**: a live FreeBSD hub on beastie, with `keys revoke` beside it, at 32 nodes. `main` lost a revocation in each round, and this held. DIVERGENCE 5.323 |
| H-12 compound target depth | #324 | | |
| H-13, M-18, M-41 `--test` mutations (+ beacons, x509) | #345 | | |
| H-14 dotted systemd unit names | #350 | | |
| H-15 mount device identity | #328 | FAT UUIDs whose case differs from the link | |
| H-16 `cpan.module_version` | #341 | | |
| H-17 mdadm create guard | #349 | | **done**: fleet run 38853579313 on the branch. On the `linux` leg the three live mdadm tests PASS, including `TestLiveMdadmCreateRefusesAMemberHoldingAFilesystem` |
| H-18 zfs/zpool properties | #348 | | **done**: fleet run 38854713721 on the branch. On the `freebsd` leg `zfs.filesystem_present`, `zfs.absent`, `zpool.present` and `zpool.absent` conformance PASS |
| H-19 gitfs verifies the served commit | #325 | | |
| H-20 aws-secrets region | #329 | `internal/s3fs` and `internal/awskms` build hosts from regions too (hub configuration, not a node) | |
| M-01 `allow_sls` refuses the highstate | #366 | `allow_mods`'s keyword gap, closed by #367 | **not done**: no job was dispatched hub to node under a narrowed role. The fleet's policies were not read for a role that relies on the old behaviour |
| M-05 superseded serials after a restart | #435, #436, #437 | none | **done** on beastie: `main` accepted the old certificate after a restart, and this refused it (5.392). `keys delete` had the same shape and worse: `main` accepted a deleted node on the running hub; #436 refused it, running and restarted (5.393). #437 put both on the CRL, which `openssl verify -crl_check` then refused, under a number that no longer falls (5.394). Not on the production hub |
| M-07 token lookup reads every token | #438 | `List` still reads every record, off the unauthenticated path | **done** on beastie: a made-up token cost 257 ms at 5,000 tokens on `main` and 9 µs with this; through a running API, 0.26 s against 0.016 s (5.395). Not on the production API |
| M-14 ssh destination injection | #439 | `Match exec` not tried; operator rosters now refused `%` and `@` in a host | **done** on beastie with an OpenSSH 9.5p1 client: a cached node's `fqdn` grain ran a command on the hub on `main`, both as an ssh option and through a `ProxyCommand` shell, and was refused with this (5.396). No distribution's older ssh run |
| M-06 unbounded enrollment | #378, #441 | no connection cap; many addresses still fill the pending bound, 32 of them at once | **done** on beastie: one address made 1,024 names pending in 29.9 s on `main`, and 32 with #441, whose real node then enrolled under `--wait` (5.338, 5.398). Not on the production hub, not over IPv6 or NAT |
| M-02 signed-job replay | #339 | | on FreeBSD since: 0.75–0.82 ms per signed job on beastie's ZFS (5.328) |
| M-04 typed job signing (breaking) | #337 | | live mixed-version run on beastie (5.330) |
| M-08, M-09 login backoff by (source, name) | #352 | | |
| M-10 login timing, equal work | #353 | | |
| M-11 1 MiB stream line | #379 | events: an over-long one is streamed as a stand-in; no node-side stand-in, so an older hub can still send a node an over-long job line | **done** (5.339); not run on a live hub |
| M-12 returns from non-targets | #380 | a chunk other than 0 is refused | **done** (5.340); not run on a live hub |
| M-13 one node erases history | #381, with #380's chunk refusal | a return is held to 16 MiB | **done** (5.341): `node_event_rate` and `node_event_burst`; not run on a live hub |
| M-15 relay claims node IDs | #382 | 1.9 corrected | **done** (5.342): `relay_subordinates`, and nothing enrolled directly can be claimed; no real relay run |
| M-16 webhook ingress (`auth: mtls` now refused) | #359 | | |
| M-17 `/v1/orch` kwargs; `allow_mods` keyword | #367 | | |
| M-24 `git.latest user` ignored | #390 | | **done** as root on beastie (5.350) |
| M-25 `enroll --force` pair | #391 | a refusal drops the new key | **done** against a real hub in-process (5.351); M-26, the same shape in renewal, by #400 |
| M-27 script with runas | #392 | `cmd.script` with a `salt://` source not run as another account | **done** as root on Ubuntu and beastie (5.352) |
| M-42 LVM alias with `-ff` (also `pvremove` and `lvmFindPV`, which compared the same way) | #393 | `lv_remove` and `lv_absent` still pass `-f` (Salt parity) | **done** as root on Ubuntu against real LVM, which does not refuse `-ff` (5.353) |
| M-45 free-form values (modprobe, pkgrepo, nftables, fstab; sysrc, sysctl, debconf) | #394, #395 | ufw was already held by M-37; sysctl and debconf not driven against the real tools | **done** (5.354, 5.355): nft's reading of `;` measured on Ubuntu, sysrc's on beastie; the modules not driven against the tools |
| M-53 gitfs buffers the archive | #396 | the file count and byte limits apply as entries arrive; the limits are constants, not configuration | **done** (5.356): main was killed buffering 3 GiB; not run against a real remote or on the hub |
| M-51 s3fs staleness | #397 | truncation at the bound, deleted objects, in-place writes, colliding sanitised names; multipart ETags not exercised | **done** (5.357) against the package's fake S3; not run against S3 |
| M-03 govulncheck in CI | #398 | `make vuln` pinned to the same version | **done** (5.358): a `vuln` job; `make vuln` run on Ubuntu and under BSD make on beastie |
| M-26 renewal install order | #400 | `enroll --force`'s pending install uses the same order | **done** (5.360) against a hub in-process; no real crash or full disk |
| M-34 RPM pin without a release | #401 | RedHat and Suse only; the epoch still counts, as in Salt; `dnf install` of a bare version, with the package absent, not run | **done** (5.361) against Salt 3007.1's rule; no RPM host |
| M-40 `iptables.save` (and `nftables.save`) | #402 | nftables' counters, the same defect | **done** (5.362) against iptables 1.8.10 and nftables 1.0.9 in a network namespace |
| M-48 D-Bus 90 s deadline | #403 | method calls keep 90 s; a job waits for systemd or the caller | **done** (5.363) as root on Ubuntu with a 95 s unit |
| M-52 templates vs Jinja | #399, #405–#410 | chained comparisons, `**`, `~`, `tojson`, `dict()`, `round`, `str.format` specs, float rendering, `\|format`; `tojson`'s expected values come from Salt's source, Salt not run; `!r`, `!s` and attribute fields in `str.format`; `*` widths in `format` | **done** (5.359, 5.365–5.370), each against jinja2 3.1.2 or Python 3.12.3; four corpus cases disagree with jinja2 and are left (three tuple cases; `stmt/autoescape-is-a-no-op`, a no-op on purpose); `dict()`'s keyword order left. The generator/`halite.json` drift on `filter/yaml_encode-plain` that #408 found was settled by #414 (5.374): Salt double-quotes, and the generator now reproduces the JSON byte for byte |
| M-28 agentless `oneshot` tree | #358 | | |
| M-19 `file.managed` on a symlink | #386 | every file state, not `writeAtomic`; `follow_symlinks: False` not offered; `copy`, `prepend`, `blockreplace`, `comment`, `uncomment` not each run through a link; no Linux root run | **done** as root on beastie (5.346) |
| M-20 edits drop owner/group | #383 | in `writeAtomic`, so every writer; `file.copy` and `restore_backup` not driven; no Linux root run | **done** as root on beastie (5.343); ACLs and xattrs still lost |
| M-21 string-typed modes | #384 | `file.serialize` keeps a file's mode | **done** as root on beastie (5.344) |
| M-22 `file.recurse` | #387 | rewrites keep their mode; created directories get `dir_mode`; an existing directory whose mode differs from `dir_mode` is not corrected; the `E@` regex form is unsupported | **done** (5.347); not run on a host |
| M-23 setuid and friends | #385 | | **done** as root on beastie (5.345) |
| M-29 recursive chmod follows links | #388 | ownership too, through the same root; the ownership half not run against a swap (needs a second account) | **done** (5.348); the race made certain in tests, not run as root on a host |
| M-30 `~/.ssh` symlink | #389 | the list functions too; a home that is itself a link is trusted; macOS not run | **done** as root on Ubuntu and beastie (5.349); main gave a root file to the account |
| M-31 directory umask | #385 | parents too | **done** as root on beastie under umask 027 (5.345) |
| M-32 package name injection | #331 | | |
| M-33 pkgng/apk pins | #375 | `pkg.installed` checks every provider's pin after installing | **done** on beastie (5.334). **Not done**: apk still drops the pin from its argv, so a pinned apk package now fails rather than converging; nothing here runs Alpine |
| M-35 fstab without fstype | #377 | | **done**: FreeBSD and util-linux both refuse the line (5.337); no boot was tried |
| M-36 netplan refused file | #356 | | |
| M-37 firewall value validation | #355 | | **done**: beastie's hub serves no firewall states, so no `from`/`to` there meets the new refusal. pf parsed every accepted form (`pfctl -n`). Other hubs' trees not searched |
| M-38 pf anchor order | #354 | | |
| M-39 hostname short vs FQDN | #357 | | |
| M-43 FreeBSD rcvar | #376 | a script with no rcvar is refused | **done** on beastie and on the `freebsd` leg's 15.1 VM (5.335) |
| M-44 BSD `defaults` and remount | #377 | `mount` raised to hardware | **done** on beastie and Ubuntu (5.336). FreeBSD rejects `defaults` in fstab too, so the revised "Medium if" holds. Not run on macOS or the other BSDs |
| M-46 timezone traversal | #336 | | |
| M-49 gitfs keeps served trees | #346 | two remotes, one unreachable: that one stops being listed (DIVERGENCE 5.293); a partly refused remote drops its environment | |
| M-50 pillar `S@` targets | #334 | | beastie's pillar root is empty, so nothing there meets the refusal (5.329) |
| M-54 SLS name spellings | #340 (state), #362 (policy) | | |
| M-55 `file_ignore_glob` fnmatch | #342 | | |
| Section 1.2, the toolchain's ten advisories | #360 (go1.27.2) | M-03, by #398 | |

Changes made while doing these, not findings of the audit: #369 holds
an extension's `Version` to its code, and refuses a binary whose
handshake differs from its signed manifest (DIVERGENCE 5.325). That
does not close H-02 (#372 below does). #370 fixed `internal/sshexec`'s
tests under a umask of 002 (5.326). #373 fixed an ETXTBSY race in one of
those tests, which failed CI on #372 (5.332).

### Not fixed

Severities are this file's revised ones, from the tables below.

**High.** None. H-02 is fixed (#372, above).

**Medium, half done.**

- **M-47**, Low: #440 gives every iptables command `-w 30`. 5.364 had
  left it because 1.8.10 waits unasked; Ubuntu 22.04's 1.8.7 on the
  legacy backend fails at once, exit 4, and `main`'s `iptables.append`
  did (DIVERGENCE 5.397). Not done: the lockout half, a firewall state
  that would leave the node unreachable. That is a design gap Salt
  shares (5.364), not a defect with a fix.

**Medium, not started.** None. The four marked FreeBSD, which came
first because FreeBSD is tier 1 and carries about 80% of production,
were fixed first.

**M-56, runners are not scoped.** Done by scoping, stricter than Salt
(#426, DIVERGENCE 5.391): a runner rule's target is the nodes its runners
reach, and SPEC 23.5 says so. Not done: who the API acts for is not
passed, so a run made through the API is visible to the API's other
callers; a webhook's runner call carries no narrowing; not run against a
live hub.

**Low (section 5 of the audit).** The audit lists 60 bullets and says
there are about 90. They are being taken one at a time, from the
section 5 text in the issue's comment; each that is fixed is listed
here. The rest have not been triaged, and some may have been fixed in
passing by the PRs above; that has not been looked for.

| Bullet | PR | Done |
|---|---|---|
| `ps` user filter never matches names longer than 8 | #417 | 5.376; FreeBSD already printed them whole; BusyBox `ps` not run |
| `file.rename force` deletes the destination before the move | #418 | 5.377; onto itself too; Salt also deletes first, so only the failure path differs; `movePath` still copies on any rename error, not only EXDEV; not run as root or on FreeBSD |
| `openssl.pkcs12_export` writes under the umask | #419 | 5.385; an existing bundle was rewritten in place; LibreSSL and FreeBSD's base openssl not run |
| `file.recurse` joins listed paths with no `..` check | #420 | 5.386; the prefix was matched as a string too; `cacheFile` accepting a body with no published digest not looked at; no hub run |
| policy `stringList` drops non-string items | #421 | 5.387; YAML 1.2 reads `on` as a string, so that example does not occur; `matchPrincipal`'s `path.Match` and `/` in principals not fixed, done in 5.465 |
| TOTP codes replayable inside their window | #422 | 5.388; in memory, so a restart forgets; LDAP and OIDC logins unchanged |
| `readJSON` accepts trailing non-JSON | #423 | 5.389 |
| API listener does not restrict curves in FIPS mode | #424 | 5.390; go1.27.2 refuses X25519 under fips140=on anyway, measured; a comment saying otherwise corrected; the new test passes against the old listener too, so it holds the property, not the change |
| `cmd.shell`/`exec_code`/script functions ignore an unparseable `timeout` | #411 | 5.371; `network.connect` too; `cmd.script` with a `salt://` source not run |
| `check_cmd` temp file chmod 0644 | #412 | 5.372; a `runas` checker gets the file; Windows and beastie not run |
| `ssh_auth.present` newline in `comment`/`options` | #413 | 5.373; `enc` and `name` too, and `ssh_known_hosts`; a comma in a known_hosts `name` left (ssh's own syntax); not run against sshd |
| `yaml_squote` corrupts multi-line values | #414 | 5.374, against PyYAML 6.0.1; `yaml_dquote` spells non-ASCII differently from PyYAML |
| `sysrc.get`/`present` treat any non-zero exit as unset | #415 | 5.375, measured on beastie; the hostname read too; `jail.go` reads through `sysrcGet` too; no jail test run |
| SigV4 canonical URI for S3 keys with `: @ + = $ &` | #444 | 5.401; held to AWS's published `PUT /test$file.text` vector, not to S3 itself; S3-compatible stores not tried |
| Redaction: overlapping secrets leave a tail in clear | #462 | 5.407; the multi-line secret in `render.Error`'s numbered window is left for the owner (two options in the entry); `log.ScrubValue` and `error` fields separate |
| `log` field holding an `error` not scrubbed | #466 | 5.408; latent, no call site passed one; JSON wrote `{}`; `fmt.Stringer` fields left (a `*url.URL` still prints its password), done in 5.462 |
| `log` field that is a `fmt.Stringer` not scrubbed; `*url.URL` prints its password | #497 | 5.462; console leaked both, measured; scrubbed text replaces a Stringer only when it held a secret, so durations stay numbers; URLs always `Redacted()`; latent, no such call site found; non-Stringer structs still unscrubbed in JSON |
| Docker bases tag-pinned, not digest-pinned | #497 | 5.463; eleven references pinned to index digests read from the registries; buildpolicy guard; nothing built locally (no runtime), CI builds fleet, rpm and saltdiff; `race` and `zfs` images not built |
| Multi-arch: apt `ListPkgs` keys on bare `${Package}`, last arch wins | #497 | 5.464; captured in a bookworm container with i386: Salt 3006.28 and 3008.3 key foreign packages `name:arch`; halite read `hello` as installed when only `hello:i386` was; native arch from dpkg's own line; breaking on multi-arch hosts; no live test |
| policy `matchPrincipal` uses `path.Match`, so `/` defeats `*` | #498 | 5.465; reached by hook principals (`hook:github/push`); failed closed, never granted more; now Salt's fnmatch, which also widens `hook:x/*` to nested paths; `matchGlob` for functions and SLS left; no live hub |
| `make dist` sums `*` of `dist/` | #498 | 5.466; a stale binary and a stray file were summed, measured on beastie with BSD make; now refused by name, not deleted; one target only, `release.yml` not run |
| Requisite globs (`test: 'b*'`) refused (found capturing 5.461, not an audit bullet) | #498 | 5.467; Salt 3006.28 and 3008.3 captured on eight cases; module globs match ID or name, bare globs IDs; no match refused; `fnmatch` moved to its own package; SPEC 11.3's "ambiguity error" for a bare ID corrected; `sls:` and `_in` globs not captured |
| Merge: `__replace__`/`__overwrite__` at the top of a later source wipes the pillar compiled so far | #499 | 5.468; the premise was false: Salt 3006.28 and 3008.3 have no such directives and kept them as data under all five strategies, captured; halite's now keys too; breaking; Salt's `aggregate` (yamlex, top level only) found to differ and not changed |
| `cacheFile` accepts a body with no published digest (from the `file.recurse` bullet) | #499 | 5.469; reached by `hash_type: sha3-256`, offered by SPEC 13.5 and the reference and known to neither side, so the hub sent every file without a digest; one hash list, sha3-256 added, bad `hash_type` refused at hub start (exit 78, measured), hub refuses to send an unhashed file, node refuses a digestless body; in-process hub only, FIPS mode not run |
| `git.remote_get` passes `remote` with no `--` (left by 5.414) | #499 | 5.470; nothing could run (`get-url` takes only `--push`/`--all`), measured with git 2.43; a real remote named `-dash`, which git makes, was unreadable; now after `--`; 5.414's "no legitimate dash spelling" false for a remote, refusal kept; FreeBSD git not run |
| L-engine-1: `cidr_subnets` wrong for IPv6, can panic or spin | #332 | 5.312, with H-04; checked against Python's `ipaddress`; listed here 2026-10-28, not re-run |
| L-engine-3: `value.DecodeJSON` has no depth limit | #332 | 5.312, with H-04; 10,000 levels, `encoding/json`'s own; real on go1.26.6, already bounded by go1.27's json/v2; listed here 2026-10-28, not re-run |
| L-engine-9: gitfs `clone`/`verify-commit` without `--`; a `-foo` branch read as an option | #325 | 5.292, with H-19; listed here 2026-10-28, not re-run |
| `atomicfile.Write`: mode and SELinux label replaced by constants (the half 5.446 left) | #500 | 5.471; a 0600 sysctl.conf and a 0640 file behind a link came back 0644, measured; mode now kept, caller's mode only for a new file; netplan keeps forcing 0600; Salt writes in place (read, not run); SELinux label and other xattrs not kept (no SELinux host); Ubuntu temp dir only |
| policy `matchGlob` (functions, runners, kwargs, SLS) still `path.Match` (left by 5.465) | #500 | 5.472; not about `/` (SLS names are canonical dotted); `[!w]` meant the set `!`,`w`: `deny_sls: ['[!w]*']` allowed `secret` and `pkg.[!r]*` granted `pkg.remove`, measured; now fnmatch; Salt's regex fallback deliberately not taken; production policy on beastie has no such pattern; no live hub |
| OIDC: no single-flight for concurrent discoveries (left by 5.419) | #500 | 5.473; 20 concurrent logins made 20 discovery requests to a slow fake issuer, measured; now one shared fetch, detached from the first caller under a 30 s bound; 30 runs under `-race`; key-set refresh not looked at; no real IdP |
| OIDC key set never refreshed on its schedule; mid-fetch logins refused (found checking 5.473, not an audit bullet) | #PRNUM | 5.474; `Refresh` had no caller, so a withdrawn key verified until restart, measured 30 min past its window; 9 of 10 logins during a rotation fetch refused; expiry now checked at use, held keys kept while the provider is down, waiters share the fetch; 30 runs under `-race`; no real IdP |
| aws-secrets: node `name` nests over a hub secret | #468 | 5.409; extension 1.1.1; cache eviction (bounded by what exists) and the `node_grain_allow` default (owner decision) left |
| `template.DirLoader` follows symlinks | #469 | 5.410; test and dev use only; Linux only |
| `state/order.go` is O(n²) | #471 | 5.411; heap, same order on 20,000 random graphs with ties; self-requisites still dropped silently (needs a Salt check) |
| Local signer signs any digest length | #473 | 5.412; extension 1.0.1; the host only sends 32 bytes; key-file mode check left (owner policy) |
| `migrate/skeleton.go` names unescaped in generated Go | #464 | 5.413; a `__virtualname__` produced a stub that compiled and called `os.RemoveAll`; `migrate --bridge-skeleton` not run end to end |
| `make vuln` runs `govulncheck@latest` | #398 | M-03, 5.358 |
| `lvm.lvresize` calls a rounded-up size a shrink | #475 | 5.430; measured on the fleet `linux` leg (LVM2 2.03.16): failed before, passed after; `lv_present` warned too; only 4 MiB extents |
| `iptables.insert` does not enforce `position` | #474 | 5.428; docs corrected, behaviour unchanged (moving a live rule is a decision); Salt parity recalled, not checked |
| `swap.on`/`off` compare the caller's path with the kernel's keys | #472 | 5.418; measured and fixed on beastie (FreeBSD `swapctl -l`), live test passed there; Linux not run; GEOM labels not links |
| Multi-arch: `dpkg.set_selections` diff compares bare names with `name:arch` | #470 | 5.417; happens on a single-arch host too (941 qualified lines on Ubuntu 24.04); apt `ListPkgs` keyed on `${Package}` not fixed (needs a foreign-arch host to capture), done in 5.464 |
| `ufwChanged` unchanged when v4 skipped and v6 added | #467 | 5.416; established from ufw 0.36.2's source, fixtures composed, not captured (ufw needs root) |
| `shutdown -r +N <message>` message starting with `-` read as an option | #465 | 5.415; systemd 255 measured with no bus (`-h` accepted); FreeBSD unaffected, read from shutdown.c, not run; refused rather than `--` |
| websocket `writeFrame` unsynchronised, frames interleave | #445 | 5.406; reproduced over `net.Pipe`; no real client connected |
| CI's FreeBSD Go tarball and saltdiff's Salt onedir fetched unverified | #446 | 5.403; on the PR, CI's FreeBSD leg, the fleet FreeBSD leg and all three saltdiff builds ran the checks and passed; a mismatch was shown refusing on beastie and under dash, not inside CI's VM or a Docker build |
| `status.uptime` `extractBootSeconds` takes the first `sec = ` | #447 | 5.404; latent, FreeBSD prints `sec` first (beastie); macOS not run |
| `file.touch` documents updating times and changes nothing | #453 | 5.405; the execution function only; the state still only ensures existence; Salt's `atime`/`mtime` not added; FreeBSD not run |
| `git.latest` passes URL, rev and remote with no `--` | #459 | 5.414; not neutralised for `remote` on an existing checkout: the command ran; `git.remote_get` left; FreeBSD git not run |
| OIDC: redirects to `http://` followed, discovery endpoints not required https, discovery re-run while the IdP is down | #448 | 5.419; the token endpoint redirect sent the client secret to plaintext, measured; fake issuer only, no real IdP; no single-flight for concurrent discoveries |
| LDAP `ParseFilter` finds `>=`/`<=`/`~=` in the value | #451 | 5.420; fake directory only; the `MapRoles` case-fold half was left then, and is made exact by 5.449 |
| Actions pinned by mutable tag; VM checkouts keep the token | #449 | 5.421; the token reached the VM as a `_temp` credentials file rsync copies, not in `.git/config`; fleet.yml ran on the PR, release.yml not run |
| `job.ID.Valid()` accepts non-digits | #452 | 5.422; only the `+` in the fraction got through `time.Parse`; no stored jid checked |
| CI's `cancel-in-progress` cancels pushes to main | #450 | 5.423; at least eleven main commits had no result, #446's among them; pushes now grouped by commit; no push to main run under it yet |
| Provenance guidance `--repo` only | #454 | 5.425; checked against v0.1.1's attestation; no branch attestation shown accepted; repository settings for dispatch not changed (admin) |
| buildpolicy text greps with gaps (cross/fips-cross flags, `matrix.go`, dead `ext.Confine`, attest wording) | #457 | 5.426; each gap reproduced and closed; still text checks; flow-form YAML permissions not read |
| systemd node unit `ProtectKernelModules=yes` | #460 | 5.427; worse than the audit said: the module tree is hidden, so `kmod` reads answered False; measured under a user manager, not as root under the system manager; the hub unit's writable PKI is by design and unchanged |
| leaks: `job.Cache.locks` never freed | #455 | 5.431; no hub run long enough to see it; the bullet's `certreload` part not changed |
| leaks: `eventbus.Wait` a channel per idle stream per 25 s | #458 | 5.432; the hub's stream also registered its wait after its read, missing an event for up to 25 s, fixed with it; a closed stream still leaves one channel; not run on a live hub |
| logs: `oidcCallback` error text unbounded; `oidc login started` logs `state` | #461 | 5.433; the state let a log reader cancel a login, not finish one; other unauthenticated paths not looked at |
| `keys operator create --admin` writes a root-owned 0600 policy | #463 | 5.434; reproduced and fixed on beastie with a throwaway hub; a directory `--admin` creates itself stays root's; Linux not run |
| `hubError` relays raw hub error text to API clients | #482 | 5.435; the SPEC 22.3 citation is weaker than the audit said (it names stack traces only); the 403 refusal text is kept on purpose; `hook.go`'s 502 fixed separately (5.441); not run on a live hub |
| `ps.pkill` has no guard against signalling PID 1, the node agent or sshd | #483 | 5.436; init and the node's own pid refused, `force: True` overrides, signal 0 exempt; sshd deliberately not guarded; tests are a scripted procps table and skip on FreeBSD; nothing real was signalled |
| `maxProviderOutput` does not bound memory; the runner buffers all stdout | #484 | 5.437; fixed for grain providers via a new `Command.MaxOutput`; `cmd.run` and every other command deliberately still unbounded, a decision not made; tests unix-only, run on Ubuntu only |
| archive entries read whole into memory, no entry-count or total-size limit | #485 | 5.438; 250,000 entries and 8 GiB per archive added, numbers chosen not derived; the memory half was done in 5.454; not run beyond unit tests |
| staticcheck results (1.3) | #493 | 5.456; dead code removed (121 hits to 108); the "bug-shaped" hits were harmless; `wintask` is used on Windows, so the audit's "unused XML reader" is true only off Windows; `ST1005` (61), deprecations and simplifications not done |
| cron comments claim Salt's crontabs are recognised (found by `U1000`) | #493 | 5.455; they are not (different markers, nothing reads Salt's); comments corrected, adoption not implemented |
| `disttar` packages untracked files | #493 | 5.457; only git-tracked files under the source directories; `make dist` not run; the `make dist` sums-`*` half not changed, done in 5.466 |
| enrollment token only as `--token <secret>` on argv, and `halite_node_token` on a command line | #486 | 5.439; `--token-file` and `halite_node_token_file` added, `--token` kept with a stderr note; measured with `ps` on Linux; the rc script was only `sh -n` checked, not run on FreeBSD; not enrolled against a real hub |
| `pkg.installed` reports Changed without verifying presence or the pin | #487 | 5.440; the pin half was M-33, this is presence; shown only against scripted managers, no real manager made to install nothing |
| `hook.go` 502 relays the runner's error text to the webhook sender | #488 | 5.441; sender now gets a fixed sentence, detail logged; not run against a live hub |
| `merge.go` silently drops `dst` past depth 100 | #488 | 5.442; bound raised to 10000, so the drop moves rather than goes (`Merge` has no error to report it with); stack cost reasoned, not measured |
| `snap.installed` compares the channel as text | #488 | 5.443; short forms (`stable`, `5.0`) normalised before comparing; the rule is from memory of snapd, not checked on a machine with snapd |
| `certreload.Load` re-reads both files on every handshake | #489 | 5.444; throttled to once per 5 s; contents still compared; no flood measured |
| `range()` materialises 10M items with no byte budget | #489 | 5.445; worse than the audit said (24 bytes an item, and the limit was per call: 30 calls allocated 6.2 GB); now counted across the render; only `range()` charged |
| `atomicfile.Write` replaces a symlinked target, constants for mode and label | #489 | 5.446; the symlink half for the system config modules; mode, SELinux label and netplan not done; Ubuntu temp dir only |
| `dnf_module` reports "no streams" on dnf5 (L-pkg-31); `TestLiveDnfModuleReadersAgree` under-gated | #490 | 5.447; dnf5 refused by name from `dnf --version`; dnf5 and dnf 4 outputs captured from containers; no module state driven on dnf5; no Fedora fleet leg |
| `TestLiveAuthselectReads` expects `minimal`; docs say `minimal` | #490 | 5.448; the profile is chosen from `authselect list`; list and list-features captured on fedora:42; the tests were not run on Fedora |
| `cmd.run` buffers all output in memory (`maxProviderOutput` for everything else) | #491 | 5.450; 64 MiB default, `max_output` overrides, 0 removes it; the operator's decision; Ubuntu only |
| LDAP `MapRoles` folds group-name case | #491 | 5.449; exact match, as Salt's eauth; breaking for an estate relying on the fold; fake directory only |
| `ps.pkill` does not guard sshd | decision | 5.451; Salt guards nothing, so sshd stays unguarded; the PID 1 and self guard (5.436) stays |
| M-47 firewall lockout | decision | 5.451; do nothing, as Salt; `-w 30` (#440) is the only change |
| jail.conf: a dot in a name becomes an underscore (UNVERIFIED) | #492 | 5.452; **not true**: measured on beastie, FreeBSD 15.1, the dot is kept and means a child jail, so starting fails with jail's own message, which now has the reason added |
| `netplan.managed` replaces a symlinked file | #492 | 5.453; follows the link now; Ubuntu temp dir only |
| archive entries read whole into memory | #492 | 5.454; streamed; 96 MiB entry allocated 236 MiB before; `TotalAlloc`, not peak RSS |
| disttar: rc.d scripts archived 0644 | #496 | 5.459; v0.1.1 shipped them so, measured; mode taken from git's index; `make dist` and the release workflow not run |
| `permutations`/`combinations` cap by count only | #496 | 5.460; the count was per call, as `range()`'s was (5.445): three calls of 4.2M items rendered; now charged to the render; list repetition (`[x] * n`) had the same gap and is fixed with it; counts, not memory; `batch`/`slice` not charged |
| `state/order.go` drops self-requisites silently | #496 | 5.461; Salt 3006.28 and 3008.3 refuse eight self forms, captured; halite ran seven and skipped one; now a one-hop cycle, breaking; `listen`/`onfail`/`use`/`_any` not captured |
| Info: `service.mask`/`unmask` and `dnsutil.hosts_*` declare no `Privileges` | none needed | `service.mask`/`unmask` gained `root` in #345 (5.308), the day after the audit; `dnsutil.hosts_*` has declared it since it was added on 2026-09-15, so that half of the bullet was not true |

**Also from section 1.** The staticcheck results (1.3) are taken in 5.456, in part.
The two live tests with wrong assumptions on current distributions (1.1)
are fixed in #490 (5.447, 5.448).

**Ledger and comment statements found to be false** (the table below).
Each was to be corrected with the fix that touches it. The row for
M-15 (DIVERGENCE 1.9) is corrected by 5.342 (#382), which the section now carries. The H-02 row's comments (declarations are
signed) are true since #372, and `Runtime.Add`'s says when it became so. The others were not re-checked
for this update.

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
| SPEC 25.1 and 25.2, 6.2's health row, handbook 12 and 28 | no application parsing before authentication on the hub port; health is the only route without a certificate | `POST /v1/enroll` parses a CSR for an anonymous caller, by design | none; found fixing M-06, corrected in #442 (5.399); the enroll error leak that run found was fixed in #443 (5.400) |

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
| M-06 unbounded enrollment | CONFIRMED (repro), wider | Medium | 5.284 mentions pending records as operability only | **Wider:** in token mode a bogus token still leaves a pending record that locks out the real node. Bounds by #378 (5.338); per-source limit by #441 (5.398). |
| M-07 token lookup cost | CONFIRMED (repro) | Medium or higher | not recorded | ~4.2 s per garbage request at 5,000 tokens on macOS (the audit measured 78 ms). |
| M-08 unbounded login username | CONFIRMED (trace) | Low-Medium | not recorded | PBKDF2 slot limit slows filling the table. |
| M-09 backoff is a lockout | CONFIRMED (simulation) | Medium | contradicts 5.232 | 105 requests per day held a name blocked for the whole day. 5.232 already records the cross-name half. |
| M-10 enumeration by timing | CONFIRMED (repro) | Low-Medium | contradicts 5.232 | ~121 ms vs ~45 ms. |
| M-11 1 MiB line limit | CONFIRMED (trace), corrected | Medium | not recorded | **Correction:** one reconnect per node per oversized job, not a loop; the job is silently lost. Event half not reproduced. |
| M-12 returns from non-targets | CONFIRMED (trace) | Low-Medium | not recorded | `Missing()` filters correctly; the API's synchronous count does not. |
| M-13 flood erases history | CONFIRMED (trace), easier | Medium | not recorded | Caller-chosen `Chunk` lets one node write unlimited 64 MiB files against any existing JID. |
| M-14 ssh option injection | PARTLY (repro on OpenSSH 10.3) | Medium if the hub's ssh is older than 9.6, else latent | not recorded | halite's argv shape is rejected by 10.3; older OpenSSH **needs a host**. scp path is not a vector as written. Only the cache roster is node-controlled. Reproduced since, with OpenSSH 9.5p1 on beastie, through both the option and a `ProxyCommand` shell, and fixed (#439, 5.396). |
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
| M-47 firewall lockout, no `-w` | PARTLY | Low | not recorded | Lockout is a design gap Salt shares; `-w` matters on legacy iptables only. Since measured: 1.8.10 waits unasked, 1.8.7 (Ubuntu 22.04) fails at once; `-w 30` added (#440, 5.397). |
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
