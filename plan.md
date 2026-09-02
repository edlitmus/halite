# Remaining work

A plan for what is left between this build and SPEC section 32's phase 6 exit
criteria, written by comparing SPEC.md against the code as it stands.

**Method.** Every count below was measured against the build rather than read
out of `docs/DIVERGENCE.md`: the module and state registries were interrogated
through `sys.list_*`, the conformance suites were run, and each named feature
was traced to the line that implements or refuses it. Where this document and
the ledger disagree, section 1 says so and the code is cited.

**Date of measurement:** 2026-09-02, against `57b0d39` plus an uncommitted
`go.mod` edit, on darwin/arm64 with Go 1.27.1. Section 2.1 was re-measured
after the `go.mod` fix by running `make check` and then each stage it does not
reach.

---

## 0. Where the project actually stands

Phases 0 through 4 are complete. Phase 5 is roughly two thirds built. Phase 6
has not started.

| Phase | State |
|---|---|
| 0. Foundations | Done. The migration report has now run against a real 129-file estate tree, so the exit criterion is met in substance. |
| 1. Local state and pillar | Done, within a module inventory that is a third of what SPEC 15 names. |
| 2. Hub, transport, enrollment | Done. Outstanding: external pillar, `halite-hub files`, return chunking, the event-bus indexes. |
| 3. The automation loop | Done. Outstanding: `salt.parallel`, the queue runner, live pause/resume, beacons and schedules through pillar, the node-side bus. |
| 4. API and integration | Done, including the bridge protocol and sandbox. Outstanding: no reference bridge extension ships. |
| 5. Breadth | gitfs, s3fs, agentless mode, relays and the FIPS artifact set are built. **Windows and macOS parity is not started** (2 of 65 platform modules). |
| 6. Hardening to 1.0 | Not started. No benchmarks, no chaos suite, no packaging, no CI, no node evidence, no detached signing. |

`internal/specaudit` and `internal/docsaudit` both pass, so the ledger's
*tables* are honest. Its *prose* has drifted, which matters because the prose
is what a reader plans from.

---

## 1. Correct the ledger before planning from it

`docs/DIVERGENCE.md` is the project's planning input, and nine of its claims
are now wrong. This is first on the list because every later estimate is drawn
from it. Each of these survived the most recent commit, which edited the file.

| # | Claim | Reality |
|---|---|---|
| 1 | Header: "phases 0 and 1 are complete. Phases 2 through 6 have not been started"; "FreeBSD 15.1 … the only platform on which any of this has been run" | Phases 0–4 complete, 5 started. Linux is verified against a real system; macOS builds natively. The file's own section 4 table says so. |
| 2 | 6.1a: "What is **not** built in the API: Nothing. Phase 4 is complete." — followed by a stale bullet list | Editing artifact. The list still names the bridge protocol as absent, contradicting the same section's "The extension model is built" eleven paragraphs earlier. |
| 3 | 6.1a: "The rest of phases 5 and 6 does not exist: no FIPS artifact set…" | 6.1b says the FIPS artifact set **is** built. Direct self-contradiction. |
| 4 | 6.1: `file_ignore_regex` is "declared and read by nothing" | Fully implemented — `internal/fileserver/roots.go:47-69,201-217`, enforced on fetch and listing, fatal on a malformed pattern. |
| 5 | 6.1: "the node peer policy of SPEC 19.5 is not built" | The `node:` principal peer read path is built and enforced — `internal/hub/mine.go:263-300`. Only the *execute* half (`publish.*`) is absent. |
| 6 | 6.1a: "`enrollment_mode: attested` is refused by name rather than accepted and ignored" | It is **accepted**. See item 2.3 below — this one is a behavioural defect, not just a doc error. |
| 7 | 6.3: the migration tool "has been run against synthetic trees only — never against a real Salt tree of any size" | Contradicted by the file's own 5.9 and 5.26, and by `complex-tree.txt`. Phase 0's exit criterion is met. |
| 8 | 5.x: the Salt differential covers "eight trees" (line 1058) / "nine trees" (line 1383) | Ten — `internal/saltdiff/testdata/trees/`. |
| 9 | "seventeen" bridged returners, repeated in SPEC 20.3 prose, `internal/returner/bridged.go:14`, and the ledger | `BridgedNames` lists **16** — `internal/returner/returner.go:236-240`. |

