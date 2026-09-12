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

**Amended 2026-09-10** on freebsd/amd64 with Go 1.26.8, without a full
re-measurement. Several blocks landed since the date above and the counts
in §0, §2.3 and §7 were corrected for them rather than re-derived: SPEC
15.3's **macOS row**, which now ships entirely, and eleven of the twelve
in its **Common Linux row** — every one but `authselect`. `pam`/`quota`/
`openssl_cert` are the three §7.12 ranked first; `lvm`, `iptables` and
`nftables` are the next three, each closing a 15.3 module *and* a 15.5
state; `journald` is the one §7.12 flagged for a design decision;
`mdadm`, `modprobe` and `udev` are exec-only (SPEC 15.5 names no state
for any of them). `authselect` is left **pending on purpose** — see
§2.3's row for why. 40 of 65 platform modules now ship. One consequence a reader should not have to hunt for:
the release gate is red on **ten** modules rather than two, all of them
`apparmor`, `snap` or the eight-strong macOS row — every other new
module has been driven against its real tool. `iptables`/`nftables`
run their live tests inside an unprivileged network namespace and
`journald`'s reads run against the host journal, so all three reached
`hardware` in the ordinary suite; `journald`'s varlink control verbs
were driven as root against real systemd 255. Everything else here is still
measured against 2026-09-05.

**Amended again 2026-09-11**: `apparmor` closed too (DIVERGENCE 5.55),
on §6 item 11's first route rather than its third — this fleet's own
Ubuntu host has no `passt` package installed, so it has none of the
unparseable `abstractions/passt` syntax that broke `apparmor-utils` on
the runner 5.37 used, and the same `apparmor-utils` 4.0.1 that failed
there works here. `live_apparmor_test.go` needed no change: 148 real
profiles read in all four modes, and a throwaway profile loaded, moved
complain → enforce by name, disabled with its reboot symlink confirmed,
and unloaded. §6 item 11's actual question — whether to stop depending
on the `aa-*` tools so the module also works on a node that *does* have
an unparseable profile somewhere — is still open and still a decision
rather than an afternoon; it is just no longer the blocking one. The
gate drops from ten to **nine**: `snap` and the eight-strong macOS row.

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
| 5. Breadth | gitfs, s3fs, agentless mode, relays and the FIPS artifact set are built. Windows parity is largely done and verified on a real host; the FreeBSD and macOS rows of SPEC 15.3 now ship entirely. **32 of SPEC 15.3's 65 platform modules, 18 of SPEC 15.2's core execution modules and 14 of SPEC 15.5's core state modules remain.** |
| 6. Hardening to 1.0 | Started. Metrics are nearly complete, tracing and `doctor` ship (§3.2); CI runs every leg of `make check` on four platforms; the chaos suite and upgrade testing are built (§3.4); the two SPEC 30 rows that a benchmark can measure are measured and met (§3.1). Outstanding: the scale harness the other eleven performance rows need, no packaging, no node evidence, no detached signing; the render sandbox ships (§3.3) and the seccomp allowlist on the parent does not. |

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

**The lesson generalises, and did.** macOS built and had providers and
had never run the suite; it runs on every change now, and found two
defects within five minutes of a runner existing — see §1.3. FreeBSD is
the development platform, has run the suite for a long time, and has no
CI because GitHub hosts no runner for it. Linux arm64 has still never
run it, though macOS in CI is arm64, so the platform-neutral code now
runs on that architecture somewhere.

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

### 1.2 And three more, from running the race detector *more often*

An earlier revision of this section said the detector had never run
against this tree on any platform. **That was wrong.** `make check` and
`make race` have been run on FreeBSD, the development platform, and on
Ubuntu Linux, and the toolchain supports the detector on both. What had
never happened is `make check` completing on Windows, where
`CGO_ENABLED=1` has no compiler to use — which is what `make racecheck`
and the container are for, and nothing else.

The correction matters because it changes the lesson. These three were
**reachable on platforms that already ran the detector**, and had simply
never been hit; DIVERGENCE 4.8 is the account. What found them was
repetition and a different account, not a new system.

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
it.

The first two say something sharper than "run it somewhere new": **a
gate that runs sometimes is not the same as a gate that runs.** The data
race showed up in one full sweep and not in the two after it, and the
flake loses about one run in eight. Neither is found by running the
detector once, on any platform. Both are found by running it on every
change, which is what CI now does — and which is why §3.5 moved from an
argument to a measurement.

**§7's remaining platforms should be read that way too.** macOS has
never run the suite and FreeBSD has, so they are not the same item: the
first is 4.6's kind of risk, and the second is this section's.

### 1.3 And two more, five minutes after macOS had a runner

macOS is a GitHub-hosted runner. An earlier revision of §7 said it was
not — the sentence was written about FreeBSD and macOS was allowed to
ride along in it unchecked, which is §1.2's mistake made a second time
about a second thing. It costs two lines in the matrix, and it is arm64,
so it is also the first time these tests have run on that architecture.

Both findings are in tests rather than modules, and both are the same
shape as the sentence that delayed them: **a guard written for the one
platform anybody had run.**

1. **Seven zpool tests fell through a Windows-shaped skip.** It read `if
   runtime.GOOS == "windows"`, which is true of the platform it was
   written on and says nothing about macOS — neither Windows nor a ZFS
   platform, so the registry refused the states correctly and the tests
   were not expecting to be refused. It asks whether this node is one of
   the platforms `zfsPlatforms` declares now, rather than naming one it
   is not.
2. **The timezone fixture could not converge on darwin.** It redirects
   the zone files and blanks `Lookup` so `setZone` writes them rather
   than reaching for systemd; darwin takes neither path, driving
   `systemsetup`, which the test mocks. Nothing moved the link the
   reader reads, so the state could not have converged however correct
   it was — and it *is* correct: `zoneFromPath` takes the last
   `/zoneinfo/` in a target, which is what makes macOS's
   /var/db/timezone path parse. The fixture was what stood between the
   module and any evidence of that, which is the more insidious failure
   because it looks like coverage.

FreeBSD is the platform this now leaves uncovered, and it is a harder
problem than macOS was: GitHub hosts no FreeBSD runner and the agent is
.NET, so self-hosting is not the straightforward answer either. It needs
a VM inside a Linux runner or a second CI system. §7 carries it.

### 1.4 What FreeBSD found, and the audit that came out of it

FreeBSD went into CI in a QEMU virtual machine on a Linux runner,
because GitHub hosts no FreeBSD runner and Cirrus CI — which ran native
FreeBSD and was what projects in this position used — stopped running
jobs in June 2026. The prediction was that emulation would make it too
slow for a pull request. It runs the suite in **1m44s**, faster than the
Windows leg, so that was wrong; only the race detector is left off it,
because the detector's own slowdown multiplied by emulation's is a
different proposition.

It found two things in its first green run, and the second is the one
that matters:

1. The virtual machine's account is not root, so a toolchain unpacked
   into `/usr/local` failed. Environmental.
2. **`hostname`'s tests had never run FreeBSD's branch.**
   `persistentHostname` reads rc.conf through `sysrc` there and
   /etc/hostname everywhere else; the tests redirected the file and
   nothing else. Linux took the file branch and passed, Windows skipped
   the module, and FreeBSD — the only platform with a `sysrc`, and the
   one this project is developed on — had no automation. The module's
   FreeBSD path had no test at all, behind a test that looked like one.

