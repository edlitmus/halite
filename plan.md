# Remaining work

A plan for what is left between this build and SPEC section 32's phase 6
exit criteria, written by comparing SPEC.md against the code as it
stands.

**Method.** Every count below was measured against the build rather than
read out of `docs/DIVERGENCE.md`: the module and state registries were
interrogated through `sys.list_modules` and `sys.list_state_modules`, the
refusal registry was counted, and each named feature was traced to the
line that implements or refuses it. Where a claim here is a count, the
command that produced it is one a reader can run.

**Date of measurement:** 2026-09-04, against `c6a9656`, on
windows/amd64 with Go 1.26.6. The previous revision of this document was
written on 2026-09-02 against `57b0d39`; forty-three commits have landed
since, and section 0 says which of its findings are now closed.

---

## 0. Where the project actually stands

Phases 0 through 4 are complete. Phase 5 is most of the way built.
Phase 6 has not started.

| Phase | State |
|---|---|
| 0. Foundations | Done. |
| 1. Local state and pillar | Done, within a module inventory that is about half of what SPEC 15 names. |
| 2. Hub, transport, enrollment | Done. Outstanding: external pillar, `halite-hub files`, return chunking, the event-bus indexes. |
| 3. The automation loop | Done. Outstanding: `salt.parallel`, the queue runner, live pause/resume, beacons and schedules through pillar, the node-side bus. |
| 4. API and integration | Done, including the bridge protocol and sandbox. Outstanding: no reference bridge extension ships. |
| 5. Breadth | gitfs, s3fs, agentless mode, relays and the FIPS artifact set are built. Windows parity is largely done and verified on a real host; macOS has providers but no module set. **59 of SPEC 15.3's 65 platform modules and 47 of SPEC 15's core modules remain.** |
| 6. Hardening to 1.0 | Not started. No benchmarks, no chaos suite, no packaging, no CI, no node evidence, no detached signing. |

### 0.1 What the previous revision listed and what has closed

Its section 1 said nine ledger claims were wrong; all nine are corrected
and the drift guards now cover Markdown prose and the waiver reasons.
Its section 2.1 said `make check` failed on three tests; it does not.
Sections 2.2 and 2.3 are closed — the inert keys warn at startup and
`enrollment_mode: attested` is refused by name. Section 3.1 (`{% break
%}`), 3.2 (the `grains` state), the `CatUndefined` row of 3.6, and most
of 3.3 and 3.5 have landed. Section 4.1's Linux provider depth is done.

Two of its items were overtaken by evidence rather than by work.
Section 4.3 called Windows "the single largest remaining block of work in
the project" and said question 9 should be answered before it was
scheduled; it was scheduled, and section 1 below is what running it
found. Section 5.4's container is built.

---

## 1. What running on Windows established, and what it did not

The suite had never been run on Windows. On the first native run it
failed **80 tests across 12 of 55 packages**; it now passes all 58 with
no skips. `docs/DIVERGENCE.md` section 4.6 is the full account. Three
things from it belong in a plan rather than a ledger:

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

---

## 2. Phase 5's real remainder: the module inventory

This is the largest block of work left, and it is the one that decides
whether the estate can migrate. The registries answer:

```
halite-node call sys.list_modules        # 50
halite-node call sys.list_state_modules  # 27
```

against SPEC 15.2's 56 core execution modules, 15.5's 47 core state
modules, and 15.3's 65 platform modules.

### 2.1 Core state modules whose execution side is partly there

The previous revision of this section called these "the state wrapper and
nothing else", and that was wrong. The execution modules named here are
registered, but what they register is the *reading* half:

```
mount.active                                    timezone.get_zone
environ.get  environ.has_value  environ.items   zpool.healthy  zpool.list
```

There is no `mount.mount`, no `timezone.set_zone`, no `environ.setval`.
So each of these is a state *and* the mutating execution functions under
it — still small, still worth doing early, but two pieces rather than
one, and a plan that said otherwise would have had somebody discover it
an hour in.

| State | What the execution side has | What it also needs |
|---|---|---|
| `timezone.system` | `get_zone` | `set_zone`; `timedatectl` on Linux, `/etc/localtime` elsewhere, `tzutil` on Windows |
| `environ.setenv` | `get`, `has_value`, `items` | `setval`, and a decision about what "permanent" means: `/etc/environment` on Linux, the registry on Windows |
| `mount.mounted` | `active` | `mount`, `umount`, `remount`, `set_fstab` |
| `zpool.present` | `healthy`, `list` | `create`, `destroy`, `export`, `import` — the largest of the four |
| ~~`beacon.present`~~ | the `beacons` module ships whole | **done** — the state only, so this one really was just the wrapper |

