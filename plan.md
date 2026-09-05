# Remaining work

A plan for what is left between this build and SPEC section 32's phase 6
exit criteria, written by comparing SPEC.md against the code as it
stands.

**Method.** Every count below was measured against the build rather than
read out of `docs/DIVERGENCE.md`: the module and state registries were
interrogated through `sys.list_modules` and `sys.list_state_modules`, the
pending-platform table was counted, the metric families were counted by
the audit that guards them, and each named feature was traced to the line
that implements or refuses it. Where a claim here is a count, the command
that produced it is one a reader can run.

**Date of measurement:** 2026-09-05, against `3aecb0f`, on
windows/amd64 with Go 1.26.6. The previous revision was written on
2026-09-04 against `c6a9656`; twenty-one commits have landed since, and
section 0 says which of its findings are now closed.

**A note on this file.** Nothing enforces it. `internal/specaudit`
guards SPEC.md, `docs/DIVERGENCE.md` and README.md against the
registries; `internal/docsaudit` guards the generated pages. This
document is outside both, which is why the last four revisions have each
opened by correcting the one before. Section 3.5 says what would fix
that, and it is the same answer as for everything else here.

---

## 0. Where the project actually stands

Phases 0 through 4 are complete. Phase 5 is most of the way built.
Phase 6 has started in one place only — metrics — and that arrived
sideways, as part of phase 5's observability rather than as phase 6 work.

| Phase | State |
|---|---|
| 0. Foundations | Done. |
| 1. Local state and pillar | Done, within a module inventory that is about half of what SPEC 15 names. |
| 2. Hub, transport, enrollment | Done. Outstanding: external pillar, `halite-hub files`, return chunking, the event-bus indexes. |
| 3. The automation loop | Done. Outstanding: `salt.parallel`, the queue runner, live pause/resume, beacons and schedules through pillar, the node-side bus. |
| 4. API and integration | Done, including the bridge protocol and sandbox. Outstanding: no reference bridge extension ships. |
| 5. Breadth | gitfs, s3fs, agentless mode, relays and the FIPS artifact set are built. Windows parity is largely done and verified on a real host; macOS has providers but no module set. **59 of SPEC 15.3's 65 platform modules, 21 of SPEC 15.2's core execution modules and 18 of SPEC 15.5's core state modules remain.** |
| 6. Hardening to 1.0 | Barely started. Metrics are nearly complete (§3.2). No benchmarks, no chaos suite, no packaging, no CI, no node evidence, no detached signing, no render sandbox. |

### 0.1 What the previous revision listed and what has closed

Its section 2.1 is closed and stays closed: `timezone`, `environ`,
`mount`, `zpool`, `beacon` and `schedule` all ship with both halves, and
the registries confirm it — `mount.mount`, `timezone.set_zone`,
`environ.setval` and `zpool.create` are all callable.

Its section 3.2 is nearly closed, and was the largest single correction
this revision had to make. It said eleven of thirty-two metric families
were unregistered, that all three extension counters were among them,
and that "the node exposes no metrics at all". Two of those three
statements are now false: **thirty of thirty-two are registered**, the
three extension counters among them, and a node serves `/v1/metrics` on
`metrics_listen` when an operator asks for it.

Two of its items were not closed but were mis-stated, and are corrected
below: the state-module arithmetic in §2.2, and `file`'s function depth
in §2.4.

### 0.2 One thing the previous revision said that was not quite true

It said `make check` passes. Two corrections, and the second is the
interesting one.

For a day it did not: §1.1 is what broke and why it stayed broken.

And **`make check` had never completed on this Windows host**. Six of its
seven legs pass here — `fmt-check`, `vet`, `build-all` across all eight
targets, `test`, `policy` and `fips-test`, all re-run for this revision.
The seventh, `race`, sets `CGO_ENABLED=1` on purpose, and the detector on
windows/amd64 needs a C toolchain this machine does not have:

```
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

So the platform that found four defects in §1 was also the platform where
the concurrency surface went unchecked. **`make racecheck` closes that**:
it runs the `race` recipe verbatim in a container that has a compiler,
as an unprivileged account. What it found on its first afternoon is
§1.2, and DIVERGENCE 4.8 is the full account.

It is deliberately not part of `check`. `check` has to work on a machine
with no network and no Docker, which is the same machine a release is
built on with `GOPROXY=off`; a host with a compiler should run `make
race` directly and get the same answer faster.

There is also no `make` on this host at all, so the legs above were run
by hand with the environment each recipe sets. The Makefile is written
for BSD make, and DIVERGENCE 6.2 recorded that it had never been run
under GNU make; it has now, in a container — all 33 targets expand under
GNU Make 4.4.1 and `racecheck` runs through it end to end. That says the
file parses there, not that every target's result matches between the
two makes.

---

## 1. What running on Windows established, and what it did not

The suite had never been run on Windows. On the first native run it
failed 80 tests across 12 of 55 packages; it then passed all of them.
`docs/DIVERGENCE.md` section 4.6 is the full account. Three things from
it belong in a plan rather than a ledger:

**Three of those failures were defects on every platform.** Six packages
each had their own copy of write-a-temp-file-and-rename, all six racing
on Windows and losing 12 of 200 replaces under contention; the event bus
reopened its segment after `Close`, so a closed bus went on accepting
events forever; and a grain provider that timed out was waited out for 61
seconds against a 300ms bound. None of these was a Windows bug. They were
bugs that only a platform without unlink-while-open could show.

**The lesson generalises.** macOS builds and has providers, and its test
suite has never been run there either. FreeBSD is the development
platform and its five modules are unbuilt. Linux arm64 has never run the
suite. Every one of those is the same shape of risk this was, and the
cost of finding out is one afternoon each.

**What Windows still lacks** is the module set, not the platform work:
`win_dacl`, `win_service`, `win_registry` and `win_task` ship; the other
fourteen do not, and there is no user or group provider, so
`user.present` has nothing to reach.

### 1.1 And the fourth defect, found a day later

`go test ./...` failed on Windows, deterministically — three failures
out of three under `-count=3`, so not a flake:

```
--- FAIL: TestAnExtensionCallIsObserved (1.60s)
    observe_test.go:101: a call was observed as taking 0s