**That is the same defect as §1.3's macOS timezone fixture**, one week
apart: a fixture that forces one code path on a platform that takes
another, passing while asserting nothing. Two platforms, two fixtures,
one mistake — which is a pattern rather than a coincidence, so the rest
of the platform-branching code was audited rather than left for the next
runner to find.

**The audit's finding is about shape, not about any one module.** A
decision made from `runtime.GOOS` can only be checked on the platform it
decides for, so with four platforms every branch but one is unreachable
from any given host. `internal/config` solved this before —
`RootFor(goos)`, `VarPathFor(goos, kind)` take the platform as an
argument, after a layout that was never checked from another host put a
node's configuration and enrollment key in `\etc\halite` off whichever
drive it started on. The modules had not followed it.

What changed:

- `sysctlConfFor(goos)` replaces a bare `if runtime.GOOS == "linux"`,
  and six platforms are asserted from wherever the suite runs.
- `hashLocations` was already a table keyed by platform and **nothing
  asserted it**. Its field index is now pinned per platform: a wrong one
  reads a UID or a date, compares it against a password hash, never
  matches, and so resets the password on every run — on a platform
  nobody develops on, reporting a change rather than failing.
- `pickAccountTool` had no test at all. Its fallback — the branch a
  minimal container takes — is now checked through an injected
  `Lookup`, from any host.
- Every platform *name* in every signature is checked against the set Go
  actually has: 194 declarations. A typo there produces a module that
  refuses everywhere, and on all but one platform the refusal is what a
  reader would expect to see anyway.
- A refusal has to name both the platform the node is on and the ones
  the module runs on.

The limit is worth stating: this checks data, not behaviour. Whether
`sysrc` is the right way to read a FreeBSD hostname is a question for a
FreeBSD runner, which is what found it.

### 1.5 And four more, the first time anything compiled for tier 3

SPEC 27.1's tier 3 — OpenBSD, NetBSD, Solaris and illumos, Linux on
riscv64, ppc64le and s390x — promises "compiles and is published".
Nothing had ever compiled for any of them: `TARGETS` in the Makefile
listed the eight tier 1 and tier 2 platforms, so `build-all` enforced
the tiers that were also the tiers somebody ran on, and the one tier
whose entire content is a compilation promise was the one nothing
compiled.

Compiled for the first time on 2026-09-06: **four of the nine targets
failed**, all four in `internal/bridge`'s resource limits, and both
failures were real platform differences rather than typos — OpenBSD has
no `RLIMIT_AS`, and Solaris and illumos have no `RLIMIT_NPROC` at all.
DIVERGENCE 4.10 has the detail.

**This is §1.3 and §1.4's defect a third time, in a third place.** Those
two were fixtures forcing a branch the platform does not take; this is a
build tag claiming platforms are alike when they are not. Same root:
code that decides by platform can only be checked on the platform it
decides for, and nobody was on these. It is also 4.4a exactly — macOS
grouped with the BSDs for the width of `syscall.Rlimit`, tree did not
compile there at all, found when somebody finally built for it.

What changed:

- The four targets build, and so does AIX, which is in no tier and was
  fixed because leaving one platform uncompilable is how this arose.
- `limitsAvailable` now reads the same declarations `Confine` applies,
  so `sys.list_extensions` cannot report a limit as enforced that is
  skipped, or the reverse. Before, it said all four limits were enforced
  on every unix, which was a sentence rather than a consequence.
- `TARGETS` carries all seventeen platforms, so `build-all` compiles and
  vets each and `cross` publishes it — which is what tier 3 says. 98s
  for the seventeen.
- `internal/buildpolicy` reads SPEC 27.1's tier table and the Makefile's
  target list and fails if they disagree in **either** direction. Each
  tier's platform cell is matched in full, so any edit to that table
  fails and lands in front of somebody who has to decide what it means
  for the build. That is the part that lasts: a row quietly growing a
  platform nothing compiles for is how this happened.

The limit, again: compiling is what tier 3 promises and compiling is
what is checked. Nothing has *run* on any of these seven platforms, and
the `RLIMIT_DATA` mapping on OpenBSD is read from that platform's
documented behaviour rather than watched taking effect.

### 1.6 And the one CI had been reporting for a week

`TestAQueuedJobWaitsForTheNodeToReturn` had been failing about one run
in twenty and was written off as a flake worth watching. It was not a
flake. It is a **lost update on the job record**, and the failure it
reports is real: a queued job could be delivered to the same node
twice.

The hub's job record is read, changed and written back by several
goroutines and the write replaces the whole file. The goroutine that
gives a reconnecting node its missed jobs clears that node from the
spool; the goroutine that records the node's return marks the job
complete. Both read, both write, and the later write puts back
everything the earlier one changed — including the spool entry. A spool
entry that comes back means the node is sent the job again on its next
connection, which for the `cmd.run` a queued job usually is, is a second
run of an instruction issued once.

`job.Cache.Update` now does read-change-write under a per-job lock and
hands the mutator what is on disk. DIVERGENCE 4.11 has the account and
the boundary — which sites were converted and which four `Put` calls are
creates and did not need to be.

**Three things this says about the rest of the document.**

1. It is the **third defect of this shape in a fortnight**. The other
   two are in §1.2's list: the webhook spool naming files by a timestamp
   that is not unique, and the relay spool doing the same and silently
   overwriting. All three are durability mechanisms that are correct
   read one operation at a time and wrong when two arrive together, and
   none of them had a test with two writers in it. That is a gap in how
   this build is tested rather than three unrelated bugs, and §3.4's
   chaos suite is where it belongs.
2. **An intermittent test failure is a finding until it is diagnosed.**
   This one was reported here as "noted but unaddressed" for a week
   while modules were built on top of it. The cost of that reading was
   nothing this time and could have been a duplicated `pkg.installed`
   on the estate.
3. It is §1's argument again, in the form the argument was always
   about: **only CI found it.** No local run has ever failed this test,
   before or since — the window needs a slower machine than the ones
   here, and every one of the four runners is slower than these.

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

**Execution, 17 of SPEC 15.2**: `acl`, `at`, `blockdev`, `data`,
`kernelpkg`, `locale`, `logrotate`, `nfs`, `reboot`, `selinux`,
`shadow`, `state`, `sudo`, `swap`, `system`, `tls`, `tmpfs`. `ps` has
shipped (DIVERGENCE 5.61).

**State, 14 of SPEC 15.5**: `acl`, `at`, `iptables`, `kernelpkg`,
`locale`, `logrotate`, `lvm`, `mac_defaults`, `nftables`, `pro`,
`reboot`, `selinux`, `sudo`, `win_wua`.

`hostname`, `ssh_known_hosts` and `apparmor` have since shipped, which
is what moved both counts. `apparmor` moved three at once: SPEC names it
in 15.2, in 15.5 and in 15.3's Debian row, so it is the only remaining
item on this list that closes a platform gap as a side effect of closing
a core one.

Ranked by what the fleet's own tree reaches for. Not by "what a
migration is blocked on", which is how this list was ordered until
2026-09-06: the fleet is entirely on halite and there is no migration
to block. §7 has the consequences.

1. ~~**`hostname`**, exec and state~~ — **done.** Four execution
   functions and the state, managing the running name and the persistent
   one together, because a node where those disagree renames itself at
   the next boot. Unix only and declared so: a Windows rename waits for a
   reboot, so a state that set one would report a change on every run
   until somebody rebooted — the shape §6's `win_registry` question is
   already about, and not worth shipping twice before it is answered.
