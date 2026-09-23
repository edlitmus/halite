# Code review findings

## Most serious: a pillar targeting bypass

`internal/pillar/pillar.go:240` checks whether a top-file target is permitted by **scanning the literal expression text** for `G@`/`I@`/`J@`. Line 245 then compiles that same expression, and `internal/target/target.go:176` expands `N@name` into the nodegroup's own expression. The permit check never sees the expansion.

So SPEC 12.4's trusted-grain rule, and the pillar-cannot-target-on-pillar rule, are both enforced only against the spelling. Put the grain inside a nodegroup and neither fires. The agent demonstrated the consequence that actually hurts: because `targetNode()` exposes only trusted grains, an untrusted `G@` term can never match, so `not G@role:db` is true **everywhere** — and the excluded database hosts were handed the secret, with no diagnostic at all. Spelled directly, the same expression is refused.

The fix is to expand nodegroups before checking, not after. Two smaller defects sit in the same function: the audit record files a grain-matched delivery as `glob`, and `grainNamesIn` ends a name at `:` or space only, so `(G@fips_mode)` is read as the grain `fips_mode)` and a legitimate expression is refused.

## `--test` is not read-only

- **`check_cmd` runs on the node during a dry run.** `internal/runner/runner.go:496` gates it on `res.Succeeded()`, and `internal/states/states.go:76` makes that **true for a nil "would change" result**. `r.Ctx.Test` is consulted nowhere in `runner.go` outside line 308 — I checked. Worse than the write itself: the command validates a post-change state that test mode never produced, so every `visudo -c` / `nginx -t` / `sshd -t` state reports a spurious **failure** in a dry run.
- **`git.latest` fetches into the repo under `--test`**, despite declaring `TestReliable`. The comment at `internal/builtin/git.go:225` says a fetch "changes nothing in the working tree"; the agent snapshotted `.git` and found 7 files written, remote-tracking refs moved and a tag created. That comment is the exculpatory shape your CLAUDE.md warns about.
- **`grains.absent` with `destructive: true` never converges** — it oscillates, reporting a change on every highstate forever, because the converged-check at `grains_state.go:145` asks "does this node hold an entry?" and the apply path masks with null instead of deleting.

That last one explains why the first two survived: `internal/states/states.go:7` claims "every state module must pass" the conformance harness, and the harness has **6 real state functions out of 132 registered**. A `Probe` case on either would have failed immediately.

## Docs claiming more than was demonstrated

- `README.md:299`, `docs/getting-started.md:126` and `docs/migrating-from-salt.md:236` all say macOS and Windows "cross-compile and have not been run on either", naming `pkg` and `service` as unexercised — while `evidence.go` marks both `Hardware` and ten `mac_*`/`win_*` modules likewise. `README.md:90` contradicts `README.md:299` in the same file.
- **`yaml_bool_11` is documented as off and is on.** `docs/from-salt.md:519` tells operators to set `yaml_bool_11: true` to get 1.1 booleans; `internal/render/render.go:500` already defaults it true. I confirmed both lines. The advice is a no-op and the stated behaviour is inverted.
- **`job_queue_depth` is documented as 100 and the node uses 16.** `internal/config/keys.go:238` vs `cmd/halite-node/enroll.go:354`. Confirmed. Nothing compares a `config.Key.Default` to the literal the program actually falls back to, so the generator prints the wrong number faithfully.
- `sysrc` claims `Captured`, but its only test swaps in `RecordingRunner`, which never executes anything — so on the FreeBSD runner it runs beside a real `sysrc` and never calls it. `evidence.go:35` also says "what is still `Assumed` is nothing" while `sys.evidence` reports 20.
- `contrib/man/halite-node.8:46` documents a `state show` subcommand that doesn't exist; `docsaudit` checks code→page but never page→code.