```

The cause is the same shape as the three above: **Go's monotonic clock
on Windows has a granularity of about 500 µs on this host**, measured
directly, and an extension call that reuses an already-warm pooled
process returns inside that. `time.Since` read exactly zero against an
assertion of `took > 0`.

It arrived with `dd246ff`, the metrics commit — the one piece of phase 6
that has landed — and the suite was red for a day before anyone looked,
because nothing runs it. That is §3.5's argument in one sentence, and it
is the reason this entry stays in the document after the fix.

There were two ways to close it, and they were not the same decision.
The test now asserts that the observation *happened* and that its
duration is not negative, because a zero-duration reading is a correct
reading of a sub-tick call and a histogram bucketing it at zero is
right. The alternative — flooring the duration where it is measured —
would have made the counter lie slightly in exchange for an assertion
that holds on every clock, and a metric that lies to satisfy a test is
the wrong trade for an observability feature.

Note what this does *not* fix: `halite_ext_duration_seconds` on Windows
cannot distinguish a 10 µs call from a 400 µs one. That is the
platform's clock rather than this build's, and it is worth knowing
before somebody writes an alert on the low buckets.

### 1.2 And three more, the first time the race detector ran

The detector had never run against this tree on any platform: Windows
has no compiler for it (§0.2) and 4.1's Linux runs are unit tests under
emulation. `make racecheck` runs it in a container. DIVERGENCE 4.8 is
the account; what belongs here is that it found three things and only
one of them is a race.

1. **A data race on the hub's clock.** `Server.Now` is a func field, and
   `now()` reads it from background goroutines while two tests assigned
   it on a hub that was already serving. Test-origin — `Now` is nil in
   production — but a real unsynchronised access, now moved through an
   atomic installed before `Serve` starts.
2. **A flake that was green where it was written.** A test waited for a
   job's returns and then read the event bus, which the hub writes
   second; it lost about one run in eight on Linux and had never lost on
   Windows. That is the worst shape a test can have, because CI runs on
   Linux and the author does not.
3. **Two permission helpers that cannot work as root**, asserting
   refusals that CAP_DAC_OVERRIDE never delivers.

Each appeared only after the one before it was fixed, which is the
argument for running a new environment more than once before believing
it. **The cheap platforms in §7 item 9 should be read the same way**:
macOS and FreeBSD are not one afternoon each, they are one afternoon
each *per layer*, and the race detector is a layer nothing had run
anywhere.

---

## 2. Phase 5's real remainder: the module inventory

This is the largest block of work left, and it is the one that decides
whether the estate can migrate. The registries answer:

```
halite-node call sys.list_modules        # 50
halite-node call sys.list_state_modules  # 32
```

against SPEC 15.2's 56 core execution modules, 15.5's 47 core state
modules, and 15.3's 65 platform modules. Both counts are what a
*Windows* build registers; the platform rows differ per target and the
core rows do not.

### 2.1 Closed: the five states whose execution side was half there

The previous two revisions of this section were each rewritten, so what
it concluded is worth keeping in one paragraph even though the work is
done.

Each of `timezone`, `environ`, `mount` and `zpool` registered only the
*reading* half of its execution module — `mount.active`, `environ.get`,
`timezone.get_zone`, `zpool.list` — so each was a state *and* the
mutating execution functions under it: two pieces rather than one. All
four are built, along with the `beacon` state, which really was just the
wrapper, and the `schedule` states, whose existing `absent` did not write
the running set back, so a job a state removed came back on the next
restart.

`zpool` is the one that cost something other than code, and §2.1a is why.

### 2.1a What `zpool` cost, and what it bought

ZFS is a kernel module and there is no userspace stand-in, so none of it
could be verified where the rest of this project is: a container shares
the host's kernel, and neither Docker Desktop's WSL2 kernel nor a macOS
VM kernel has `zfs.ko`. The module was therefore run against a real pool
inside a virtual machine with its own kernel, booted under KVM, and that
is now `make zfscheck` rather than an afternoon somebody has to repeat.

It was worth it on the first run. Two defects, both in reading `zpool
list`, and neither reachable from a fixture written from memory:

- Scripted mode indents **every** row under the pool by exactly one tab,
  whatever its depth. A reader that took the indentation for depth found
  every pool empty, and the state then warned that a pool it had just
  created was "nothing".
- The `logs`, `cache` and `spare` section headers are printed
  **unindented and space-padded**, ignoring `-H` in the middle of an
  otherwise tab-separated listing. A reader that took every unindented
  row for the pool's own row filed a pool's log device as an extra leg
  of the mirror above it.

The general lesson is the one §1 draws from Windows and §3.5 draws about
CI: a fixture written from memory tests the memory. The specific lesson
is that a kernel-backed subsystem needs a kernel, and that renting one
for ninety seconds is cheap.

### 2.2 Core modules missing entirely

Counted out of the ledger's own tables, which a test holds to the
registries in both directions.

**Execution, 21 of SPEC 15.2**: `acl`, `apparmor`, `at`, `blockdev`,
`data`, `firewall`, `hostname`, `kernelpkg`, `locale`, `logrotate`,
`nfs`, `ps`, `reboot`, `selinux`, `shadow`, `state`, `sudo`, `swap`,
`system`, `tls`, `tmpfs`.

**State, 18 of SPEC 15.5**: `acl`, `apparmor`, `at`, `firewall`,
`hostname`, `iptables`, `kernelpkg`, `locale`, `logrotate`, `lvm`,
`mac_defaults`, `nftables`, `pro`, `reboot`, `selinux`,
`ssh_known_hosts`, `sudo`, `win_wua`.

The previous revision said sixteen. It named the right set and
subtracted wrong; eighteen is what the table holds.

Ranked by what the estate's own tree reaches for, and by what a migration
is blocked on:

1. **`hostname`**, exec and state. Universal, small, and the last of the
   ones every estate touches.
2. **`ssh_known_hosts`** state. `ssh_auth` ships; this is its pair.
3. **`system`**, `reboot`, `ps`, `status` depth. What an operator reaches
   for during an incident.
4. **`selinux`**, `apparmor`, `firewall`, `iptables`, `nftables`,
   `sudo`, `acl`. Platform-shaped and mostly Linux; see 2.3.
5. The rest — `at`, `blockdev`, `data`, `kernelpkg`, `locale`,
   `logrotate`, `nfs`, `swap`, `tls`, `tmpfs`, `lvm` — each small, none
   blocking.

`shadow` and `state` are deliberately last. `shadow` overlaps
`user.present`'s ageing arguments, which section 6 lists as an open
question, and building it before that is answered would mean building it
twice. `state` as an execution module is `state.apply` callable from a
reaction, which the reactor already reaches another way.

### 2.3 Platform modules: 59 of 65

Every one is registered as refused-with-a-reason, so a tree naming one
gets "this build does not ship it yet" rather than "unknown module". That
is the difference between a gap and a typo, and it is already done. The
six that ship are `zfs`, `zpool`, and the four Windows ones.

| Family | Missing | Why it ranks where it does |
|---|---|---|
| Debian and Ubuntu | 10 | **The estate is Ubuntu.** `aptpkg`, `dpkg`, `apt_key`, `ufw`, `netplan`, `snap`, `pro`, `debconf`, `debbuild`, `apparmor`. |
| Common Linux | 12 | `systemd_service`, `journald`, `iptables`, `nftables`, `lvm`, `mdadm`, `pam`, `modprobe`, `udev`, `quota`, `openssl_cert`, `authselect`. |
| Windows | 14 | Four ship. No user or group provider. |
| macOS | 10 | The providers ship; the `mac_*` modules do not. |
| RHEL | 7 | `yumpkg`, `dnfpkg`, `rpm`, `firewalld`, `subscription_manager`, `dnf_module`, `chattr`. |
| FreeBSD | 5 | The development platform, still unbuilt. |
| SUSE | 1 | `zypperpkg`. |

Note the overlap with 2.2: `iptables`, `nftables` and `lvm` are named in
both 15.3 and 15.5, so building the module and building its state are one
piece of work.

### 2.4 Function-level shortfalls inside modules that ship

`file` has **40** of the ~50 SPEC 15.2 enumerates. The previous revision
said 32 and named `hardlink` among the absences; `file.hardlink` ships.
What is still absent is `patch`, `sed`, `list_backups`, `restore_backup`,
`seek_read`, `seek_write` and the SELinux context pair, along with
`file.accumulated`, which SPEC 15.5 promises by name because trees use it
and which nothing in the tree implements.

`pkg` has 18 of 26, `service` 16 of 18, `cmd` 12 of 13. Those three are
unchanged.

### 2.5 The rest of phase 5

- The agentless **reverse tunnel** (21.1). Trees go inline and anything
  over 4 MiB is refused by name.
- The `scan`, `cloud` and `terraform` **rosters** (21.2), each refused by
  name.
- **`minionfs`/`nodefs`** (13.2), a warning rather than a refusal:
  `cmd/halite-hub/serve.go` logs "this build serves the roots, git, and
  s3 backends" and carries on.
- **No reference bridge ships.** SPEC 20.3 promises in-tree `postgres`
  and `sqs` as worked examples. The protocol, the sandbox, the adapter
  and the name lookup are all built, so all 16 bridged returners are
  reachable and unpopulated.

---

## 3. Phase 6: one item of it exists

Grouped by what each unblocks.

### 3.1 Nothing is measured (SPEC 30)

`grep "func Benchmark"` over the tree returns **zero**. Two of SPEC 30's
thirteen rows name a benchmark as their own measurement method —
highstate compile under 2 s, pillar compile under 500 ms cold and 5 ms
cached — so those are cheap and can land immediately. The rest need the
simulated node harness: 20,000 nodes per hub, 10,000-node dispatch
windows, 5,000 events/second, hub memory under 4 GiB, node memory under
40 MiB idle. None of the thirteen is known to be met or missed.

### 3.2 The observability trio (SPEC 26): metrics are nearly done

This section has moved further than any other since the last revision.

- **30 of SPEC 26.2's 32 metric families are registered**, held in both
  directions by `TestLedgerMetricGapMatchesTheBuild`. The two that are
  not are `halite_pillar_cache_hits_total`, which waits on a pillar cache
  that does not exist, and `halite_pillar_ext_failures_total`, which
  waits on external pillar. **Both wait on a feature, not on a
  counter**, so no metrics work remains that is only metrics work.
- **The node serves its own metrics.** `metrics_listen` opens
  `/v1/metrics` and nothing else, off unless the address is set, TLS
  only. Eighteen families come from the node, including the three
  extension counters and the beacon queue's drop paths. It is DIVERGENCE
  1.11 — a listener on a machine SPEC 6.1 says has none — and it is the
  right trade, but it is a divergence and should stay named as one.
- **One trap survives and is worth repeating.**
  `halite_pillar_failures_total` **is** registered and is a *different*
  metric from the spec's `halite_pillar_ext_failures_total{source}`. An
  alert written from SPEC 26.2's table against the latter matches
  nothing, silently, and silence is what it would do if the estate were
  healthy.
- **Tracing (26.3) and `doctor` (26.4) still do not exist.** `tracing` is
  an inert key (§4). `doctor` has one passing mention in a comment. It is
  also where SPEC 27.4 puts the FIPS grain-mismatch warning, so that
  warning has nowhere to live.

### 3.3 The security model's unbuilt half (SPEC 25)

Unchanged since the last revision, and verified again here.

- **The render sandbox (25.4) does not exist.** SPEC puts all YAML
  parsing and template rendering in an unprivileged child with no
  network, because "the parser and the template engine are the largest
  and most attacker-adjacent code in the system, and they need no
  privilege at all". Both run in-process in the privileged parent. The
  Linux seccomp allowlist and capability drop are likewise absent. Do not
  mistake the bridge sandbox for this one: `internal/bridge` confines
  *extensions*, and it is built.
- **Node-side evidence (25.7) does not exist.** No hash-chained
  append-only record of accepted jobs, no `halite-node verify-evidence`.
  SPEC 27.3 allocates it a directory. It is the control that gives an
  investigator a record a compromised hub cannot rewrite.
- **Detached job signing (25.6) does not exist.** `require_job_signature`
  and `job_signer_keys` are declared and unread; the job wire type has no
  signature field.
- **Signed state trees** are named in the 25.1 threat model and in phase
  6's contents. Do not mistake gitfs ref verification for it: that
  verifies a ref tip, not a tree manifest.
- **No encryption primitives exist in the tree at all** — no AES-GCM, no
  ECDH, no RSA-OAEP, confirmed by search. That is what §4's
  `pillar_cache_disk` actually waits on.

### 3.4 Testing layers that do not exist (SPEC 31)

- **The chaos suite is entirely absent** — `grep -i chaos` over all Go
  source returns zero. SPEC names eight scenarios, each of which must
  have "a defined, tested, documented behaviour": hub restart mid-job,
  network partition, disk full, clock skew, certificate expiry mid-run,
  extension hang, event bus at retention limit, reactor queue overflow.
  Several are the code paths most likely to be wrong, because they are
  the ones no test reaches.
- **The Salt differential runs** — `make saltdiff` builds a container
  carrying Salt's onedir bundle, and all three comparisons pass over ten
  trees; the container defaults to 3007.1 and the ledger records runs
  against 3006.25 and 3008.2 as well. What it compares is still narrower
  than SPEC 31 asks: the low state, the pillar, and test-mode
  predictions, not applied results. Applying a tree twice under both
  implementations and comparing `changes` is the next step, and it needs
  the same container.
- **Coverage.** Two of SPEC 31's four correctness-core packages are below
  the 90% bar on the more forgiving statement metric: `internal/template`
  82.0% and `internal/target` 89.2% (`internal/yaml` 96.3%,
  `internal/state` 90.1%). Unchanged to the decimal since the last
  revision. Branch coverage, which SPEC actually requires, is unmeasured
  and will be lower.
- **Upgrade testing** (hub at N with nodes at N−1 and N+1, cache format
  migration, certificate rotation across an upgrade) does not exist.
- **Integration testing** across the tier 1 matrix does not exist. The
  repository has two containers — the saltdiff image and the ZFS virtual
  machine of §2.1a — and each is a correctness harness for one subsystem
  rather than an integration matrix.

### 3.5 Packaging, release and CI (SPEC 4.3, 27.2)

- **No artifact in SPEC 27.2 is built.** No nfpm config, no `.msi`, no
  `.pkg`, no container image for the product itself, no SBOM, no
  provenance attestation. `make release` builds bare binaries into `bin/`
  and has never been run. `contrib/` has systemd units, FreeBSD rc.d
  scripts and example configuration, and that is the whole packaging
  story.
- **CI exists**, in `.github/workflows/`. SPEC 4.2 opens by saying the
  dependency policy "has teeth: CI enforces it", and until now nothing
  did. `ci.yml` runs every leg of `make check` on push and on every pull
  request — `fmt-check`, `vet` and `policy` as one fast gate, then
  `build-all` across all eight targets, the suite and the race detector
  on Linux *and* Windows, `fips-test`, and the Salt differential that
  SPEC 31 calls the primary correctness gate and that had been green by
  not running. The jobs are split by make target so a failure names the
  leg rather than the word "check".
- **Reproducibility is two builders on a tag**, in `release.yml`: the
  same tag built on two runner images, compared by digest. `make repro`
  remains the cheap half and still runs on every change, because a build
  that is not reproducible from two paths on one machine will not be
  reproducible across two.
- **What CI does not yet do.** It does not publish anything: SPEC 27.2's
  artifacts do not exist to publish (§3.5's first bullet is unchanged),
  so `release.yml` proves the build is reproducible and stops there. It
  runs on GitHub-hosted runners, which is a dependency SPEC does not
  discuss.
- Toolchain provenance — fetch by digest from an internal mirror — is not
  implemented.

**CI was the highest-leverage item in this document, and it is done.**
The argument for it was never abstract. `internal/builtin` did not
compile on Linux for two weeks. The suite went red on 2026-09-04 over a
one-line assertion that cannot hold on Windows and stayed red until
somebody looked. The race detector had never run on any platform and
found three defects the first time it did. Every correction in §0.1
drifted for the same reason: the only thing that ran any of these was a
person deciding to.

**It earned its place on the first run**, and on the legs this document
predicted: six jobs green, both Windows jobs red, five failing tests
across four packages, two causes and neither of them a Windows defect.

**Line endings, four of the five.** There was no `.gitattributes`, so
line endings were whatever each checkout's `core.autocrlf` said. Every
machine this project is developed on uses LF; GitHub's Windows runners
default to CRLF. That breaks every test that reads the project's own
files — `buildpolicy` reported that `go.mod` has no `toolchain`
directive, `docsaudit` reported the generated pages as out of date with
code that had not changed, and `specaudit` made two accusations against
DIVERGENCE.md that were not true. A checkout setting nothing pinned, and
sharper than the tests: `contrib/docker/race/run.sh` under CRLF is
`#!/bin/sh\r`, which no kernel will exec, so a Windows clone with stock
settings produced a `make racecheck` that could not start.