2. ~~**`ssh_known_hosts`** state~~ — **done**, with `ssh.known_hosts`
   beside it, the way `ssh_auth` pairs with `ssh.auth_keys`. One
   deliberate break from Salt, recorded in the ledger: trust on first use
   is refused rather than performed. A key is declared outright or
   scanned and checked against a declared fingerprint; Salt scans and
   accepts, which pins whatever answered the day the tree first ran.
3. **`system`**, `reboot`, ~~`ps`~~, `status` depth. What an operator
   reaches for during an incident. **`ps` ships**: seven functions over
   the system's own `ps`, FreeBSD's libxo JSON where there is one, and
   both halves demonstrated on a real process table -- the mutating one
   against processes the test starts and marks, so it needs no root and
   touches nothing else on the machine. It also unblocked two of SPEC
   16.2's beacons, `proc` and `ps`, which were pending "a later phase,
   with a portable reader for it" -- **and both now ship**, leaving
   fifteen of that inventory. DIVERGENCE 5.61 and 5.62.
4. **`selinux`**, `iptables`, `nftables`, `sudo`, `acl`. Platform-shaped
   and mostly Linux; see 2.3. `apparmor` is struck: it ships, with seven
   execution functions and the `apparmor.mode` state, and it closed
   15.2, 15.5 and 15.3's Debian row together. `selinux` is the one left
   whose shape it informs — the same question (what is the running
   policy, and what does a state do about it) against a mechanism that
   answers it entirely differently, so nothing here was built to be
   shared between them until there is a second one to share with. `firewall` is struck: it
   ships as a virtual module with a ufw provider, which closed 15.2's
   execution module, 15.5's state and 15.3's `ufw` together. Its
   provider interface is shaped by the one provider it has, and
   `iptables` and `nftables` are the two most likely to reshape it —
   they replace a whole ruleset at once rather than adding rules one at
   a time, which is a different thing from what the interface currently
   asks a provider to do.
7. The rest — `at`, `blockdev`, `data`, `kernelpkg`, `locale`,
   `logrotate`, `nfs`, `swap`, `tls`, `tmpfs`, `lvm` — each small, none
   blocking. `locale` now also carries `internal/migrate`'s gap test,
   which needs a state that does not exist and comes due whenever one is
   built.

`shadow` and `state` are deliberately last. `shadow` overlaps
`user.present`'s ageing arguments, which section 6 lists as an open
question, and building it before that is answered would mean building it
twice. `state` as an execution module is `state.apply` callable from a
reaction, which the reactor already reaches another way.

### 2.3 Platform modules: 40 of 65

Every one is registered as refused-with-a-reason, so a tree naming one
gets "this build does not ship it yet" rather than "unknown module". That
is the difference between a gap and a typo, and it is already done.
Twenty-two ship: `zfs`, `zpool`, the four Windows ones, `dpkg`,
`debconf`, `netplan`, `apparmor`, `snap`, `jail`, and **ten aliases**.
**SPEC 15.3's FreeBSD row ships entirely**, which is the first row to.

**The aliases answered the open question this section used to end with.**
SPEC names both halves and both are true: 15.2's `pkg`, `service` and
`sysctl` pick a provider for the node, and 15.3 names `aptpkg`,
`freebsdpkg`, `systemd_service` and the rest as modules of their own.
The providers here had been carrying 15.3's names all along, so what was
missing was the name being callable. It is a module name rather than a
second set of functions, which is what keeps the counts honest, and it
refuses on a node whose provider does not match — naming the provider
that node *does* have, because `aptpkg.install` on a RHEL node is
neither a typo nor an unbuilt module but the wrong module for the
machine.

Only the names whose provider exists are aliased. `zypperpkg` and
`dnfpkg` stay pending: SUSE has no provider here and the dnf one covers
repositories but not packages, so aliasing either would turn "not built"
into "built, and fails when you call it".

`dpkg` is the other Debian arrival, and the one that pays off soonest,
because it is the half the virtual `pkg` module is deliberately not:
`pkg.list_pkgs` filters to what is installed, so a package left
half-configured by an interrupted upgrade is invisible there — and apt
refuses to do anything else until it is resolved, which makes it exactly
what an operator is looking for.

| Family | Missing | Why it ranks where it does |
|---|---|---|
| Debian and Ubuntu | 3 | **One host of five.** `dpkg`, `debconf`, `netplan`, `apparmor` and `snap` ship; `aptpkg` and `ufw` are aliases. `pro` and `debbuild` remain. `apt_key` is declined rather than pending: apt-key was removed in Debian 12 and Ubuntu 24.04, and `pkgrepo` writes the keyrings that replaced it. |
| Common Linux | 1 | `systemd_service` is an alias. **`pam`, `quota`, `openssl_cert`, `lvm`, `iptables`, `nftables`, `journald`, `mdadm`, `modprobe` and `udev` ship** (DIVERGENCE 5.47-5.54) -- eleven of twelve. `iptables`/`nftables` are deliberately **not** `firewall` providers. `journald` reads through `journalctl -o json` and does rotate/flush/sync over journald's own varlink socket via a new `internal/varlink`. `mdadm`/`modprobe`/`udev` are exec-only (no state -- SPEC 15.5 names none for any of them). **`authselect` is deliberately pending, not built.** SPEC files it under Common Linux, but it is Fedora/RHEL 8+ only in reality -- Debian manages PAM through `pam-auth-update`, which `pam` already reads -- and this project has no RHEL host to verify against. It waits alongside the other RHEL-only SPEC 15.3 modules for the same reason, rather than shipping fixtures for a tool nobody here has run, which is the exact mistake DIVERGENCE 5.31 warns against. |
| Windows | 13 | Four ship, `win_pkg` is an alias. No user or group provider. |
| macOS | 0 | **The row ships entirely**, second after FreeBSD. `mac_brew_pkg` and `mac_service` are aliases; `mac_defaults`, `mac_power`, `mac_user`, `mac_group`, `mac_shadow`, `mac_softwareupdate`, `mac_keychain` and `mac_assistive` are modules (DIVERGENCE 5.41-5.46). Every one of them is `assumed`: no CI leg is a Mac. |
| RHEL | 7 | `yumpkg`, `dnfpkg`, `rpm`, `firewalld`, `subscription_manager`, `dnf_module`, `chattr`. |
| FreeBSD | 0 | **Four hosts of five, and the first row to ship entirely.** `freebsdpkg`, `freebsd_service`, `freebsd_sysctl` and `pf` are aliases; `pf` was the `firewall` module's second provider and the first to reshape that interface, refusing a default policy because pf has none (DIVERGENCE 5.31). `jail` reads `jls --libxo=json` and has its envelope checked against a real `jls` on CI's FreeBSD runner (5.32). |
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

`pkg` has 23 of 26 — `info_installed`, `file_dict`, `download`,
`list_downloaded` and `autoremove` landed in the apt provider (DIVERGENCE
5.40), leaving only `mod_repo`/`del_repo`, which `pkgrepo` already
covers. `service` has 16 of 18, `cmd` 12 of 13.

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

### 3.1 Two of the thirteen are measured (SPEC 30)