**Work:** correct all nine. Then close the hole that let them drift. The
existing guard, `TestNothingClaimsADeliveredPhase`, reads Go string literals
and skips `_test.go` files and Markdown, which is exactly why these survived.
Extend it to `docs/DIVERGENCE.md` and `README.md` prose and to the waiver
reasons in `internal/config/unread_test.go`, keyed off the existing
`DeliveredPhases` constant.

---

## 2. Broken or silently inert today

### 2.1 `make check` fails on three tests (hours)

**Fixed.** The `go` directive is now `1.26.6`, so `go vet ./...` exits 0 and
`internal/fips` builds and passes. That was the only failure blocking a whole
stage.

**What `make check` does now.** It clears `fmt`, `vet` and `build-all` — the
latter green on all eight target platforms — then halts at `test` on **three
failing tests in two packages**. Because `check` is a serial dependency chain
(`fmt vet build-all test race policy fips-test`), those three tests stop it
before `race`, `policy` and `fips-test` ever run, so I ran each separately:

| Stage | Result |
|---|---|
| `fmt`, `vet`, `build-all` | Pass (8/8 platforms) |
| `test` | **Fails** — 3 tests, 2 packages |
| `race` | **Fails** — the same 3 tests, and **zero data races** across the tree |
| `policy` | **Fails** — the lexicon test, which is one of the same 3 |
| `fips-test` | **Fails** — the same 3, with **no FIPS-specific failures** |

So the whole of `make check` is gated on three tests, and nothing else is
hiding behind them. That is a better position than the stage list suggests: the
race detector is clean over every package, and the FIPS suite has no failure of
its own.

The three:

- **`TestLexiconPolicy` fails on a file that is not part of the project.**
  `internal/buildpolicy`'s scan walks the whole tree; `ExemptPaths`
  (`internal/buildpolicy/lexicon.go:70-91`) skips `vendor/`, `.git/`, `bin/`
  and `dist/` but not `.claude/`, so a developer's git-ignored editor config
  fails the project's own build gate. Add `.claude/`, or skip git-ignored paths
  generally. This one also fails `make policy` on its own.
- **Two darwin-only failures in `internal/builtin`**
  (`pkg_more_test.go:131`, `user_password_test.go:68`): both assert error text
  that no darwin branch produces. They are the macOS gap surfacing as red
  tests, and they will stay red until item 4.3 lands. Either skip them by
  platform with a reason, or fix the messages.

**Two things about `check` itself, worth fixing before it becomes CI (§5.5):**

- **The `go.mod` fix dropped the `toolchain` directive**, which SPEC 4.3
  requires by name: "pinned Go toolchain version in `go.mod` via the
  `toolchain` directive". The `go 1.26.6` line still sets a floor, but the
  explicit pin the reproducibility control asks for is gone, and **nothing in
  the tree checks for it** — no test, no Makefile assertion. Restore the
  directive and add an assertion in `internal/buildpolicy`, which is where the
  other SPEC 4.2/4.3 build rules already live. A reproducibility control that
  one edit can silently remove is not a control.
- **`check` begins by rewriting the tree.** `fmt` is `gofmt -l -w cmd internal`
  (`Makefile:166-167`), so `check` can never fail on formatting — in CI it
  would reformat and pass, reporting nothing. CI needs the `-l`-only form,
  failing when the list is non-empty. (The tree is in fact already formatted:
  the run above changed nothing.)

### 2.2 Eleven config keys are accepted and do nothing (days)