Three of these are audit gaps rather than one-off slips — page→code, `Key.Default`→fallback literal, and the 253 "works" rows in `docs/command-reference.md` that nothing executes (two of which the CLI refuses).

I'll fold in the other two reviews when they land, then give you a single ranked list. Nothing has been changed — the tree is clean.

Third review is back. I verified its two headline claims on this machine.

## The audits that don't audit

**`internal/buildpolicy/policy_test.go:103` — the SPEC 4.2 dependency allowlist has never run, anywhere.** It shells `go list -m -f '{{.Path}}' all`, which cannot succeed in a vendored repo:

```
go: can't compute 'all' using the vendor directory
=== RUN   TestDependencyAllowlist
    policy_test.go:103: go list is unavailable in this environment: exit status 1
--- SKIP
```

That's not environmental, it's a property of the repo, so it skips on every machine including CI. `make policy` runs without `-v`, so the skip is invisible — and `policy` is the one CI leg that runs on a docs-only change. CLAUDE.md names this test as what enforces the dependency allowlist. Reading `vendor/modules.txt` works offline and always; an unreadable source should be `t.Fatal`, not `t.Skip`.

**`make release-gate` doesn't cover `cmd`.** The gate selects modules whose `Privileges` free text contains the substring `"root"`. `cmd` declares `Privileges: []string{"whatever the command needs"}` and has **no evidence row at all** — I confirmed both. So ten mutating functions including `cmd.run`, `cmd.script` and `cmd.exec_code` — arbitrary code execution — sit outside the gate that exists to catch exactly "nobody has considered this module". The agent proved the mechanism by editing only that prose string to add the word "root", at which point the gate fails and prints `cmd  no declaration at all`. 23 mutating modules are out of scope this way.

Three more of the same shape, each demonstrated by breaking the thing and watching the audit pass:

- **The build-integrity gate reads the Makefile comment it was copied from.** `strings.Contains` over the whole file, and lines 7–11 are a comment block listing the five flags. Strip `-trimpath`, `CGO_ENABLED=0` and `GOPROXY=off` from the actual recipe and it stays green.
- **The `ext.Confine()` audit is a text search** — the very defect you fixed in #80 reinstates cleanly by commenting the call out, because `// ext.Confine()` still contains the string.
- **SPEC 31's Upgrade row is covered by three comments.** Delete all 596 lines of upgrade test body, leave the marker comments, and `specaudit` passes while logging the covering files by name.

Two decay findings: `firewall` is `Hardware` on the strength of one hand-driven session and has **no `TestLive*` anywhere**, so the FreeBSD leg's broad `-run TestLive` selects nothing for it and `internal/builtin/firewall*.go` isn't in fleet.yml's trigger paths. And 22 live tests are selected by no leg's `-run` filter — including the two `TestLiveSnap*` tests written specifically because the `snap list` fixtures had been written from documentation.

The agent was explicit about its limits: it could not run the fleet legs, so finding 7 is read off `run.sh` and the filters rather than observed on a runner. It also recorded one hypothesis that turned out false.

Last review (maintainability) is still running; I'll give you the consolidated ranking when it lands. Tree is still clean.

All four reviews are in. Consolidated and ranked, with the top claim of each agent re-checked by me directly.

## Tier 1 — wrong behaviour on a real fleet

**1. Pillar nodegroups bypass the SPEC 12.4 trusted-grain rule, and deliver to the hosts they exclude.** `internal/pillar/pillar.go:240` checks the literal expression text; `:245` then expands `N@name` into the nodegroup's expression. Because only trusted grains are exposed to matching, an untrusted `G@` term can never match — so `not G@role:db` is true everywhere and the database hosts got the secret, silently. Spelled directly the same expression is refused. This is your security boundary failing open.