~~`grep "func Benchmark"` over the tree returns **zero**.~~ **Done for
the two rows that named a benchmark as their own method**, which is what
this item said was cheap. `internal/perf` carries all thirteen rows with
what measures each, held to SPEC's own table in both directions, and
`make perf` runs the measured two against their targets.

Both are met with room: the highstate compile of 500 states over 50 SLS
files takes **96 ms** against a 2 s target, and the cold pillar compile
of 200 pillar SLS takes **154 ms** against 500 ms. FreeBSD, Xeon E5-2620
v3, Go 1.26.8. DIVERGENCE 5.57.

Two things it does not settle. The pillar row's *cached* number has
nothing to measure, because there is no pillar cache — the same feature
§3.2's two missing metric families wait on. And the other eleven rows
still need the simulated node harness, a soak, or the integration
matrix: 20,000 nodes per hub, 10,000-node dispatch windows, 5,000
events/second, hub memory under 4 GiB, node memory under 40 MiB idle.
Each is tracked with what it waits on, which is a different thing from
being measured.

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
- ~~**Tracing (26.3) and `doctor` (26.4) still do not exist.**~~
  **`doctor` ships**, with all eleven of SPEC 26.4's checks, a remediation
  line on every finding that a guard makes mandatory, and the check set
  held to the specification's own sentence in both directions. It
  carries SPEC 27.4's FIPS mismatch warning, which had nowhere to live
  before. DIVERGENCE 5.30, including why a platform with no kernel FIPS
  mode is a skip rather than a warning — the fleet is four FreeBSD hosts
  to one Linux, and a check that warns on four nodes in five is one
  nobody reads. **Tracing (26.3) ships too**, machinery and wiring: a
  span per job, per state and per file transfer, `tracing` off the inert
  table, and a trace that survives the hop from hub to node and back
  across a file transfer. **SPEC section 26 is complete.** DIVERGENCE
  5.34, including the two defects the wiring found and the one thing it
  does not establish — no collector has read a span this build made.

### 3.3 The security model's unbuilt half (SPEC 25)

Unchanged since the last revision, and verified again here.

- ~~**The render sandbox (25.4) does not exist.**~~ **Built**, in
  `internal/rendersandbox`, behind `render_sandbox: true` and off by
  default while the path is new. YAML parsing and template rendering
  happen in a child; module dispatch, template loading and gpg
  decryption stay in the parent, which is SPEC's own division. The
  pipeline is split at its serializer so that decrypted pillar never
  enters the unprivileged process. It costs 2.3 times the compile — 217
  ms against 96 ms on the SPEC 30 tree — and is still an order of
  magnitude inside that target. DIVERGENCE 5.58.

  Two parts of the same bullet remain. **The Linux seccomp allowlist and
  capability drop on the privileged *parent* are not built**, and they
  are a separate mechanism from the child. And **the unprivileged
  account is written and unexercised**: this project's hosts render as
  the developer's own account, so `Credential` and the network namespace
  both need a node running as root to be demonstrated. Do not mistake
  the bridge sandbox for either: `internal/bridge` confines
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

- ~~**The chaos suite is entirely absent**~~ — **built.** All eight of
  SPEC's scenarios are registered in `internal/chaos` with a defined
  behaviour and a stated limit, and each has a test that names it. A
  ninth is registered that SPEC does not name: the concurrent-writer
  shape all three of the defects in §1.2 and §1.6 had, none of which
  any of SPEC's eight would have caught. Three guards hold the registry
  to SPEC and to the tests, and each was checked by breaking it.
  DIVERGENCE 5.29 has the account; `make chaos` runs the layer with
  `-v`, which is the point of it.

  **It found two things on its first run.** A reader resuming from a
  pruned event-bus offset was silently skipped forward — 380 events,
  measured — which is DIVERGENCE 4.12, **since fixed**. It was never an
  open decision: SPEC 17.2 names the behaviour and names the error
  (`subscriber_lag`), and its whole argument is that Salt loses events
  silently and this must not. Checked against Salt 3007.1 and 3008.2 in
  the differential container rather than recalled:
  `salt/utils/event.py` has no offset, replay or resume at all, and a
  subscriber that cannot keep up is dropped at a ZeroMQ high-water mark
  of 1000 without being told. Silently advancing a stale reader was the
  Salt behaviour by another mechanism. `subscriber_lag` had shipped as a
  *metric* and not as the error, which is how the row read as done.
  And the behaviour first written down for `hub restart mid-job` could
  not be tested as written, because stopping a hub *drains*: `Serve`
  waits for the batch goroutine, so a graceful stop never leaves the
  half-done batch the scenario is about.
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
- ~~**Upgrade testing**~~ — **built.** All three clauses of SPEC 31's
  Upgrade row have tests, and `internal/specaudit` holds the row to them
  in both directions so a fourth clause cannot sit there uncovered. What
  it establishes is the tolerance and the refusal; no two halite
  versions have ever actually run against each other, because there has
  never been a second version. DIVERGENCE 4.13.
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
  `build-all` across all seventeen targets of SPEC 27.1, tier 3
  included, the suite and the race detector on Linux, Windows, macOS and
  FreeBSD, `fips-test`, and the Salt differential that
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
- **`cross` now publishes tier 3 too**, which is the second half of what
  that tier promises. It is nine more binaries per release and nothing
  has run any of them; see §1.5 for what that does and does not mean.

**CI was the highest-leverage item in this document, and it is done.**
The argument for it was never abstract. `internal/builtin` did not
compile on Linux for two weeks. The suite went red on 2026-09-04 over a
one-line assertion that cannot hold on Windows and stayed red until
somebody looked. The race detector, which had been run on FreeBSD and on
Linux, had never been run *often enough* to catch a race that appears in
one sweep out of three (§1.2). Every correction in §0.1 drifted for the
same reason: the only thing that ran any of these was a person deciding
to.

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

- **YAML (SPEC 10.1):** 402 cases, 330 agree, 36 deliberate, **36 gaps**.
  The direction that matters: **20 of those gaps are documents halite
  accepts that the reference implementation refuses**, all admitted
  defects rather than design choices — a tree Salt would not load, loads
  here. ~~One `gapChomping` case the suite itself calls "the most
  damaging gap in this table".~~ **Measured and closed, and it was not
  what the label said.** That case was an `!!binary` value the
  comparison could not represent; chomping itself had never been
  measured, and a 126-document matrix against PyYAML found the real
  defect — a block scalar whose file ends without a final newline came
  back with one, so `contents: |` wrote a file the source does not
  contain. The agreement count fell by one in the process, because two
  suite cases assert a line break that PyYAML and libyaml both decline
  to add and SPEC 10.1 picks the implementations. DIVERGENCE 5.56.
- **Templates (SPEC 10.2):** 198 cases, 157 agree, 26 outside the subset,
  **15 gaps — but 9 are corpus-extractor artifacts**. Six are real:
  calling a filter result, string `indent(width=…)`, `groupby` with a
  numeric attribute, `{{ self.foo() }}`, a `caller=none` macro default,
  and the `is in` test.
- **PyYAML differential:** 240 documents, 230 agree, 10 deviations, zero
  unexplained. Done. The count grew with the chomping matrix of
  DIVERGENCE 5.56.