`internal/config/unread_test.go:24-48` waives 25 declared keys as unread. For
**eleven of them the stated reason is a phase that has already shipped**:

`job_cache`, `quiesce`, `quiesce_allowlist`, `startup_states`, `parallel_jobs`,
`socket_dir`, `node_data_cache`, `hub_type`, `legacy_acl`, `pillar_cache_disk`,
`ext_pillar_fail` — all waived "phase 2", plus `tracing` waived "phase 2: no
spans are emitted yet".

I confirmed each is genuinely inert: the only greps outside `keys.go`,
`keydoc.go` and tests are one coincidental wire constant. `IsKnownKey` accepts
them, so **nothing warns** — an operator sets `job_cache` or `startup_states`
on a build where phase 2 landed and gets silence.

Two of them state the opposite of the truth in their own documentation:
`internal/config/keydoc.go:905` says `require_job_signature` is "named and
refused rather than accepted and ignored", and `:947` says the same of
`tracing`. Neither is read by anything.

**Work:** for each key, either implement it or warn at startup naming the
section. The correct pattern already exists — `cmd/halite-hub/serve.go:329-331`
does exactly this for `ext_pillar`. Then make the waiver reasons machine-checked
against `DeliveredPhases` so a shipped phase cannot remain an excuse.

`pillar_cache_disk` deserves separate mention: it is documented as caching
pillar "encrypted at rest" (SPEC 12.8), and **no encryption primitives exist in
the tree at all** — no AES-GCM, no ECDH, no RSA-OAEP. Implementing it means
writing the SPEC 25.3 encrypted-pillar stack, not wiring a flag.

### 2.3 `enrollment_mode: attested` silently degrades to manual (days)

`internal/keystore/authority.go:34-47` accepts `attested` as a valid mode, and a
test pins that acceptance. But the automatic branch at `:183-196` fires only for
`ModeToken`, so with `attested` configured every CSR falls through to `Pending`
(`:198-202`). There is no attestation verification anywhere — no instance
identity document, no TPM, no IMDS. `SourceAttested` exists as an enum value
(`internal/keystore/keystore.go:64`) and is never assigned.

The failure is fail-safe (pending, not auto-accept), so this is a
misconfiguration trap rather than a hole. But an operator who configures
attested enrollment believes attestation is being checked, and both the ledger
and `keydoc.go:181` tell them the mode is refused.

**Work:** refuse `attested` by name at startup now — a one-line change that
makes the docs true — and schedule the real implementation with the phase that
takes the cloud-metadata dependency.

---

## 3. Make the estate migratable

This is the point of the project, and it is the workstream with the clearest
evidence behind it. The migration report against a real shared estate tree
(129 state files, 64 pillar files, 193 rendered) returns **205 blocking
findings, 93 for review, 6 notes**, with an effort split of state 209, yaml 62,
pillar_grain 12, custom_module 11, module 9, parse 1.

Ranked by references-unblocked per unit of work:

### 3.1 `{% break %}` and `{% continue %}` (small, highest ratio)

The single hard parse failure across 193 real files. Salt enables three Jinja
extensions — `do`, `with_`, and `loopcontrols`; this build has the first two.
Not in `internal/template/lex.go` or `parse.go`, and the vendored Jinja corpus
does not cover it, so only a real tree found it. One feature, one call site
blocked today, and it will recur in any tree that loops.

### 3.2 The `grains` state module (small, 11 references)

The largest single gap in the tree. Every grains *execution* function ships;
there is no grains *state*, so a tree that sets a grain declaratively has
nowhere to put it. Needs `grains.present` and `grains.absent`.

### 3.3 State functions missing from modules that already ship (medium)

`file.recurse` (4), `pkgrepo.managed` and `pkgrepo.absent` (5),
`test.show_notification` (4), `pkg.purged` (3), `file.serialize` (2), and one
reference each to `file.rename`, `file.get_user`, `grains.absent`,
`schedule.absent`, `mount.mounted`, `shadow.gen_password`, `event.send`.