**A privileged account, the fifth.** The runner is a local
administrator, and `permtest.DenyRead`'s DENY entry did not deny — so
`TestFileManagedRefusesAnUnreadableFile` failed reporting the code under
test for a condition the environment never created. It is §1.2's root
problem on the other platform, and the fix is the container's rather
than the unix one's: CI runs the Windows suite as a standard account,
which keeps the coverage instead of skipping it, and is the account a
hub runs as anyway.

**And then, once the Windows suite could run at all, a defect in a
durability guarantee.** The webhook returner's spool names each file by
its nanosecond timestamp, on the reasoning that two returns cannot be
spooled in the same nanosecond. They can: `time.Now` is only as fine as
the platform's clock, and on Windows that is about half a millisecond —
the same granularity as §1.1. Three returns shared a timestamp, the sort
fell through to the content digest, and the backlog went upstream as 3,
2, 1, against the oldest-first guarantee the spool exists to provide.

Fixing it turned up a worse one by inspection: the **relay** spool names
files by timestamp and drop count, so two returns inside one tick are
the *same file* and the second silently overwrites the first — in the
one mechanism whose stated purpose is that an outage delays returns
rather than losing them. No test reached it, and none would have on a
machine with a fine clock.

None of the four was reachable from any machine this project is
developed on. That is the argument for CI restated as a measurement,
four hours after the argument stopped being necessary — and the last of
them is silent data loss in a property `docs/DIVERGENCE.md` advertises
as something Salt's syndic does not do.