`schedule` is the same shape as `beacon`, and this section previously
said it had no state at all. It had one: `schedule.absent`, added with
the five small gaps a real tree reached for. What it did not have was
`present`, and its `absent` did not write the running set back — so a job
removed by a state came back on the next restart. Both are fixed, and
the two states are one implementation because they are the same state
twice.

### 2.2 Core modules missing entirely

**Execution, 21 of SPEC 15.2**: `at`, `acl`, `apparmor`, `blockdev`,
`data`, `firewall`, `hostname`, `kernelpkg`, `locale`, `logrotate`,
`nfs`, `ps`, `reboot`, `selinux`, `shadow`, `state`, `sudo`, `swap`,
`system`, `tls`, `tmpfs`. `http` and `pkgrepo` have since shipped.

**State, 23 of SPEC 15.5**: `acl`, `apparmor`, `at`, `beacon`,
`environ`, `firewall`, `hostname`, `iptables`, `kernelpkg`, `locale`,
`logrotate`, `lvm`, `mac_defaults`, `mount`, `nftables`, `pro`,
`reboot`, `selinux`, `ssh_known_hosts`, `sudo`, `timezone`, `win_wua`,
`zpool`. `pkgrepo` has since shipped.

Ranked by what the estate's own tree reaches for, and by what a
migration is blocked on. **`pkgrepo` and `http` are done**, and are struck
from the ranking rather than left in it:

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
gets "this build does not ship it yet" rather than "unknown module".
That is the difference between a gap and a typo, and it is already done.

| Family | Missing | Why it ranks where it does |
|---|---|---|
| Debian and Ubuntu | 10 | **The estate is Ubuntu.** `aptpkg`, `dpkg`, `apt_key`, `ufw`, `netplan`, `snap`, `pro`, `debconf`, `debbuild`, `apparmor`. |
| Common Linux | 12 | `systemd_service`, `journald`, `iptables`, `nftables`, `lvm`, `mdadm`, `pam`, `modprobe`, `udev`, `quota`, `openssl_cert`, `authselect`. |
| macOS | 10 | The providers ship; the `mac_*` modules do not. |
| Windows | 14 | Four ship. No user or group provider. |
| RHEL | 7 | `yumpkg`, `dnfpkg`, `rpm`, `firewalld`, `subscription_manager`, `dnf_module`, `chattr`. |
| FreeBSD | 5 | The development platform, still unbuilt. |
| SUSE | 1 | `zypperpkg`. |

Note the overlap with 2.2: `iptables`, `nftables` and `lvm` are named in
both 15.3 and 15.5, so building the module and building its state are one
piece of work.

### 2.4 Function-level shortfalls inside modules that ship

`file` has 32 of the ~50 SPEC 15.2 enumerates — `patch`, `sed`,
`hardlink`, `list_backups`, `restore_backup`, `seek_read`, `seek_write`
and the SELinux context pair are the notable absences, along with
`file.accumulated`, which SPEC 15.5 promises by name because trees use
it. `pkg` has 18 of 26, `service` 16 of 18, `cmd` 12 of 13.

### 2.5 The rest of phase 5

- The agentless **reverse tunnel** (21.1). Trees go inline and anything
  over 4 MiB is refused by name.
- The `scan`, `cloud` and `terraform` **rosters** (21.2), each refused by
  name.
- **`minionfs`/`nodefs`** (13.2), a warning rather than a refusal.
- **No reference bridge ships.** SPEC 20.3 promises in-tree `postgres`
  and `sqs` as worked examples. The protocol, the sandbox, the adapter
  and the name lookup are all built, so all 16 bridged returners are
  reachable and unpopulated.

---

## 3. Phase 6: nothing in it exists

Grouped by what each unblocks.

### 3.1 Nothing is measured (SPEC 30)

`grep "func Benchmark"` returns **zero**. Two of SPEC 30's thirteen rows
name a benchmark as their own measurement method — highstate compile
under 2 s, pillar compile under 500 ms cold and 5 ms cached — so those
are cheap and can land immediately. The rest need the simulated node
harness: 20,000 nodes per hub, 10,000-node dispatch windows, 5,000
events/second, hub memory under 4 GiB, node memory under 40 MiB idle.
None of the thirteen is known to be met or missed.