- **`salt['x.y'] is defined` always answers true**, found while building
  the render sandbox. `template.Dispatcher.HasModule` exists, is
  implemented by every dispatcher in the tree, and is called by nothing:
  a subscript of `salt` returns a dispatch value whatever the name, so
  the guard `{% if salt['foo.bar'] is defined %}` takes the true branch
  on a node that does not have the module and fails at the call instead.
  Salt answers false, because its loader raises and Jinja turns that
  into undefined, so this is a migration defect as well as a wrong
  answer. The fix is confined to the subscript spelling: a name with a
  dot in it can be checked, and a bare `salt['pkg']` used as a prefix
  for `salt.pkg.version` cannot, so only the first consults the
  registry. Not fixed here, because it changes what an existing tree
  means and belongs in its own change.
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
8. **`module.run` argument pass-through.** Salt passes unknown kwargs
   through to the function being run; this build validates against a
   fixed parameter list. Strict validation is right for every other state
   and wrong for this one.
9. ~~**systemd over D-Bus, or `systemctl` shell-out?**~~ **Resolved
   (DIVERGENCE 5.39): D-Bus, as SPEC 15.2 says.** `internal/dbus` is a
   direct ~470-line wire client; the systemd provider issues its whole
   surface over `org.freedesktop.systemd1`, waits on `JobRemoved` for
   job completion, and falls back to `systemctl` only when the bus
   cannot be reached. Driven against real systemd 255.
10. **Do reference bridges ship?** SPEC 20.3 promises in-tree `postgres`
   and `sqs` as worked examples, and no destination extension exists.
11. **Does `apparmor` stop depending on the `aa-*` tools?** No longer the
   blocking question — DIVERGENCE 5.55 closed the release gate on
   `apparmor` by route 1 below (a host whose tools work), so the module
   ships. This is now about the *other* kind of host: one whose
   `apparmor-utils` cannot parse a profile it has, the way Ubuntu
   24.04's could not parse `abstractions/passt` on the runner 5.37
   found it on. On such a host `apparmor.enforce`, `complain` and
   `disable` are still inoperable by any means, and `apparmor.status`
   says so by name (`tools_reason`) rather than silently. DIVERGENCE
   5.37 has the reproduction.

   `apparmor_parser` is C, ships by default, parses everything the
   kernel does, and this module already uses it for `reload`. It can
   load a profile in complain mode directly. **The catch is
   persistence:** `aa-complain` edits the profile file to add
   `flags=(complain)` so the mode survives a reboot, and
   `apparmor_parser --complain` changes no file, so the next boot loads
   the profile as written and the mode is gone.

   So the choice is between a module that works on Ubuntu but forgets
   its mode at the next boot, a module that works on Ubuntu and owns an
   AppArmor profile parser — a larger surface than it looks, and the
   sort §5.31 was about — and leaving it as it is with the fact
   recorded. It is a security control, so the third is not obviously
   wrong; that is why it is here rather than in §7.

Question 9 of the previous revision — strict undefined (33.4) — is
answered and struck: `CatUndefined` is implemented and the migration
report emits the undefined-reference row SPEC 28.5 requires.

---

## 7. Suggested order

**This section was re-ranked on 2026-09-06, and the premise it had used
through every previous revision was wrong.**

It ranked by "what the migration is blocked on", and by "the estate is
Ubuntu". Neither is true. The fleet is **100% on halite** — the
migration is finished, so there is nothing left to be blocked. And it is
**four FreeBSD hosts** (two physical, two virtual) **to one Ubuntu**,
built from source and installed with `make install`. Every item below
moved, and two of them moved a long way.

Three consequences, before the list:

- **FreeBSD carries 80% of production and is SPEC 27.1 tier 2.** Tier 2
  promises "built and unit-tested; functional tests on a subset"; tier 1
  promises full CI, functional tests and packages. CI already runs the
  whole unit suite on FreeBSD on every change, which is more than tier 2
  asks for and less than tier 1 describes. Whether the table should move
  is a question for §6 rather than a commit — but ranking Linux work
  above FreeBSD work, which this document did, was ranking one host
  above four.
- **Deploying from source demotes packaging and promotes upgrades.**
  SPEC 27.2's `.deb`, `.rpm`, `.msi` and `.pkg` serve nobody on this
  fleet. What `make install` from source *guarantees* is that a hub and
  its nodes run different versions for as long as an upgrade takes,
  because five hosts are not rebuilt in the same instant. SPEC 31's
  Upgrade row — hub at N with nodes at N−1 and N+1 — went from a
  theoretical gap to a condition this fleet enters deliberately, every
  time, and nothing establishes what happens.
- **The Salt differential now guards a translation, not a system.** SPEC
  31 calls it the primary correctness gate on the strength of "a corpus
  of real SLS and pillar trees from this estate". The estate runs no
  Salt. It remains the only check that can say an existing tree means
  the same thing under a reimplementation, and it is worth keeping — but
  deepening it to compare *applied* results, which this list had at
  number three, is work for a migration nobody is doing.

~~**Stand up CI.**~~ ~~**`hostname` and `ssh_known_hosts`.**~~ ~~**The
chaos suite.**~~ ~~**`subscriber_lag`.**~~ ~~**`doctor`.**~~ ~~**Compile
for tier 3.**~~ ~~**Run the suite on macOS and FreeBSD.**~~ All done.
§7.1 keeps the reasoning from the ones whose argument still earns its
place.

**Now — the fleet this actually runs on**

1. ~~**`pf`**~~ — **done**, the same day the re-rank put it first, and
   **run on a real FreeBSD host the same evening**, which found a defect
   in it. It manages an `anchor` rather than pf.conf, refuses to load
   rules into an anchor pf.conf does not reference — which would report
   rules the firewall never evaluates — and refuses a default policy,
   because pf has none. That last is the `firewall` interface being
   reshaped by its second provider, exactly as its own comment
   predicted, and it needed no change to the interface.

   The defect is the more instructive half. pf does not print back the
   text it is given — it reprints from its parsed form, `port = 9999`
   with a pass rule's default flags and state tracking appended — so no
   rule ever matched itself, both of `mail.edlitmus.info`'s rules were
   reported as added on every run, and `firewall.absent` could remove
   neither. The module's own comment had asserted the opposite and
   nothing had checked it, and the idempotence test passed because its
   fixture was written in the module's own spelling. That is §1.4's
   lesson about fixtures, repeated by whoever had just written §1.4, in
   a form §1.4 did not cover: it generalised about code branches and
   applies equally to another program's output. DIVERGENCE 5.31.
2. ~~**Upgrade testing**~~ — **done**, and it found a defect on the way.
   A job record carried no version marker and round-tripped through
   `job.Job`, so an older hub reading and writing back a record a newer
   hub had written silently dropped every field it did not know —
   eleven keys in and nine out, measured. That is the rollback case, one
   `git checkout` and one `make install` away on a fleet built from
   source. The record now carries `halite.job/1`, and a schema this
   build does not know is readable and refused for writing.

   It also **corrected an assumption**: Salt requires its server
   upgraded first and halite does not, because the wire is tolerant in
   both directions. That tolerance was accidental — `encoding/json`'s
   defaults and one `default:` branch — and is now a guarantee with a
   test on each direction. The ALPN is the only place skew is fatal, and
   it is frozen. DIVERGENCE 4.13.