---

## 4. Settings that are accepted and do nothing

Twelve keys are **inert**: they warn at startup naming what the operator
gets instead, which is the honest half, and they still do nothing.
`job_cache`, `quiesce`, `quiesce_allowlist`, `startup_states`,
`parallel_jobs`, `socket_dir`, `node_data_cache`, `hub_type`,
`legacy_acl`, `pillar_cache_disk`, `ext_pillar_fail`, `tracing`.

`pillar_cache_disk` deserves separate mention: it is documented as
caching pillar "encrypted at rest" (SPEC 12.8), and no encryption
primitives exist in the tree at all (§3.3). Implementing it means writing
the SPEC 25.3 encrypted-pillar stack, not wiring a flag.

Five more are unread with a reason: `job_signer_keys` and
`require_job_signature` wait on phase 6; `log_level_file`, `regex_engine`
and `node_id_source` are settings with one value.

---

## 5. The conformance tail

Lowest priority, and all three suites pass with tables enforced in both
directions, so nothing here is silently rotting. Re-measured, and
unchanged.

- **YAML (SPEC 10.1):** 402 cases, 331 agree, 34 deliberate, **37 gaps**.
  The direction that matters: **20 of those gaps are documents halite
  accepts that the reference implementation refuses**, all admitted
  defects rather than design choices — a tree Salt would not load, loads
  here. One `gapChomping` case the suite itself calls "the most damaging
  gap in this table", because block-scalar chomping feeds `file.managed`
  contents; that one should be fixed regardless of its position here.