### 3.2 The observability trio (SPEC 26)

- **11 of 32 metric families are unregistered**, guarded in both
  directions by `TestLedgerMetricGapMatchesTheBuild`. All three extension
  counters are among them: the extension model ships entirely
  uninstrumented, so a bridged extension timing out is a job failure with
  no counter behind it. Watch one trap: `halite_pillar_failures_total`
  **is** registered but is a different metric from the spec's
  `halite_pillar_ext_failures_total{source}`, so an alert written from
  the table silently matches nothing.
- **The node exposes no metrics at all.** `internal/metrics` is imported
  by `hub`, `api` and `relay` only. Everything node-local is uncounted —
  beacon queue drops, local state run duration, scheduler `maxrunning`
  skips — and SPEC 26.2 requires a counter on every bounded queue and
  every drop path.
- **Tracing (26.3) and `doctor` (26.4) do not exist.** `doctor` has one
  passing mention in a comment. It is also where SPEC 27.4 puts the FIPS
  grain-mismatch warning, so that warning has nowhere to live.

### 3.3 The security model's unbuilt half (SPEC 25)

- **The render sandbox (25.4) does not exist.** SPEC puts all YAML
  parsing and template rendering in an unprivileged child with no
  network, because "the parser and the template engine are the largest
  and most attacker-adjacent code in the system, and they need no
  privilege at all". Both run in-process in the privileged parent. The
  Linux seccomp allowlist and capability drop are likewise absent.
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

### 3.4 Testing layers that do not exist (SPEC 31)

- **The chaos suite is entirely absent** — `grep -i chaos` over all Go
  source returns zero. SPEC names eight scenarios, each of which must
  have "a defined, tested, documented behaviour": hub restart mid-job,
  network partition, disk full, clock skew, certificate expiry mid-run,
  extension hang, event bus at retention limit, reactor queue overflow.
  Several are the code paths most likely to be wrong, because they are
  the ones no test reaches.
- **The Salt differential now runs** — `make saltdiff` builds a container
  carrying Salt's onedir bundle, and all three comparisons pass over ten
  trees against 3007.1. What it compares is still narrower than SPEC 31
  asks: the low state, the pillar, and test-mode predictions, not applied
  results. Applying a tree twice under both implementations and comparing
  `changes` is the next step and needs the same container.
- **Coverage.** Two of SPEC 31's four correctness-core packages are below
  the 90% bar on the more forgiving statement metric: `internal/template`
  82.0% and `internal/target` 89.2% (`internal/yaml` 96.3%,
  `internal/state` 90.1%). Branch coverage, which SPEC actually
  requires, is unmeasured and will be lower.
- **Upgrade testing** (hub at N with nodes at N−1 and N+1, cache format
  migration, certificate rotation across an upgrade) does not exist.
- **Integration testing** across the tier 1 matrix does not exist. The
  saltdiff image is the only container in the repository, and it is a
  correctness harness rather than an integration one.

### 3.5 Packaging, release and CI (SPEC 4.3, 27.2)

- **No artifact in SPEC 27.2 is built.** No nfpm config, no `.msi`, no
  `.pkg`, no container image, no SBOM, no provenance attestation.
  `make release` builds bare binaries into `bin/`. `contrib/` has systemd
  units and FreeBSD rc.d scripts, and that is the whole packaging story.
- **There is no CI at all** — no `.github/`, no GitLab config, no
  Jenkinsfile. SPEC 4.2 opens by saying the dependency policy "has teeth:
  CI enforces it", and 4.3 assigns CI the dependency assertion and the
  two-builder reproducibility check on every tag. The *checks* mostly
  exist as `make` targets and Go tests; nothing runs them automatically.
- **Reproducibility is one builder, not two.** `make repro` builds twice
  on one machine from two paths, and its own comment is honest about the
  difference.
- Toolchain provenance — fetch by digest from an internal mirror — is not
  implemented.

**Standing up CI is the highest-leverage item in this document.** It is
what keeps everything else from drifting back, and the last three
revisions of this file each had to open by correcting claims that drifted
because nothing enforced them.

---

## 4. Settings that are accepted and do nothing

Twelve keys are **inert**: they warn at startup naming what the operator
gets instead, which is the honest half, and they still do nothing.
`job_cache`, `quiesce`, `quiesce_allowlist`, `startup_states`,
`parallel_jobs`, `socket_dir`, `node_data_cache`, `hub_type`,
`legacy_acl`, `pillar_cache_disk`, `ext_pillar_fail`, `tracing`.