**2. `--test` is not read-only, three ways.** `check_cmd` runs on the node during a dry run (`runner.go:496` gates on `Succeeded()`, which `states.go:76` makes true for a nil "would change"; `Ctx.Test` appears nowhere else in that file — I checked) and reports a spurious *failure*, because it validates a post-change state test mode never produced. `git.latest` writes 7 files into `.git` under `--test` while declaring `TestReliable`. `grains.absent --destructive` never converges — it oscillates forever, so a highstate reports a change on every run.

**3. `timeout: "900"` on `cmd.run` means no timeout at all.** `internal/builtin/cmd.go:410` uses bare `time.ParseDuration`, `:173` discards the error, and `exec.go:724` arms the deadline only when `Timeout > 0`. The same word in the same SLS file parses differently at `state/lowstate.go:483`, which accepts `"900"` *and* diagnoses failure. Seven independently-written duration parsers, drifted. I confirmed both sides.

**4. `halite-node doctor` prints through no redactor; the hub's identical path scrubs.** `cmd/halite-node/doctor.go:51,54` vs `cmd/halite-hub/doctor.go:75`. Confirmed. DIVERGENCE 5.110 fixed exactly this — on the hub — and the same entry notes the node compiles its *whole* tree, so it reaches more GPG blocks. The node has no doctor test.

## Tier 2 — the checks that stopped checking

These matter more than their individual blast radius, because each one is why something above survived.

- **The SPEC 4.2 dependency allowlist has never run, anywhere.** `go list -m all` cannot succeed in a vendored repo, so `policy_test.go:103` skips unconditionally — I reproduced it. `make policy` runs without `-v`, and it's the one leg that runs on a docs-only change.
- **`make release-gate` doesn't cover `cmd`.** It selects on the substring `"root"` in free-text `Privileges`; `cmd` says `"whatever the command needs"` and has no evidence row at all — both confirmed. Ten mutating functions, including `cmd.exec_code`, outside the gate.
- **The build-integrity gate reads the Makefile comment it was copied from** — strip the real flags, it stays green.
- **The `ext.Confine()` audit is a text search** — your #80 fix reinstates by commenting the call out.
- **SPEC 31's Upgrade row is three comments** — delete 596 lines of test body, `specaudit` passes and names the covering files.
- **`internal/states/states.go:7` claims every state module passes the conformance harness; it covers 6 functions of 132.** A `Probe` would have caught both #2 defects immediately.
- **`firewall` is `Hardware` with no `TestLive*` anywhere**, and 22 live tests match no leg's `-run` filter — including the two `TestLiveSnap*` written because those fixtures had been written from documentation.

## Tier 3 — prose that outruns what was demonstrated

`yaml_bool_11` documented as off and defaulting to on (the documented fix is a no-op); `job_queue_depth` documented 100, code 16; three files saying macOS/Windows have never been run while `evidence.go` marks ten of their modules `Hardware`; `sysrc` claiming `Captured` on a test that swaps in `RecordingRunner` and never calls it; a `state show` subcommand in the man page that doesn't exist; two `docs/command-reference.md` rows marked "works" that the CLI refuses.

## Tier 4 — maintainability

The render sandbox leaks 4 fds per attempt on its documented failure path (200 held after 50 renders, finalizer-bounded); `keystore` re-implements `atomicfile` and loses the directory fsync and the Windows ACL; `ext.Kinds` and `internal/extension.Kinds` are two lists with a tautological test binding them; 49 symbols with no reference on any GOOS, four of which are unwired halves — including `DeleteToken`, so every enrolment token ever minted stays on disk.

---

Three things worth saying plainly. **Nothing is changed** — the tree is clean. **The recurring shape is a text search standing in for a check**: findings in tiers 2 and 4 are almost all `strings.Contains` where an AST parse or a value comparison belongs, which is the DIVERGENCE 5.82 shape generalised. And **what wasn't verified**: the fleet-leg analysis is read off `run.sh` and the `-run` filters, not observed on a runner; the Windows ACL consequence in keystore is read from source; and the error/leak sweep found one demonstrated leak rather than being exhaustive.