3. ~~**`jail`**~~ — **done**, and written against what `pf` cost. It
   reads `jls --libxo=json` rather than the table, because parsing the
   human interface where a structured one exists is choosing the surface
   that bit `pf`; and `TestJailReadsWhatARealJlsPrints` runs the real
   `jls` on CI's FreeBSD runner and feeds the parser what came back,
   which is the test `pf` did not have. What is still assumed — the
   field names inside a jail entry — is marked as assumed and needs a
   host with a jail running. DIVERGENCE 5.32. **SPEC 15.3's FreeBSD row
   now ships entirely.**

4. ~~**Module evidence**~~ — **done**, and it is the generalisation of
   items 1 and 3 rather than a new idea. `pf` was the third time a
   fixture written from expectation had passed while asserting nothing
   (§1.3, §1.4, DIVERGENCE 5.31), and after three the shape is the
   finding. Every module that changes something now declares what has
   actually been demonstrated about it — run against the real tool on a
   real machine, run against it but only reading, or written from
   documentation — with a note naming the tool and the doubt.

   `sys.evidence` answers per module, `doctor` gains a **module
   verification** check that names the root-mutating ones nobody has
   demonstrated, a failing mutation carries the note, and
   `make release-gate` refuses a release in which any root-mutating
   module is still an assumption. The gate has one failure mode — a
   release does not happen — which is why it can be a gate rather than a
   warning. DIVERGENCE 5.33.

   Writing the table also found three wrong cross-references in the
   ledger, including one where CI's coverage was *weaker* than the
   document implied.

**Then — closing the gate, which this fleet can do**

5. **Demonstrate the modules the gate is still red on.** It was nine,
   then two; it is now **eleven**, and the arithmetic is worth stating
   plainly rather than buried. `make fleetcheck` closed four, the two
   live CI legs closed two more, `netplan` closed on this fleet's Ubuntu
   host (DIVERGENCE 5.38), `quota` closed on a runner (5.48), and `lvm`
   closed on this fleet's Ubuntu host (5.50) — but the macOS row added
   eight, because a module arrives undemonstrated and that is the
   correct state for new work. `apparmor` has since closed too (line 40
   above, DIVERGENCE 5.55); `snap` needs the network the no-network rule
   refuses; the eight macOS modules need a CI leg that is a Mac and
   writes to it, which none is.

   ~~**`quota`**~~ — **done**, and it cost six CI runs of which five
   were about the machine rather than the module. The leg makes an ext4
   filesystem in a file, mounts it through the loop driver, sets a limit
   through `setquota` and reads it back through `repquota -O csv`; it is
   green, and `quota` is `hardware`.

   The five runs are the useful part. Two were wrong guesses at ext4's
   two quota mechanisms — the feature was blamed for the classic route's
   failure on the strength of the symptom alone, and the next run
   disproved it. The run that settled it was the one that stopped
   guessing and **probed**: the runner's kernel was missing the
   `quota_v2` module *file*, present in `linux-modules-extra` and absent
   from the image, which neither mechanism works without. One
   `apt-get install` and it passed first time.

   Three lessons, and only the first is about quotas. A symptom is not a
   diagnosis. A probe is cheaper than a guess, measurably: two runs went
   on hypotheses and the third produced the answer outright. And a test
   that runs and skips **looks like progress in a log and is not** —
   five of those runs were a green Fleet job with a skipping test inside
   it, so the ledger now separates three things where it had two: the
   test existing, the test running, and the test reaching its
   assertions.

   What is still assumed is the *BSD* half. The fixed-width parser is
   written to `repquota.c`'s own printf calls, which is better than a
   fixture and is not a demonstration, and this fleet has no UFS
   filesystem to make one on. `edquota -e` remains an argument vector
   only.

   This stays the highest-ranked *unbuilt* item because nothing else on
   this list can ship a release until it is done.

   ~~`dpkg`~~, ~~`debconf`~~, ~~`pkgrepo`~~ and ~~`timezone`~~ are done,
   driven against a real Debian's own tools in a disposable container —
   destructively, because these are the mutating modules and a parser
   that reads correctly says nothing about whether the write took. It
   reaches no network, it runs nightly rather than gating pull requests,
   and each note names the distribution and what it does not cover.
   DIVERGENCE 5.35.

   ~~`hostname`~~ and ~~`sysctl`~~ are done too, and needed no new
   infrastructure at all — which is what the previous revision of this
   item was about to reach for. Neither can be done in a container:
   `sysctl` is the kernel and a container shares the host's, and Docker
   bind-mounts `/etc/hostname` so the atomic replace cannot work there.
   The machines were already in CI — a GitHub runner is a fresh virtual
   machine per job, and the FreeBSD one boots on every change. The two
   legs exercise *different branches*: `hostnamectl` and a sysctl
   drop-in on Linux; `sysrc`, rc.conf and `/etc/sysctl.conf` on FreeBSD,
   which is the branch §1.4 found had no test at all. DIVERGENCE 5.36.

   ~~`netplan`~~ is done, on this fleet's one Ubuntu host as forecast.
   The obstacle it was ranked for never applied: the module does not run
   `netplan apply` unless a declaration names it, so the test writes a
   document, has real `netplan generate` validate it, reads it back
   through `netplan get`, and removes it — none of which touches an
   interface, so the CI Linux leg runs it too. What is *not* covered is
   `netplan apply` itself, deliberately and by name. DIVERGENCE 5.38.

   **What is left, and what each actually needs**, in the order the
   effort is worth it:

   1. ~~**`apparmor`**~~ — **done**, and the "afternoon on a host that
      has one" §6 named as route 1 turned out to be this fleet's own
      Ubuntu host. The GitHub runner 5.37 used has the stock `passt`
      package, whose `abstractions/passt` apparmor-utils 4.0.1 cannot
      parse — breaking `aa-enforce`/`aa-complain`/`aa-disable` on
      *every* profile there, `/usr/bin/man` included, because the
      `aa-*` tools parse the whole tree before doing anything. This
      fleet's host has no `passt` package, so it has none of that
      syntax, and the same apparmor-utils 4.0.1 works on it: 148 real
      profiles read in all four modes, and a throwaway profile loaded,
      moved complain → enforce by name through the real tools, disabled
      with its reboot symlink confirmed, and unloaded. `apparmor` is
      `hardware`; the gate is red on nine, not ten. §6 item 11's actual
      question — teaching the module to change a persisted mode without
      the `aa-*` tools, for the host that *does* have an unparseable
      profile — is still open; DIVERGENCE 5.37 and 5.55 have the detail.
   2. **`snap`** — snapd is already on an Ubuntu runner and the obstacle
      is the network: `snap install` fetches, and there is no offline
      equivalent of the local apt repository `fleetcheck` uses. Either a
      pre-seeded snap or an exception to the no-network rule, and the
      exception is the wrong answer.

   Two lessons from doing the first seven, both worth carrying.

   The Debian run found six defects on its first attempt and every one
   was in the *test*, not the module — wrong argument names and wrong
   return shapes, written by somebody who had just read the interfaces.
   Expect that, and expect it to be the useful part.

   And **a live test that passes is not yet a test**. Of the two breaks
   pushed at `hostname` and `sysctl`, one was invisible to every
   assertion in the file, because `set_hostname` writes both halves and
   a module that confuses them agrees with one that does not. The two
   had to be pulled apart deliberately before anything could tell them
   apart. Break every live assertion on purpose before believing it.

**Then — phase 6, on a fleet that is in production.** Two of the four
were closed while this revision was being written, so the first genuinely
unbuilt item here is number 7.