- **Templates (SPEC 10.2):** 198 cases, 157 agree, 26 outside the subset,
  **15 gaps — but 9 are corpus-extractor artifacts**. Six are real:
  calling a filter result, string `indent(width=…)`, `groupby` with a
  numeric attribute, `{{ self.foo() }}`, a `caller=none` macro default,
  and the `is in` test.
- **PyYAML differential:** 114 documents, 104 agree, 10 deviations, zero
  unexplained. Done.
- **The regex engine (SPEC 10.4):** `internal/regexcompat` refuses 11
  PCRE constructs by name with a workaround apiece and hands the rest to
  RE2. The estate's real tree produced **zero regex findings across 193
  files**, which answers SPEC 33 question 8: the backtracking engine
  stays in phase 6 and on this evidence could be dropped. One cheap
  defect, still open: detection is a raw substring scan with only an
  escape check, so a construct spelling inside a character class —
  `[(?=]` — is a false positive, and no test covers it.

---

## 6. Questions that need a person, not a commit

1. **The `cmd.run` shell default (SPEC 33.3).** **54 of the estate
   report's 93 review findings** are one category: a `cmd.run` naming a
   program with arguments, pipes or `||`, which this build treats as a
   single program name because it runs without a shell. None blocks, so
   they are all latent breakage at migration. Decide whether
   `cmd_default_shell: true` is the estate-wide setting for a period, or
   whether 54 call sites get rewritten. **This is a scheduling decision
   with numbers attached, and it should be taken before the estate starts
   rewriting states.**