Note `pkgrepo` exists as neither an exec nor a state module, and there is no
`schedule` or `beacon` state module at all — so a tree cannot declare a
schedule or a beacon even though both exec modules ship (12 and 10 functions).

### 3.4 Reactor call-shape compatibility (small, 6 references)

`saltutil.runner` is Salt's other way to call a runner from a reaction; this
build accepts only `runner.<function>`. Also absent as execution functions
callable from a reaction: `state.apply` and `grains.set`.

### 3.5 Eight `file` functions are states but not exec functions (medium)

`file.managed`, `file.directory`, `file.symlink`, `file.touch`, `file.line`,
`file.blockreplace`, `file.comment`, `file.uncomment` are reachable from an SLS
file but are **not** in the exec registry — verified against
`sys.list_functions`, which returns 32 `file.*` names and none of these. In Salt
they are execution functions. So `salt['file.managed'](…)` in a template and
`halite-node call file.managed` both fail where Salt succeeds. `file.managed` is
the single most-referenced function in the estate tree (122 references), so any
tree that calls it from a template is blocked.

Also missing and explicitly promised by SPEC 15.5 ("is supported because trees
use it"): **`file.accumulated`**. No such code exists.

### 3.6 Decisions, not code (needs a human)

- **Modules SPEC never planned for**, but which the estate uses:
  `alternatives` (3), `docker_container` / `docker_image` (2), `rabbitmq_policy`
  / `rabbitmq_user` / `rabbitmq_vhost` (3), `kmod` (1), `macpackage` (1). Not
  gaps against SPEC — but migration blockers. Amend SPEC, bridge them, or
  rewrite the tree.
- **Seven state functions reject arguments Salt accepts**: `user.present`
  (`mindays`, `maxdays`, `inactdays`, `unique`, `optional_groups`,
  `enforce_password`), `archive.extracted` (5), `group.present` (`system`,
  `members`), `file.managed` (`skip_verify`, `keep_source`), `file.replace`,
  `pkg.installed`, `git.latest`. The `user.present` row is one coherent
  feature — shadow ageing policy — not six oversights.
- **`module.run` argument validation.** Salt passes unknown kwargs through to
  the function being run; this build validates against a fixed parameter list.
  Strict validation is right for every other state and wrong for this one.
- **The `cmd.run` shell default (SPEC 33 question 3) now has its data.**
  **54 of the report's 93 review findings** are one category: a `cmd.run`
  naming a program with arguments, pipes or `||` in it, which this build treats
  as a single program name because it runs without a shell. That is the largest
  review category by a wide margin and the concrete cost of the SPEC 15.2
  inversion. None of them blocks, so they are all latent breakage at
  migration — decide whether `cmd_default_shell: true` is the estate-wide
  setting for a period, or whether 54 call sites get rewritten.
- **Missing from the report itself:** SPEC 28.5 requires an
  "Undefined references" row. `CatUndefined` is declared at
  `internal/migrate/migrate.go:44-45` and **never emitted**. Until it is, no
  report can tell an estate what strict-undefined will cost it — which is
  SPEC 33 question 4's decision input.

---

## 4. Finish phase 5: platform parity

### 4.1 Linux provider depth (the highest-value platform work)

The ledger ranks "a Linux host" first, and a real Linux node is now verified.
The gap is not the platform-neutral code but the providers:

**Eight of 18 `pkg` functions refuse on apt, dnf, yum and apk.** The base
provider interface is 6 methods (`internal/builtin/pkg.go:17-34`); holding,
upgrading, ownership and repository management are optional interfaces
(`pkg_more.go:27-52`) that **only `pkgngProvider` implements** — a code comment
at `pkg_more.go:322` reads `// ---- pkgng, the provider this host runs ----`.
So `pkg.hold`, `pkg.unhold`, `pkg.list_holds`, `pkg.upgrade`,
`pkg.list_upgrades`, `pkg.owner`, `pkg.file_list` and `pkg.list_repos` work on
FreeBSD only. Effective Linux depth on the estate's own platform is 10 of 25
SPEC-named functions.

Also: **`service` uses `systemctl` shell-out**, while SPEC 15.2 specifies
systemd over D-Bus with a hand-rolled wire client. No D-Bus code exists. Decide
whether the shell-out is the answer and amend, or write the client.

`useradd` is built (`internal/builtin/user.go:137`), so account management is
covered on Linux.

### 4.2 The 63 unimplemented platform modules

SPEC 15.3 names 65; `zfs` and `zpool` ship. Nothing is registered-as-refused
here — the other 63 simply do not exist, so a tree naming one gets an unknown
module rather than a reason. The Debian/Ubuntu row alone (`aptpkg`, `debconf`,
`dpkg`, `apt_key`, `ufw`, `netplan`, `apparmor`, `snap`, `pro`, `debbuild`) is
10 modules, and the estate is Ubuntu.

Worth doing first regardless of module work: **register the 63 as refused with a
reason**, the way beacons already do (7 built, 17 refused,
`internal/beacon/builtin.go:72-97`). That turns "unknown module" into "this
build does not ship `aptpkg` yet", which is the difference between a typo and a
gap. Cheap, and it makes the migration report honest.

### 4.3 Windows and macOS: zero modules each

- **macOS has no platform-specific code at all** — there is no `*_darwin.go`
  anywhere, and no `//go:build darwin`-only file outside a 16-line rlimit
  helper. It gets service management (launchd) incidentally and nothing else: no
  package provider, no user provider, no `mac_*` modules.
- **Windows has one 86-line grain stub**
  (`internal/grains/platform_windows.go`), whose own comment defers the real
  sources to a later phase. It hardcodes `os=Windows` and empty release
  strings. `file.chown`/`chgrp` and every state `user:`/`group:` attribute are
  dead (`internal/builtin/ownership_other.go:25,32`), `runas` refuses, and
  there is no package, service or user provider.

SPEC 27.1 puts Windows in **tier 1** and macOS in tier 2, and SPEC 15 says the
platform modules "ship in v1.0". Phase 5's exit criterion is a green tier 1
matrix. On the current inventory that is 18 Windows modules plus a package
provider, a service provider (SCM), a user provider and real grains — the
single largest remaining block of work in the project, and SPEC 33 question 9
asks whether it is required. **That question should be answered before the work
is scheduled, not after.**

### 4.4 Smaller phase 5 items

- The agentless **reverse tunnel** (SPEC 21.1). Trees go inline and anything
  over 4 MiB is refused by name (`cmd/halite-hub/ssh.go:244-279`).
- The `scan`, `cloud` and `terraform` **rosters**, each refused by name
  (`internal/roster/roster.go:104-106`).
- **`minionfs`/`nodefs`** (SPEC 13.2). Currently a *warning*, not a refusal —
  `fileserver_backend: [roots, minionfs]` starts the hub. Same for an
  `ext_pillar` that contributes nothing. Both should refuse or be implemented.

---

## 5. Phase 6: hardening to 1.0

Nothing in this section exists. Grouped by what each unblocks.

### 5.1 Nothing is measured (SPEC 30)

**There is not a single Go benchmark in the repo** — `grep "func Benchmark"`
returns zero. Two rows of SPEC 30's table name "Benchmark" as their own
measurement method (highstate compile under 2 s, pillar compile under 500 ms
cold / 5 ms cached), so those are cheap and can land immediately. The rest need
the simulated node harness: 20,000 nodes per hub, 10,000-node dispatch windows,
reactor throughput at 5,000 events/second, hub memory under 4 GiB, node memory
under 40 MiB idle. None of the twelve targets has ever been measured, so none
is known to be met or missed.

### 5.2 The observability trio (SPEC 26)

- **11 of SPEC 26.2's 32 metric families are unregistered:**
  `halite_state_run_duration_seconds`,
  `halite_state_compile_duration_seconds`, `halite_pillar_cache_hits_total`,
  `halite_pillar_ext_failures_total`, `halite_gitfs_fetch_duration_seconds`,
  `halite_gitfs_signature_failures_total`,
  `halite_event_subscriber_lag_seconds`, `halite_beacon_dropped_total`,
  `halite_ext_invocations_total`, `halite_ext_duration_seconds`,
  `halite_ext_timeouts_total`. Guarded in both directions by
  `TestLedgerMetricGapMatchesTheBuild`, so the count is trustworthy. Watch one
  trap: `halite_pillar_failures_total` **is** registered but is a different
  metric from the spec's `halite_pillar_ext_failures_total{source}`, so an
  alert written from the SPEC table silently matches nothing.
- **The node exposes no metrics at all.** `internal/metrics` is imported by
  `hub`, `api` and `relay` only; the node has no registry and no HTTP listener,
  and the `metrics` config key is scoped `hubAPI`. Everything node-local is
  therefore uncounted — beacon queue drops, local state run duration, scheduler
  `maxrunning` skips. Note SPEC 26.2 requires a counter on every bounded queue
  and every drop path, so this is a spec requirement, not a nice-to-have.
- **Tracing (26.3) and `doctor` (26.4) do not exist.** `doctor` has one
  passing mention in a comment. It is also the command SPEC 27.4 assigns the
  FIPS grain-mismatch warning to, so that warning has nowhere to live.

### 5.3 The security model's unbuilt half (SPEC 25)

Three items, none of which the ledger records:

- **The render sandbox (25.4) does not exist.** SPEC puts all YAML parsing and
  all template rendering in an unprivileged child process with no network,
  because "the parser and the template engine are the largest and most
  attacker-adjacent code in the system, and they need no privilege at all".
  Today both run in-process in the privileged parent. The only sandbox in the
  tree is the extension/bridge one. The Linux seccomp allowlist and capability
  drop that 25.4 also specifies are likewise absent.
- **Node-side evidence (25.7) does not exist.** No hash-chained append-only
  record of accepted jobs, and no `halite-node verify-evidence`. SPEC 27.3 even
  allocates it a directory. This is the control that gives an investigator a
  record a compromised hub cannot rewrite, so it is load-bearing for the
  "compromised hub" row of the 25.1 threat model.
- **Detached job signing (25.6) does not exist.** `require_job_signature` and
  `job_signer_keys` are declared and unread (see 2.2); the job wire type has no
  signature field. SPEC 33 question 6 asks whether this is required for
  production — the other control for the same threat is 25.7, so answer both
  together.

Also absent: **signed state trees**, named in the 25.1 threat model and in
phase 6's contents. Do not mistake gitfs ref verification (built, SPEC 13.3)
for it — that verifies a ref tip, not a tree manifest.

### 5.4 Testing layers that do not exist (SPEC 31)

- **The chaos suite is entirely absent** — `grep -i chaos` over all Go source
  returns zero. SPEC names eight scenarios, each of which must have "a defined,
  tested, documented behaviour": hub restart mid-job, network partition, disk
  full, clock skew, certificate expiry mid-run, extension hang, event bus at
  retention limit, reactor queue overflow. Several of these are the code paths
  most likely to be wrong, because they are the ones no test reaches.
- **The primary correctness gate skips silently.** All three `internal/saltdiff`
  tests require `salt-call`; on any host without it they skip, and
  `TestResultsMatchSalt` additionally needs `HALITE_SALTDIFF_RESULTS=1`. The
  skip message says "a skip here is a gap, not a pass" — but `make check` still
  passes. What it compares is also narrower than SPEC 31 asks: the low state,
  the pillar, and **test-mode predictions**, not applied state results. The
  container needed to apply state in has never been built.
- **Coverage is falling.** Whole tree 62.5%. Two of SPEC 31's four
  correctness-core packages are below the 90% bar even on the more forgiving
  statement metric: `internal/template` 82.6% and `internal/target` 89.2%
  (`internal/yaml` 96.6%, `internal/state` 90.9%). Branch coverage, which is
  what SPEC actually requires, is unmeasured and will be lower. The newer
  subsystems are much worse — `transport` 20.5%, `sshexec` 17.4%, `relay`
  31.8%, `pki` 34.6%, `fileserver` 41.8%, `builtin` 43.1%.
- **Upgrade testing** (hub at N with nodes at N−1 and N+1, cache format
  migration, certificate rotation across an upgrade) does not exist.
- **Integration testing** across the tier 1 matrix does not exist; there are no
  containers anywhere in the repo.

### 5.5 Packaging, release and CI (SPEC 4.3, 27.2)

Larger than the ledger's 6.2 suggests, which mentions only that `make release`
has never been run.

- **No artifact in SPEC 27.2 is built.** There is no nfpm config, no
  `Dockerfile`, no `.wxs`/MSI, no `.pkg` or launchd plist, no SBOM tooling and
  no provenance attestation. `make release` builds bare binaries into `bin/`.
  `contrib/` has systemd units and FreeBSD rc.d scripts, which is the whole of
  the packaging story today.
- **There is no CI at all** — no `.github/`, no GitLab config, no Jenkinsfile.
  SPEC 4.2 opens by saying the dependency policy "has teeth: CI enforces it",
  4.3 assigns CI the dependency assertion and the two-builder reproducibility
  check on every tag, and SPEC 31 lists CI layers throughout. The *checks*
  mostly exist as `make` targets and Go tests; nothing runs them automatically,
  which is why the three failures in item 2.1 are sitting in a clean tree.
- **Reproducibility is one builder, not two.** `make repro` builds twice on one
  machine from two paths — its own comment is honest about the difference. SPEC
  4.3 requires two independent builders to agree. Note the `toolchain` pin that
  the same control depends on is currently absent and unguarded (§2.1).
- Toolchain provenance (fetch by digest from an internal mirror) is not
  implemented.
- **`make check` is not yet CI-shaped.** Its first stage rewrites the tree
  instead of failing, and its serial chain means one red test masks four later
  stages (§2.1). Both need fixing before a pipeline is built on it, or CI will
  report a narrower result than it appears to.

Standing up CI is the highest-leverage item in this section: it is what keeps
everything else in this document from drifting again.

---

## 6. The conformance tail

Lowest priority, and the ledger is right about that. All three suites pass, and
each holds a table enforced in both directions, so nothing here is silently
rotting.

- **YAML (SPEC 10.1):** 402 cases, 331 agree, 34 deliberate per 10.1.2/10.1.3,
  **37 gaps**. The direction that matters: **20 of those gaps are documents
  halite accepts that the reference implementation refuses**, and all 20 are
  admitted defects rather than design choices — meaning a tree Salt would not
  load loads here. Also one `gapChomping` case that the suite itself calls "the
  most damaging gap in this table", because block-scalar chomping feeds
  `file.managed` contents. That one should be fixed regardless of its position
  in this list.
- **Templates (SPEC 10.2):** 198 cases, 157 agree, 26 outside the subset, **15
  gaps — but 9 are corpus-extractor artifacts**, cases depending on Python
  objects registered in Jinja's own test environment that no template engine
  could satisfy. **6 are real:** calling a filter result (`attr("items")()`),
  string `indent(width=…)`, `groupby` with a numeric attribute,
  `{{ self.foo() }}` block reuse, a `caller=none` macro default, and the
  `is in` test. One feature each. Plus `loopcontrols`, which the corpus misses
  entirely — see 3.1, where it belongs.
- **PyYAML differential:** 114 documents, 104 agree, 10 deviations, **zero
  unexplained**. Done.
- **The regex engine (SPEC 10.4):** `internal/regexcompat` is 169 lines and
  contains no engine — it refuses 11 PCRE constructs by name, each with a
  workaround string, and hands everything else to RE2. **The estate's real tree
  produced zero regex findings across 193 files**, which answers SPEC 33
  question 8: the backtracking engine stays in phase 6, and on this evidence
  could be dropped. One real defect worth fixing cheaply: detection is a raw
  substring scan, so a construct spelling inside a character class — `[(?=]` —
  is a false positive, and no test covers it.

---

## 7. Questions that need a person, not a commit

SPEC 33's nine were all answered by taking the spec's own default. Four now
have data or consequences that make deferring them expensive:

1. **Windows scope (33.9).** The largest remaining work block in the project
   (§4.3). Confirm the 18-module set is required before scheduling it, because
   phase 5 cannot exit without it as written.
2. **Detached job signing (33.6)** and node-side evidence. Both answer the
   compromised-hub threat; decide together, and decide before the API surface
   sets in hard (§5.3).
3. **The regex gap (33.8).** The data is in and says "not needed" (§6). Close
   the question.
4. **Strict undefined (33.4).** Cannot be answered yet, because the migration
   report does not emit undefined references (§3.6). Implement `CatUndefined`
   first, then decide.
5. **The `cmd.run` default (33.3).** 54 review findings say what it costs
   (§3.6). This is now a scheduling decision with numbers attached, and it
   should be taken before the estate starts rewriting states.

New questions this review raises:

6. **Modules SPEC never planned for** — `alternatives`, `docker_*`,
   `rabbitmq_*`, `kmod`, `macpackage`. Amend SPEC, bridge, or rewrite the tree
   (§3.6).
7. **`module.run` argument pass-through** (§3.6).
8. **systemd over D-Bus, or `systemctl` shell-out?** SPEC 15.2 says the former;
   the build does the latter (§4.1).
9. **Do reference bridges ship?** SPEC 20.3 promises in-tree `postgres` and
   `sqs` bridges as worked examples. The bridge protocol, sandbox, adapter and
   name lookup are all built, but **no destination extension exists**, so all
   16 bridged returners are reachable and unpopulated.

---

## 8. Suggested order

Sequenced by value per unit of work, and by what unblocks what.

**Now — days, mostly mechanical**

1. Fix the lexicon scan's exempt list and the two darwin tests — the only three
   failures left in `make check`. Restore the `toolchain` directive and assert
   it in `internal/buildpolicy`, and make `fmt` report rather than rewrite
   (§2.1).
2. Correct the nine ledger claims and extend the drift guards to Markdown prose
   and waiver reasons (§1).
3. Warn on the eleven inert config keys, and refuse `attested` by name
   (§2.2, §2.3).
4. Register the 63 absent platform modules as refused-with-a-reason (§4.2).

**Next — the project's purpose**

5. Stand up CI, running `make check` plus the conformance suites. Everything
   above stays fixed only if something enforces it (§5.5).
6. `{% break %}` / `{% continue %}`, then the `grains` state module — the two
   cheapest large reductions in the estate's 205 blocking findings (§3.1, §3.2).
7. Implement `CatUndefined` so the report can answer SPEC 33 question 4, then
   work the rest of §3.3–3.5.
8. Linux provider depth: the four optional `pkg` interfaces for apt, dnf and
   apk (§4.1).

**Then — phase 6 foundations**

9. The two SPEC 30 benchmarks that need no harness, and node-side metrics plus
   the 11 missing families (§5.1, §5.2).
10. Get the Salt differential to fail rather than skip, and build the container
    that lets it compare applied results (§5.4).
11. The chaos suite, starting with the paths no test reaches at all (§5.4).
12. Packaging and the two-builder reproducibility check (§5.5).

**Blocked on a decision**

13. Windows and macOS parity (§4.3, question 1).
14. Detached job signing, node evidence, the render sandbox, signed state trees
    (§5.3, question 2).

**Last**

15. The YAML over-acceptance set, prioritising the chomping case; the six real
    template gaps; the regexcompat character-class false positive (§6).