6. ~~**Tracing**~~ — **done**, and it took two commits because the
   first was half of one. `internal/tracing` landed with the
   propagation, the span model, the sampler and the OTLP/HTTP JSON
   exporter and with nothing starting a span, which the ledger recorded
   and which was not sufficient: a package carrying a specification
   section's name reads as a feature whatever a document says.

   It is wired now. Three settings, `tracing` off the inert table, and a
   trace that runs from an operator's submission through the hub's
   dispatch, the node's job, each state that actually executed, each
   file that state fetched, and the hub's side of that transfer. The
   state span deliberately covers the states that *ran* rather than
   every declaration in the highstate, and carries whether the state
   changed anything, because a converged run is nearly every run.

   Two defects fell out of the wiring, which is the argument for doing
   it rather than shipping the seam: `Tracer.Stop` panicked when called
   twice, on the shutdown path, and a file transfer ignored its caller's
   context — so `jobs kill` did not stop a fetch. DIVERGENCE 5.34.

   **What is not established**: no span this build produces has been
   read by a real collector. That is the same shape as §5 above, one
   layer up, and the first estate to set `tracing: otlp` settles it.
7. ~~The two SPEC 30 benchmarks that need no harness~~ (§3.1) —
   **done.** Both targets are met with room, 96 ms against 2 s and
   154 ms against 500 ms, and the other eleven rows are now tracked with
   what each waits on rather than being thirteen numbers nobody had
   checked. The generated trees are asserted to the shape SPEC names,
   because a benchmark cannot fail and one measuring nothing reports an
   excellent number. DIVERGENCE 5.57.
8. ~~The render sandbox~~ (§3.3) — **done**, and the two things it did
   not settle are named rather than implied. The mechanism works on
   every platform the tree builds for and enforces different amounts on
   each, which `Describe` reports and the node logs; what is unexercised
   is the unprivileged account, because nothing here runs a node as
   root. Running it is what turned up both of its defects — an error
   message that named the file twice, and a `render_sandbox_user` that
   was accepted and ignored on any node that could not drop privilege.
   Neither was reachable from a test that passed. DIVERGENCE 5.58.
9. **Node evidence and detached signing** (§6). Supply chain, and it
   matters more now that the thing being supplied runs everything.

**Demoted, with the reason**

10. **Packaging** (§3.5) — was fifth, on the argument that the fleet
   needs a way to deploy. It has one. `make install` is FreeBSD-aware
   already: rc.d service files, `pw useradd` in its own error message.
   If any of SPEC 27.2 is built for this fleet it is a **FreeBSD port or
   pkg** rather than a `.deb`, and neither ranks above `make install`
   continuing to work. The reproducible-build half is done regardless.
11. **The Debian and Ubuntu row** (§2.3) — was first, through three
   revisions. `pro` and `debbuild` remain, on one host. `pro`'s design
   question is answered: a Pro-enabled FIPS node and a `GOFIPS140` build
   are **two** claims and both are required, which is what `doctor`'s
   FIPS check now says out loud. So it is buildable — it is simply worth
   less than it was when the estate was imagined to be Ubuntu.
12. **The Common Linux row** (§2.3) — was eleven modules and is now
    one, deliberately unbuilt. ~~`pam`, `quota` and `openssl_cert`~~ are **done**, taken
    first for the reason this item gave: they are the three that mean
    something on FreeBSD, which is four hosts of five. DIVERGENCE
    5.47-5.49. ~~`lvm`~~, ~~`iptables`~~ and ~~`nftables`~~ are **done**
    too, the next three, each closing a 15.5 state as well. `lvm`: the
    JSON report, grow-not-shrink, a loopback live leg on this fleet's
    Ubuntu host (5.50). `iptables` and `nftables`: idempotence from
    `iptables -C` and from nft comment tags respectively, `flush`
    guards on both, and live tests that run inside an **unprivileged
    network namespace** so nothing touches the host firewall and they
    need no CI gate — so both are `hardware` (5.51). All three are off
    the release gate. `iptables`/`nftables` are deliberately not
    `firewall` providers; §2.2's "reshapes the interface" prediction was
    the same one `pf` disproved.

    Two of the three cost more thought than the count suggests, and both
    lessons generalise. `pam` has **two include mechanisms** that are not
    spellings of each other — Debian's untyped `@include` and the typed
    `include` the BSDs and RHEL use — so a reader written on either
    platform reports a truncated chain on the other. And Linux's
    `repquota` report is **genuinely ambiguous**: a blank grace column
    means a row has six to eight numbers depending on the state of the
    filesystem, and a grace under an hour prints as a bare number, so no
    rule recovers which column is missing. That one is refused rather
    than guessed at, and Linux is read through `-O csv` instead.

    `openssl_cert` came out at `hardware` on the first afternoon, which
    is unusual here and worth naming: its mutating path writes a file it
    is told to write, in a directory a test owns, needing no root and no
    network — so the round trip through the real tool costs a
    `t.TempDir()` and runs wherever the suite does. It found a defect
    that way, an unreadable trust file being reported as an untrusted
    certificate.

    ~~`journald`~~ is **done**. The design note resolved pragmatically:
    the literal "native export protocol over a socket" is unreachable
    without a dependency (no cgo read API; the varlink socket only does
    rotate/flush/sync; compressed data objects need LZ4/XZ/ZSTD), so
    reads go through `journalctl -o json` — the `jls --libxo=json`
    precedent, a machine format not the aligned columns SPEC objects to
    — and the *control* verbs really do go over journald's varlink
    socket, through a new ~130-line `internal/varlink`. Both halves
    driven against real systemd 255. DIVERGENCE 5.52.

    ~~`mdadm`~~ is **done**, exec-only: 13 functions over `mdadm
    --detail` (parsed as `Label : Value` + a member table) and
    `/proc/mdstat` (the resync/recovery/reshape progress, the way
    `zpool status` reads a scrub). `create` refuses an existing array
    or a member that already carries an md superblock without `force`.
    A loopback live leg on this fleet's Ubuntu host built a RAID1 with
    a spare and ran fail/remove/add/save_config/stop against it.
    DIVERGENCE 5.53.

    ~~`modprobe`~~ and ~~`udev`~~ are **done**, both exec-only and both
    small: `modprobe` (10 functions) parses `/proc/modules` and
    `modinfo` directly and persists across the two files the kernel
    actually reads — a bare name in `/etc/modules-load.d` for "load at
    boot" and a `denylist-<name>.conf` in `/etc/modprobe.d` for "never
    load", the latter still spelled the old way inside the file because
    that is modprobe.conf(5)'s own directive; `udev` (6 functions)
    reads `udevadm info --export`/`--export-db` and can `trigger`/
    `settle`/`reload_rules`. Both driven against a real kernel and udev
    on this fleet's Ubuntu host, the mutating halves gated on root.
    DIVERGENCE 5.54.

    That closes eleven of the Common Linux row's twelve. **`authselect`
    is left pending, deliberately, not built from documentation.** It
    is Fedora/RHEL 8+ only in reality, whatever row SPEC 15.3 files it
    under; Debian and Ubuntu manage PAM through `pam-auth-update`,
    which `pam`'s own module already handles, and this project has no
    RHEL host to verify authselect against. It waits for the same
    reason `yumpkg`, `dnfpkg`, `rpm`, `firewalld`,
    `subscription_manager`, `dnf_module` and `chattr` do — shipping
    fixtures for a tool nobody here has run is exactly the mistake
    DIVERGENCE 5.31 found and this document keeps citing. Items 14-19
    below say exactly what machine closes each.