2. **Modules SPEC never planned for** but the estate uses:
   `alternatives` (3 references), `docker_container`/`docker_image` (2),
   `rabbitmq_policy`/`user`/`vhost` (3), `kmod` (1), `macpackage` (1).
   Amend SPEC, bridge them, or rewrite the tree.
3. **A `win_registry` state.** SPEC 15.5 does not name one, so none
   ships — the `win_registry` *execution* module does. Salt has
   `reg.present` and an estate migrating from it will want the same; a
   registry value that can only be set through `module.run` reports a
   change on every run.
4. **Detached job signing (33.6) and node-side evidence.** Both answer
   the compromised-hub threat. Decide together, and before the API
   surface sets any harder.
5. **Seven state functions reject arguments Salt accepts**: `user.present`
   (`mindays`, `maxdays`, `inactdays`, `unique`, `optional_groups`,
   `enforce_password`), `archive.extracted` (5), `group.present`
   (`system`, `members`), `file.managed` (`skip_verify`, `keep_source`),
   `file.replace`, `pkg.installed`, `git.latest`. The `user.present` row
   is one coherent feature — shadow ageing policy — not six oversights.
6. **`module.run` argument pass-through.** Salt passes unknown kwargs
   through to the function being run; this build validates against a
   fixed parameter list. Strict validation is right for every other state
   and wrong for this one.