`pillar_cache_disk` deserves separate mention: it is documented as
caching pillar "encrypted at rest" (SPEC 12.8), and **no encryption
primitives exist in the tree at all** — no AES-GCM, no ECDH, no RSA-OAEP.
Implementing it means writing the SPEC 25.3 encrypted-pillar stack, not
wiring a flag.

Five more are unread with a reason: `job_signer_keys` and
`require_job_signature` wait on phase 6; `log_level_file`,
`regex_engine` and `node_id_source` are settings with one value.

---

## 5. The conformance tail

Lowest priority, and all three suites pass with tables enforced in both
directions, so nothing here is silently rotting.

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
  defect: detection is a raw substring scan, so a construct spelling
  inside a character class — `[(?=]` — is a false positive, and no test
  covers it.

---

## 6. Questions that need a person, not a commit

1. **The `cmd.run` shell default (SPEC 33.3).** **54 of the estate
   report's 93 review findings** are one category: a `cmd.run` naming a
   program with arguments, pipes or `||`, which this build treats as a
   single program name because it runs without a shell. None blocks, so
   they are all latent breakage at migration. Decide whether
   `cmd_default_shell: true` is the estate-wide setting for a period, or
   whether 54 call sites get rewritten. **This is now a scheduling
   decision with numbers attached and it should be taken before the
   estate starts rewriting states.**
2. **Modules SPEC never planned for** but the estate uses:
   `alternatives` (3 references), `docker_container`/`docker_image` (2),
   `rabbitmq_policy`/`user`/`vhost` (3), `kmod` (1), `macpackage` (1).
   Amend SPEC, bridge them, or rewrite the tree.
3. **A `win_registry` state.** SPEC 15.5 does not name one, so none
   ships. Salt has `reg.present` and an estate migrating from it will
   want the same; a registry value that can only be set through
   `module.run` reports a change on every run.
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
9. **Strict undefined (33.4)** can now be answered: `CatUndefined` is
   implemented, so the migration report emits the undefined-reference row
   SPEC 28.5 requires.

---

## 7. Suggested order

Sequenced by value per unit of work, and by what unblocks what.

**Now — the estate's own blockers**

1. ~~`pkgrepo`, exec and state~~ — **done.** Virtual, with providers for
   apt, dnf/yum and Chocolatey. The convergence test caught the defect
   worth knowing about: a declaration carries `gpgcheck` on every
   platform and apt has no such concept, so the provider rather than the
   state has to answer whether a declaration matches.
2. ~~`http`, with SPEC 15.2's security contract~~ — **done.** The address
   denylist is in the dialer rather than on the URL, so it survives a
   name that resolves to the metadata service, a redirect into it, and
   DNS rebinding.
3. ~~`beacon` and `schedule` states~~ — **done.** One implementation for
   both, because they are the same state twice. `schedule.absent`
   already existed and did not persist, so a job a state removed came
   back on the next restart.
4. **The rest of §2.1** — `timezone`, `environ` and `mount`, each of
   which needs its mutating execution half as well as the state.
5. **`hostname`** and **`ssh_known_hosts`**. Small and universal.

**Next — stop the drift**

5. **Stand up CI**, running `make check`, the conformance suites and
   `make saltdiff`. Everything above stays fixed only if something
   enforces it (§3.5).
6. **The Debian and Ubuntu platform row** (§2.3). Ten modules, and the
   estate is Ubuntu.

**Then — phase 6 foundations**

7. The two SPEC 30 benchmarks that need no harness, then node-side
   metrics and the eleven missing families (§3.1, §3.2).
8. Get the differential to compare *applied* results, in the container it
   already has (§3.4).
9. The chaos suite, starting with the paths no test reaches at all.
10. Packaging and the two-builder reproducibility check (§3.5).

**In parallel, cheap and independent**

11. **Run the suite on macOS and FreeBSD.** Windows found three
    cross-platform defects in one afternoon; there is no reason to think
    those two hold none (§1).

**Blocked on a decision**

12. The `cmd.run` default, the unplanned modules, a `win_registry` state,
    job signing and node evidence (§6).

**Last**

13. The YAML over-acceptance set, prioritising the chomping case; the six
    real template gaps; the regexcompat character-class false positive
    (§5).