13. Deepening the Salt differential to compare applied results (§3.4).
    See above: it guards a translation that has already happened.

**Blocked on platform access** — nothing here is unbuilt because it was
skipped; each is written and waiting on a machine this project has
never had. The pattern items 1-13 set holds: don't ship a fixture for a
tool nobody has run against it (DIVERGENCE 5.31).

14. **A RHEL or Fedora 8+ host.** Closes `authselect` and the RHEL
    row's other six modules — `yumpkg`, `dnfpkg`, `rpm`, `firewalld`,
    `subscription_manager`, `dnf_module`, `chattr` (§2.3, all seven
    unbuilt, not merely unverified) — and lets the `pkg` module's
    dnf/yum provider be run for the first time: all four optional
    capabilities (`pkg.hold` through the `versionlock` plugin,
    upgrading, file ownership, repository listing) are implemented to
    the same shape apt's were and have never been exercised against a
    real dnf (DIVERGENCE §2.3/§2.5, evidence.go's `pkg` note).
15. **A SUSE host.** Closes `zypperpkg`, the SUSE row's one missing
    module (§2.3) — nothing built yet, not merely unverified, since
    this project has never had a SUSE machine to write it against.
16. **An Alpine host.** Verifies the `pkg` module's apk provider — it
    implements the upgrader and owner capabilities (not holder or
    repos: apk has no hold in the dpkg sense and no command that lists
    its repositories) and neither has been run against a real apk — and
    runs the `service` module's openrc provider for the first time,
    which Alpine also ships as its default init (evidence.go's
    `service` note: "the launchd, sysvinit and openrc providers ...
    have not been run at all").
17. **A non-systemd Linux with sysvinit** (Devuan, or Debian/Ubuntu
    with `sysvinit-core` in place of systemd). Runs the `service`
    module's sysvinit provider for the first time — same gap as 16,
    different init.
18. **A Mac that writes preferences, with a CI leg that is one.**
    Already the largest item on the release gate by count — the eight
    macOS row modules (§2.3, DIVERGENCE 5.41-5.46), each read-verified
    but mutating unwatched because no CI leg is a Mac that changes its
    own state. The same host also runs the `service` module's launchd
    provider for the first time, which macOS ships but nothing has
    reached (same evidence.go note as 16 and 17).

19. **A Linux host with a FIPS kernel, and one hardened to CIS Level
    2.** The newest item here and the one with the most behind it,
    because an estate that would actually run this is Ubuntu LTS with a
    FIPS kernel from a vendor channel, and everything below is a claim
    this project has made against a machine of a different shape.

    - **The FIPS artifacts have been run nowhere** (DIVERGENCE 4.5).
      They ship for Linux and no host has executed one.
    - **`doctor`'s FIPS consistency check has never seen a kernel that
      says yes.** It compares `/proc/sys/crypto/fips_enabled` against
      the binary's own mode, and the branch that matters — a compliant
      kernel under a build that is not one, which reads as compliant
      and is not — needs a kernel in FIPS mode to reach. §3.2 and
      DIVERGENCE 5.30.
    - **A vendor's FIPS channel is two claims and the grain reports
      one.** A certified frozen kernel and a patched one from an
      updates channel both write 1 to that file, and for an assessment
      they are different answers. The natural place to say which is a
      `pro` module, which is not built (§2.3, item 11 above).
    - **Every Linux evidence note was captured on one release.** The
      `service` provider over D-Bus and `journald` over its varlink
      socket were both driven against systemd 255, `netplan` against
      netplan 1.1.2, and `apparmor` against apparmor-utils 4.0.1. An
      LTS one version older carries systemd 249, the netplan 0.10x line
      before its rewrite, and apparmor-utils 3.x. None of the four is
      known to be broken there; all four are claims about a machine
      this project does not run, which is the shape of every finding in
      §1.
    - **Linux arm64 compiles and nothing more** (DIVERGENCE 4.5), and
      an estate of this kind is increasingly arm64.
    - ~~**A CIS Level 2 host will exercise a path that is written and
      unexercised, and probably break it.**~~ **The refusal is built**,
      which is the half that needed no such host. The agentless mode
      caches the pushed binary under `/var/tmp/halite-thin` and executes
      it there, and hardening benchmarks commonly mount `/var/tmp`
      `noexec`; every step up to the last one succeeds on such a host,
      so what an operator used to get was "Permission denied" about a
      binary installed successfully a moment earlier. The staging
      directory is now proved rather than assumed: the same script that
      creates it writes a probe, runs it, and removes it, and a target
      that will not execute is refused by name with `noexec` and
      `thin_dir` in the message. It is the check `OpenNodeCache` already
      makes for itself, asked about execution rather than about writing,
      and it costs no extra round trip. DIVERGENCE 5.60. **What still
      needs the host is the demonstration**: the script is exercised
      against a real `/bin/sh` and a real directory, and no `noexec`
      mount has ever refused it, because making one needs root on a
      hardened machine.

    The read half of all of this needs no root and writes nothing, so
    it is an afternoon rather than a project. The `-fips` artifacts, the
    grains, `doctor`, and the read paths of those four modules, in FIPS
    mode and out, is the whole of it.

**Blocked on a decision**

20. Whether FreeBSD belongs in SPEC 27.1 tier 1, given what it now
    carries and what CI already runs on it.
21. The `cmd.run` default, the unplanned modules, a `win_registry`
    state, and job signing (§6).
22. Whether a follower that falls behind the event bus should stop or
    resume. `subscriber_lag` refuses the read now; what a *reactor*
    should do with the refusal is the open half (DIVERGENCE 4.12).

**Last**

23. The YAML over-acceptance set — 20 documents halite reads that the
    reference refuses, which is now the whole of what is left worth
    taking there, the chomping case having turned out to be a
    mismeasurement with a real defect behind it (§5, DIVERGENCE 5.56);
    the six real template gaps; the regexcompat character-class false
    positive.

---

### 7.1 What the closed items are kept for

**CI** held the top of this list through three revisions, and the cost
of it ranking second was paid three times: two weeks of a package that
did not compile on Linux, a day of a red suite on Windows, and a race
detector that ran when somebody remembered rather than on every change —
which is not often enough to catch a one-in-three race.

**macOS and FreeBSD in CI** was wrong twice before it closed. It said
"neither is a GitHub-hosted runner"; macOS is one and always was, and
the sentence was written about FreeBSD and let macOS ride along
unchecked. Then it said FreeBSD needed "a decision about infrastructure
rather than a morning's work", on the reasoning that emulation would be
too slow to sit in front of a pull request: `test (freebsd)` runs in
**1m44s**, faster than the Windows leg. Between them the two runners
found five defects in a day, and FreeBSD has since found two more that
no other runner did — which reads differently now that FreeBSD is most
of the fleet.

**Tier 3** was the fifth time §1's argument was made. Nothing had ever
built for OpenBSD, NetBSD, Solaris, illumos, or Linux on riscv64,
ppc64le or s390x, so the one tier whose whole promise is "compiles" was
the one tier nothing compiled; four of nine targets failed on the first
attempt. It has since caught a defect within a working day, in
`doctor`'s free-space code. What it does **not** establish is that
anything runs on those seven platforms — which is exactly the claim SPEC
27.1 makes for tier 3, and no more.