7. **systemd over D-Bus, or `systemctl` shell-out?** SPEC 15.2 says the
   former; the build does the latter. Note that Windows settled the
   general form of this question: `win_service` speaks the service
   control manager's API because `sc.exe` has no machine-readable output
   mode, and `win_task` runs `schtasks` because it does. systemd has
   both, so this is a cost question rather than a correctness one.
8. **Do reference bridges ship?** SPEC 20.3 promises in-tree `postgres`
   and `sqs` as worked examples, and no destination extension exists.

Question 9 of the previous revision — strict undefined (33.4) — is
answered and struck: `CatUndefined` is implemented and the migration
report emits the undefined-reference row SPEC 28.5 requires.

---

## 7. Suggested order

Sequenced by value per unit of work, and by what unblocks what.

~~**Now — stop the drift.** Stand up CI.~~ **Done**, and §3.5 says what
it runs. It held the top of this list through three revisions, and the
cost of it ranking second was paid three times: two weeks of a package
that did not compile on Linux, a day of a red suite on Windows, and a
race detector that had never run anywhere. The one thing left is to
distrust it until it has caught something — see §3.5's closing
paragraph.

**Now — the estate's own blockers**

1. **`hostname`** and **`ssh_known_hosts`**. Small, universal, and the
   last of the core modules every estate touches.
2. **The Debian and Ubuntu platform row** (§2.3). Ten modules, and the
   estate is Ubuntu. This is the largest single block of work in the
   document and the one the migration is actually blocked on.

**Then — phase 6 foundations**

3. The two SPEC 30 benchmarks that need no harness (§3.1). Metrics are no
   longer on this list; §3.2 closed.
4. Get the differential to compare *applied* results, in the container it
   already has, and in the job CI now runs it from (§3.4).
5. The chaos suite, starting with the paths no test reaches at all.
6. **Packaging** (§3.5). The reproducibility half of this item is done —
   `release.yml` compares two builders on every tag — and what is left
   is that there are no artifacts to publish: no nfpm config, no `.msi`,
   no `.pkg`, no SBOM, no attestation. CI is the thing that would sign
   and publish them, so this is now the next release-shaped work rather
   than a prerequisite for it.
7. The render sandbox (§3.3), which is the largest unbuilt security
   control and the one SPEC argues for most directly.

**In parallel, cheap and independent**

8. **Run the suite on macOS and FreeBSD.** Windows found three
   cross-platform defects in one afternoon and a fourth the next day,
   and the race detector found three more the first time it ran; there
   is no reason to think those two hold none (§1, §1.2). Neither is a
   GitHub-hosted runner, so neither is covered by what was just built —
   which is the honest limit of item 1.

**Blocked on a decision**

9. The `cmd.run` default, the unplanned modules, a `win_registry` state,
   job signing and node evidence (§6).

**Last**

10. The YAML over-acceptance set, prioritising the chomping case; the six
    real template gaps; the regexcompat character-class false positive
    (§5).
