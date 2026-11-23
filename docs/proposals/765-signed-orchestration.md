# Proposal: orchestration on nodes that require signed jobs (issue 765)

**Status: a design, not built.** First written 2026-11-22 against `main` at
`d0d5925c`, recommending pre-signed plans (option B). **Revised the same day,
against `main` at `33dc8e21`, to option A'**: a plan-checking signing service
off the hub, which the maintainer chose (section 0). Every statement about
what the code does today cites the file and line it was read at, at
`33dc8e21`. Nothing here has been run; section 12 lists what was not
verified.

Nothing holds this file to the code: `internal/docsaudit` reads only
`docs/*.md`, `README.md` and `CHANGELOG.md`
(`internal/docsaudit/docsaudit_test.go:155-165`, `:200-212`). When part of it
is built, that part's ledger entry is the record.

**Confidence: 3/5.** The node side is unchanged: every job the service signs
is an ordinary SPEC 25.6 signed job, verified by code that exists and has been
demonstrated (DIVERGENCE 5.128, 5.276). What keeps this at 3 is that
everything else is new and none of it has run: a network service that has to
be available for the length of every signed orchestration, its durable state,
and the hub's compile agreeing with the operator's for real orchestrations.
Section 2.10 also records a hub behaviour, read and not run, that a long
signed step meets: the hub marks a job `partial` once its expiry passes, even
while a node is still running it.

## 0. What was decided

The maintainer's answers to the first version's questions, recorded on PR
801:

| # | Question | Answer |
|---|---|---|
| 1 | B now, A' later, or A' only? | **A' directly.** Orchestrations here can run for hours (database initialisation, data migrations), and B cannot cover that without raising `job_signature_max_ttl` for every signed job on those nodes |
| 2 | Is an hour enough? | Answered by 1: the signature's lifetime must not bound the orchestration |
| 3 | Sequence | **A plan authorises a set, not an order.** Documented (section 1). Where order matters, the dependent actions become one step |
| 4 | `parallel` in the signature | **No.** `parallel` and `run_parallel` stay outside the signature and are ignored on signed jobs. No v3 encoding |
| 5 | Hub configuration in templates | **A planned orchestration may not read hub configuration** |
| 6 | The interrupted-run record | **Fixed first, in its own change: issue 802.** This proposal assumes it and does not design it |
| 7 | Bind to issue 764's tree | **Yes:** a plan records the manifest digest it was compiled from |
| 8 | Priority against 764 | Moot: 764 is built (PR 809, open when this was written) |
| 9 | 5.169's live-signer sentence | **Corrected with a ledger subsection:** DIVERGENCE 5.169.1, in the same branch as this revision |

## 1. The problem, and who the attacker is

A node with `require_job_signature` refuses a job of a class it requires a
signature for unless the job carries a valid signature from a key in its
`job_signer_keys` (`cmd/halite-node/jobsignature.go:199-226`). An
orchestration's steps are jobs the hub dispatches itself, unsigned. So on such
a node a `salt.state` step, or a `salt.function` step naming an
`arbitrary_code` or `state` function, is refused, and the hardening chapter
says so (`docs/handbook/28-hardening.md:109-112`).

| Attacker | Controls | What this design must still guarantee |
|---|---|---|
| **A1, a compromised hub** | It compiles the orchestration, chooses every step's target and arguments, dispatches, holds its own CA key (`internal/pki/store.go:43`, read by `LoadCA` at `:221-229`, which `cmd/halite-hub/serve.go:135` calls), and can call anything it has credentials for, the signing service included | A step of a class a node requires a signature for runs on that node only if an operator signed a plan containing that step, with that target, function, arguments, keyword arguments and environment; the plan is unexpired and uncancelled; and the service has not already spent that step's signing budget. A1 cannot add a step, alter one, widen its target, extend a signed job's life, or replay one |
| **A2, someone who can push to the orchestration's tree** | The orchestration SLS the hub compiles | The same. A changed orchestration compiles to different steps, which no plan contains |
| **A3, an insider with API access and no plan key** | Can ask the hub to run any orchestration their policy allows | The same. Policy decides what the hub will try, a plan decides what the service will sign, and the signature decides what a node will run |
| **A4, a compromised signing service** | The job key the nodes trust for orchestration, or the right to have KMS sign with it, and the service's state | Not fleet compromise on its own: the service has no path to a node, and a job it signs reaches one only through the hub. **A4 together with A1 is fleet compromise** for the nodes that trust the service's key. Section 4 compares this with a stolen operator key |
| **A5, someone holding a TLS client certificate for the service** | Can call the service's API | Nothing the TLS layer authenticates widens what may be signed; only a plan signature does (3.6). A5 can cancel plans and spend signing budgets, which is denial of service and nothing more |

**What this does not deliver:**

- **Sequence.** A plan authorises a set of jobs, not an order. A1 can ask for
  the steps' signatures in another order, skip a step (the drain before a
  deploy), ask for a rollback step when nothing failed, or hold everything
  back. Each job it delivers is still exactly a planned step, to nodes its
  target matches, within the expiry the service gave it. A node sees one job
  at a time and cannot check an order. **Where order matters, the dependent
  actions belong in one step**: a single `state.apply` of an SLS whose own
  requisites drain, deploy and undrain runs in the node's state runner, in
  the node's order, and that order is not the hub's to change. The service
  could refuse to sign a step before the steps it requires have been signed,
  and this design deliberately does not: that binds the order of signatures,
  not of execution, because A1 can obtain the drain's signature and never
  deliver it. A check that reads as an ordering guarantee and is not one is
  worse than none.
- **Hub-side steps.** `salt.runner`, `salt.wheel` and `salt.wait_for_event`
  run on the hub (`internal/hub/orch_steps.go:326-365`, `:368-418`). A1 runs
  them regardless, and nothing a node or the service does can constrain them.
- **Batch, subset, safe limit and `tolerate_failures`, in version one.** None
  is in the signed payload (`internal/job/model.go:398-409`); the hub applies
  them (`internal/hub/dispatch.go:101-106`, `:120-125`). Under A1 they are
  controls an honest hub honours. Section 3.15 describes a phase two that
  turns `subset` and the batch size into bounds the service enforces.
- **Parallelism**, which stays outside the signature (3.16).

## 2. What happens today

### 2.1 A step is a job the hub dispatches unsigned

- The step modules are built per run (`orchStates`,
  `internal/hub/orch_steps.go:23-205`). A fleet step's `Submission` is built
  by `submission` (`orch_steps.go:261-287`) with target, target kind,
  function, environment, `test`, batch spec, subset and safe limit. It has no
  job id, expiry or signature. `applyState` adds the SLS names, `test` from
  the run, and the `pillar` and `pillarenv` keyword arguments
  (`orch_steps.go:208-232`). `callFunction` adds positional and keyword
  arguments and `run_parallel` (`orch_steps.go:235-257`, the flag at `:249`).
- `dispatchAndWait` sends it through `dispatchFor` and waits for the returns
  (`orch_steps.go:297-323`, the call at `:303`). `dispatchFor` authorises the
  step against policy as the run's principal, and the operator it acts for if
  any (`internal/hub/runners_fleet.go:441`).
- The submission sets no `TTL`, so the job gets `job.DefaultTTL`, fifteen
  minutes (`dispatch.go:132-136`; `internal/job/job.go:24`).
- An unsigned job's id and expiry come from the hub's clock (`identify`,
  `dispatch.go:570-577`).

### 2.2 The hub can already carry a signed job it did not sign

- `Submission` has `JID`, `Expires` and `Signature`, "set together or not at
  all". On a signed submission the hub checks them and does not choose them
  (`dispatch.go:47-58`).
- `identify` refuses a signed submission whose id is invalid, whose expiry is
  missing or past, or whose id this hub already has a job for
  (`dispatch.go:578-597`). Signed submissions hold a lock from that check to
  the record (`dispatch.go:154-161`, DIVERGENCE 5.531).
- The record keeps the signature, so that a queued job or a resumed batch is
  delivered from the record still signed (`internal/job/model.go:190-203`).

### 2.3 What a node checks, and when

`Offer` (`cmd/halite-node/execute.go:482`) runs the replay guard, which
includes the expiry (`internal/job/guard.go:117-123`); refuses a signed id
this node has already accepted (`execute.go:494`); and then calls
`checkJobSignature` (`execute.go:500`; `jobsignature.go:199-226`):

1. Whether this function needs a signature at all (`jobsignature.go:129-159`).
   A job that needs none is accepted whatever it carries (`:200-203`).
2. The signature verifies against a key in `job_signer_keys`
   (`internal/jobsign/jobsign.go:483`) over `job.SigningPayload`
   (`internal/job/model.go:398-409`), in the `halite-job-v2` domain
   (`jobsign.go:81`).
3. The expiry lies no further ahead, **on arrival**, than
   `job_signature_max_ttl`, an hour by default, with a minute of skew
   (`jobsignature.go:237`, `:258-271`).
4. This node matches the signed target (`jobsignature.go:290-329`). A
   nodegroup target is refused (`:301-310`).

The signed id is then written to disk before the job can run
(`execute.go:510`), and the acceptance record names the key that verified it
(`execute.go:549`).

**Nothing on the node checks the expiry again once the job is accepted.** A
search of the node's command package for `Expires` finds the parse in
`acceptJob` (`cmd/halite-node/enroll.go:1035-1042`), the evidence record
(`evidence.go:336-337`) and the bound above, and the only expiry check on the
node's path is the guard's. So a job accepted before its expiry runs to
completion however long it takes. This is what lets A' sign long steps with a
short expiry (3.9). It is a reading, not a run (section 12).

### 2.4 The orchestration is compiled on the hub, from the hub's tree

`compileOrchestration` (`internal/hub/orch.go:186-225`) compiles from
`s.Files` with the run's pillar override, no grains, **the hub's
configuration values** (`:203`), **the hub's nodegroups** (`:209`), no `salt`
dispatcher (`:207`) and strict undefined names (`:208`), and then refuses
`run_parallel: true` on a state run (`:223`, `:243-275`). `orch lint`
(`state.orchestrate_show_sls`) is the same compile on the hub
(`internal/hub/runners_orch.go:38-79`). There is no off-hub compile today.

### 2.5 Resume, a hub restart, and issue 802

- A resume is a new run with a new id from the hub's clock (`orch.go:99`),
  compiled again. It is refused if the pillar override differs from the one
  the run started with (`orch.go:348-362`) and, for a real resume, if the
  earlier run was a test run (`orch.go:372-392`). Steps before `--from` that
  the earlier run recorded are carried forward; a step it never reached runs
  now (`orch.go:394-410`).
- The run's record is written at the start, with no steps, and at the end
  (`orch.go:145`, `:177`; `run.Steps` is assigned at `:160`). A hub that stops
  mid-run leaves a record with no steps, so a resume may re-run steps that
  ran. That is **issue 802**: suspected, not reproduced, and being fixed
  separately. This proposal assumes 802's fix gives a record written as each
  step finishes, naming the job id of the step in flight (`OrchStep` already
  has a `JID` field, `internal/hub/orchstore.go:89`). Section 3.11 says what
  changes if it records less.
- A step's own job survives a hub restart as a record, still signed
  (`model.go:190-203`).

### 2.6 Test mode

In a test run, state steps are dispatched with `test` set
(`orch_steps.go:221`), and `test` becomes a keyword argument on the wire
(`job.WireKwargs`, `model.go:377-389`), so it is inside the signature.
Function steps, runner steps and waits are not dispatched in a test run
(`orch_steps.go:298-301`, `:331-333`, `:379-381`). A signed dry run cannot be
turned into a real one (DIVERGENCE 5.330).

### 2.7 `parallel` and `run_parallel` today

- `run_parallel` exists since PR 805: a `salt.function` step sets
  `Submission.Parallel` from it (`orch_steps.go:249`), and `true` on a state
  run is refused at compile time (`orch.go:243-275`). It is SPEC 9.6's
  `--parallel` for the step's job, and is kept on the record
  (`model.go:160-165`).
- It is **not** in the signed payload (`model.go:398-409`). On any signed job
  a node ignores it and runs the job alone unless its own `parallel_jobs` says
  otherwise (`cmd/halite-node/jobqueue.go:29-41`, the check at `:37`, the rule
  stated at `:20-24`). A state run is always alone (`:31-33`).
- `salt.parallel` is refused when it runs (`orch_steps.go:187-202`).

### 2.8 What the record already says

- DIVERGENCE 5.169 (`docs/DIVERGENCE.md:16582-16642`) explains why this is
  not a wiring gap, names "a live signer" and "a pre-approved plan", and
  rejects a hub-held key (`:16630-16637`).
- `plan.md:2253-2258` keeps the choice open, and `plan.md:2686-2697`
  sketches the operator surface as `halite-hub orch run …
  --sign-extension ./halite-ext-signer-kms`: the hub calling a bare signer per
  step. Section 2.9 is why that sketch is option A, rejected.

### 2.9 Why a bare signer called by the hub is not enough

5.169's live-signer bullet (`docs/DIVERGENCE.md:16612-16620`) has the hub call
"a bridged `signer` extension backed by a KMS" per step, so that "the KMS
signs bytes it is handed rather than a private key the hub holds". The signer
protocol hands over a 32-byte digest and gets back a signature (DIVERGENCE
5.170), and `halite-ext-signer-kms` refuses only a digest that is not 32 bytes
(`cmd/halite-ext-signer-kms/main.go:143-167`). A signer that sees only a
digest cannot know what it is the digest of, so a hub that can call it can get
**any** payload signed. Moving the key into a KMS takes the key off the hub's
disk and leaves the hub able to sign, which is what SPEC 25.6 exists to deny
(`SPEC.md:2582`). DIVERGENCE 5.169.1 records the correction.

### 2.10 A step longer than its job's expiry is marked `partial` while it runs

Read, not run. `Settle` closes jobs past their expiry on the premise that "a
job past its expiry is finished by definition: the nodes it has not heard from
would refuse it now" (`internal/hub/batch.go:274-281`). That is true of a
node the job never reached and false of one that accepted it in time and is
still running it (2.3). `settleExpired` sets such a job `partial` and counts it
in `halite_jobs_expired_total` (`batch.go:334-374`, the state at `:347`;
`internal/hub/metrics.go:118`). The late return is still filed:
`Cache.File` does not read the job's state (`internal/job/returnindex.go:116`
onwards), and `completeIfDone` then moves the job from `partial` to
`complete`, refusing only `complete` and `aborted`
(`internal/hub/jobs.go:206-229`, the guard at `:221`). `awaitReturns` waits
until nothing is missing or the step's own timeout passes
(`orch.go:491-499`).

So, by that reading, a long step finishes correctly in the orchestration,
while `jobs list` calls its job `partial` for a while and the expiry counter
counts a job that did not expire. **This is true today, unsigned**: an
orchestration step's job lives fifteen minutes (2.1), so a two-hour migration
step already meets it. It is its own defect and wants its own issue, which
this change does not open (question 11.8). Under A' it matters more, because
a signed step's expiry is short by design.

Separately, and also true today, a step waits five minutes unless it sets
`timeout` (`stepTimeout`, `orch_steps.go:445-453`). A long step has to say how
long it may take, signed or not.

### 2.11 What a `signer` extension is, and why the service is not one

A `signer` extension is a subprocess spoken to over the extension protocol on
its standard input and output. `halite-hub run --sign-extension` starts one
per submission, with the operator's environment
(`cmd/halite-hub/run.go:580-617`), and it signs a digest it cannot interpret
(2.9). It has no listener, no state and no view of the payload. So the service
cannot be "a mode of `halite-ext-signer-kms`": the one thing the service must
do, read the job and compare it with a plan, is what the extension protocol
was designed not to need. The service **uses** a signer extension as its key,
through the same bridge `run` uses (`internal/extsigner`; `run.go:584-617`),
so the KMS signer already demonstrated (DIVERGENCE 5.276) is the key behind
it, unchanged.

### 2.12 The estate

A search of the estate's state tree (2026-11-22) for `salt.state`,
`salt.function` and `salt.runner` found no orchestration. The maintainer
expects long ones, which is the reason for A'.

## 3. The design: a plan-checking signing service (A')

### 3.1 In one paragraph

The operator compiles the orchestration **off the hub**, from their own
checkout, with the compiler the hub uses, into a **plan**: every fleet step's
target, function, arguments, keyword arguments and environment, and nothing
the node does not check. They sign the plan with a **plan key** and hand it to
the **signing service**, a long-running process on a machine that is not the
hub. `orch run --plan <id>` names the plan to the hub. The hub compiles as it
does today, and at each fleet step asks the service to sign the job it is
about to send. The service signs it **only if** it is a step of an approved,
unexpired, uncancelled plan, with exactly the plan's fields, within that
step's signing budget, and it mints the job id and a short expiry itself. The
node verifies the result as it verifies any signed job today. **The node does
not know plans or the service exist.**

### 3.2 The parts, and the keys

| Part | Where it runs | Holds | New? |
|---|---|---|---|
| Plan tool, `halite-hub orch plan` | The operator's machine | The checkout; access to the plan key | New subcommand |
| Plan key | The operator: a KMS key behind `halite-ext-signer-kms`, or a local key | Signs plans, domain `halite-orch-plan-v1` | New key |
| Signing service, `halite-hub signer serve` (proposed; question 11.4) | A host the hub's compromise does not reach (3.5) | The orchestration job key, or the right to have KMS sign with it; `plan_signer_keys`; its state | New |
| Orchestration job key | Behind the service: KMS, or a local key in a lab | Signs step jobs, domain `halite-job-v2`, unchanged | New key; one new line in each node's `job_signer_keys` |
| Hub | As today | A client certificate for the service, and a plan id per run | Changed |
| Node | As today | Nothing new | **Code unchanged** |

### 3.3 The plan

Proposed schema `halite.orch-plan/1`, reusing the first version's where it
fits. JSON, written by the plan tool and read by the service and the hub:

```json
{
  "schema": "halite.orch-plan/1",
  "sls": ["deploy.rolling"],
  "env": "base",
  "test": false,
  "pillar_digest": "sha256:4b2f…",
  "tree": {"env": "base", "manifest": "sha256:9c1e…"},
  "created": "2026-11-22T18:10:00Z",
  "not_after": "2026-11-23T18:10:00Z",
  "job_ttl": "15m",
  "steps": [
    {
      "id": "migrate_db",
      "step": "salt.function",
      "job": {"target": "db1", "target_kind": "glob", "fun": "cmd.run",
              "arg": ["/usr/local/libexec/app/migrate --to 42"], "env": "base"},
      "retries": 0
    },
    {
      "id": "apply_web",
      "step": "salt.state",
      "job": {"target": "web*", "target_kind": "glob", "fun": "state.apply",
              "arg": ["web"], "kwarg": {"pillar": {"version": "1.4.2"}}, "env": "base"},
      "retries": 1,
      "job_ttl": "45m"
    },
    {
      "id": "check_web",
      "step": "salt.function",
      "job": {"target": "web*", "target_kind": "glob", "fun": "test.ping", "env": "base"},
      "sign": false
    }
  ],
  "hub_steps": [
    {"id": "announce_start", "step": "salt.runner", "name": "event.send"}
  ]
}
```

- **`steps`** holds one entry per fleet step (`salt.state`, `salt.sls`,
  `salt.highstate`, `salt.function`) the compile produced, keyed by state ID.
  `job` is `job.SigningPayload`'s field set (`model.go:398-409`) without
  `jid` and `expires`, which the service mints. `kwarg` is as it will be on
  the wire: put through `jobsign.WireValues` (`jobsign.go:536`), as
  `prepareSigning` does (`cmd/halite-hub/run.go:520-553`), with `test`
  folded in as `job.WireKwargs` folds it (`model.go:377-389`). So the plan
  cannot carry something the node does not check, and cannot leave out
  something it does.
- **`retries`** is how many attempts beyond the first the service may sign
  (3.13). Zero by default.
- **`job_ttl`** is the expiry the service gives each job it signs, per step or
  for the plan; the service caps it (3.9).
- **`sign: false`** marks a step the hub dispatches unsigned, as today (3.14).
- **`not_after`** bounds the plan: no step is signed after it. Its default is
  question 11.3.
- **`pillar_digest`** is the digest a run records for its resume check
  (`orch.go:110`, `:610`), for the honest hub's pre-flight. The override
  itself is in each step's keyword arguments, which is what the service
  compares.
- **`tree`** binds the plan to issue 764's signed tree (3.4).
- **`hub_steps`** lists the runner, wheel and wait steps, for the operator to
  read and for the honest hub's pre-flight. Nothing can enforce them
  (section 1).
- **No `run_parallel`, no batch, no subset** in a signed step's `job`: they are
  not in the signature (decision 4, 3.15, 3.16). The plan tool records them
  beside `job` for `plan show`, marked as unsigned.

**The plan's signature** is a file beside it, `<plan>.sig`: one line, base64
of an ASN.1 DER ECDSA signature over

```
digest = SHA-256( "halite-orch-plan-v1\x00" || <the exact bytes of the plan file> )
```

over the bytes rather than a canonical re-encoding, for the reason issue 764's
manifest gives (`docs/proposals/764-signed-state-content.md`, its 3.3): the
service verifies the bytes it received and only then parses them. The digest
is 32 bytes, so `--sign-key` and `--sign-extension` sign a plan unchanged
(2.11). **The plan's id** is `sha256:` and the hex SHA-256 of its bytes, the
shape PR 809 gives a manifest's id (`treesign.ID`,
`internal/treesign/treesign.go:153-158` on that branch at `5e57f684`, not on
`main`).

### 3.4 Compiling the plan, with one path for both ends

- **Same compiler, same inputs.** The plan tool runs `state.Compiler` with
  what `compileOrchestration` gives it (`orch.go:186-225`), with the
  operator's checkout as the loader, and two differences that **the hub makes
  too when it runs a planned orchestration**, so that both ends compile from
  identical inputs:
  - **No configuration values** (decision 5). `ConfigValues` is empty at both
    ends instead of the hub's (`orch.go:203`), so a template that reads hub
    configuration fails under strict undefined names, at plan time, naming
    the value.
  - **No nodegroups** (`orch.go:209`). A signed nodegroup target is refused by
    every node (2.3), so the plan tool refuses one at plan time, with the
    node's reason.
- **The step-to-job translation must be one function.** Today it is spread
  through `submission`, `applyState` and `callFunction`
  (`orch_steps.go:208-287`), interleaved with dispatch. Building this means
  extracting a pure `stepJob(chunk, run) (Submission, error)` that the hub's
  step modules, the hub's pre-flight (3.10) and the plan tool all call. Two
  copies of "what job does this step make" would be two things that must
  agree. This design fails closed when they do not, which is safe and loud,
  and would make it unusable.
- **Binding to the signed tree** (decision 7). When the checkout carries
  issue 764's `_halite/manifest.json` and its signature, the plan tool
  verifies the manifest against the state signer keys it is given
  (`--state-signer-keys`, the nodes' list), checks that every file the
  compile read is in the manifest with the digest it has, and records the
  manifest's id in `tree.manifest`. It refuses a checkout whose manifest does
  not verify or does not cover what was compiled. A checkout with no manifest
  gives a plan with no `tree`, and `plan show` says so. What this buys,
  stated exactly:
  - **A record**, kept by the service with the plan and in its audit log, of
    which signed revision the steps were compiled from, checkable afterwards.
  - **Not a control by itself.** A node applies the newest revision it
    accepts (764 proposal, its 3.6), which can be newer than the planned one.
    It becomes a control with 764's proposed `tree_revision` pin (764
    proposal, its 3.9): the plan tool would add the pin to each state step's
    keyword arguments, inside the signature, and a node would refuse any other
    revision. PR 809 does not build the pin: a search of its Go for
    `tree_revision` finds nothing.

### 3.5 The signing service

**What it is.** A long-running HTTPS service, proposed as `halite-hub signer
serve` (question 11.4 asks whether it should be its own binary). It does not
compile, render, resolve targets or talk to nodes. It verifies plans, compares
jobs with them, keeps budgets, signs, and writes down what it did. It is small
on purpose: everything it does is a comparison or a record.

**Where it runs.** On a machine the hub's compromise does not reach: no hub
credentials or certificates, no shared root, no shared administrator account,
and **not a jail or container on the hub's host**, whose root reaches it.
Its load is a few signatures per orchestration. On AWS, an instance whose
role may call `kms:Sign` on the orchestration key and nothing else. Which
machine is question 11.1.

**What it holds.**

- The orchestration job key. Preferably as a `signer_extension` path, run
  through `internal/extsigner` and `internal/bridge` exactly as `run
  --sign-extension` runs it (`run.go:584-617`), so that
  `halite-ext-signer-kms` with its own key, proposed `alias/halite-orch`, is
  the key, and the service's host holds only the right to ask KMS. A local key
  file is accepted for a lab, and the service warns at start that it is one.
- `plan_signer_keys`: the plan keys it accepts, in `job_signer_keys`' line
  format (`jobsign.ParseSignerKey`, `jobsign.go:331`). The service **refuses
  to start** if its own job key's public half is in this list, so that it can
  never approve its own plans.
- Its TLS identity and the client names it accepts (3.6).
- Its state, in a state directory of its own.

**What state it keeps, durable across its own restarts.** Every file is
written with `internal/atomicfile` and synced before the request that caused
it is answered.

| Record | Holds |
|---|---|
| `plans/<id>/plan.json`, `plan.sig` | The plan exactly as submitted, and its signature |
| `plans/<id>/accepted.json` | When, from which client name, under which plan key |
| `plans/<id>/cancelled.json` | When, by whom, why. Its presence is the cancellation |
| `plans/<id>/steps/<step>.jsonl` | One line per attempt signed: attempt number, jid, expiry, payload digest (`jobsign.Digest`, `jobsign.go:441`), the signature, the requesting client, the time |
| `audit.jsonl` | Every request and its outcome, signed or refused, with the reason (3.17) |

**The write comes before the answer.** An attempt is signed, then written and
synced, and only then returned. A crash before the write loses a signature
nobody received. A crash after the write and before the answer leaves an
attempt the hub never got, and the hub's repeated request returns that same
attempt (3.8, rule 6), so nothing is lost and no budget is spent twice. The
service never hands out a signature it has not recorded, which is what makes
its audit log the authority on what it signed.

### 3.6 How the hub reaches it, and who authenticates whom

HTTPS with TLS 1.3, JSON bodies, mutual TLS. Proposed endpoints:

| Endpoint | Who may call | Does |
|---|---|---|
| `POST /v1/plans` | operators | Submit a plan and its signature; answers with the id and the steps accepted |
| `GET /v1/plans/<id>` | hub, operators | The plan, its state, and every attempt signed |
| `POST /v1/plans/<id>/check` | hub | Compare a full list of compiled steps with the plan; answers with every mismatch; signs nothing and spends nothing |
| `POST /v1/plans/<id>/steps/<step>/sign` | hub | Rule 3.8; answers with `jid`, `expires` and `signature`, or a refusal naming its reason |
| `POST /v1/plans/<id>/cancel` | operators, hub | Cancel. Allowed to the hub because cancelling only removes authority |

**The governing rule: nothing the TLS layer authenticates widens what may be
signed.** Authority comes from a plan signature, which the service verifies
itself, whatever channel the plan arrived on. A client certificate decides
only who may spend a plan's budgets, read plans, and cancel them. So a wrong
decision about TLS identity is denial of service or disclosure of a plan's
arguments, never an unplanned job.

**Not the hub's CA.** The hub holds its CA's private key
(`internal/pki/store.go:43`, read by `LoadCA` at `:221-229`, which
`cmd/halite-hub/serve.go:135` calls), so under A1 it can mint any
certificate under that CA, and every enrolled node already holds one. Two
consequences, of different weight:

- If the service accepted clients from the hub's CA, any node's certificate
  would be a client unless names were pinned, and A1 could mint an operator's
  name. By the governing rule that is denial of service. There is still no
  reason to allow it.
- **If operators verified the service's server certificate against the hub's
  CA, A1 could impersonate the service to operators**, given a network
  position or control of name resolution: swallow a cancellation and answer
  "cancelled", or show a plan's state falsely. An operator who believes a
  plan is cancelled when it is live is the cost that matters, and it is why
  the service's certificate must chain to a CA the hub does not hold.

So the service has its own **signer CA**: a small CA whose key the operator
keeps off the hub (offline, or in the operator's own keystore), issuing the
service's server certificate, one client certificate for the hub, and one per
operator. The service names its clients and their roles in its configuration,
matched against the verified leaf's CN or a DNS or URI SAN, the shape
`hook_client_ca` and an `mtls` hook's `client` already use for webhook
senders (`internal/config/keys.go:225`; PR 793). A single self-signed
certificate pinned by hash is the alternative for an estate with one hub, and
gives up nothing the CA gives except rotation.

The hub gains proposed settings `orch_signer_url`, `orch_signer_ca_file` (the
signer CA), `orch_signer_cert_file` and `orch_signer_key_file`. It verifies
the service against `orch_signer_ca_file` only: never the system roots, and
never its own CA.

**The service never calls the hub.** It answers requests. The hub
authenticates the service by its server certificate, and the service
authenticates the hub by its client certificate, which under A1 is A1. That is
correct: the hub is meant to be able to ask, and what it can get is bounded
by plans.

### 3.7 Handing a plan over, cancelling it, revoking it

- **Submission is direct, operator to service** (`halite-hub signer
  submit`), never through the hub. The channel is not what grants authority,
  the signature is; but a direct channel means the acknowledgement the
  operator reads, the plan id and the steps accepted, comes from the service
  and not from something A1 relays. The operator then gives the hub only the
  id (`orch run --plan <id>`), and the hub fetches the plan from the service
  for its pre-flight.
- **Cancel** (`halite-hub signer cancel <id>`): durable, immediate for new
  signatures, and irreversible; a cancelled plan is never signed for again.
  **Jobs already signed stay valid until their own expiry**, at most the
  service's cap (3.9), because a node cannot hear about a cancellation. For an
  immediate stop on a machine, the node's own quiesce is the lever, and a hub
  cannot lift a local quiesce (PR 806). `signer cancel --local`, run on the
  service's host, writes the cancellation into the state directory directly,
  for when the network path is the problem.
- **Expiry:** after `not_after`, nothing in the plan is signed.
- **Revoking a plan key:** take it out of `plan_signer_keys`. The service
  verifies the plan's signature against the current list at **every** signing
  request, not once at submission, so every plan that key signed stops being
  signed for at the next request.
- **Revoking the service:** stop it, and nothing new is signed. Take its
  public key out of the nodes' `job_signer_keys`, and nothing it ever signed
  is accepted again. A separate key (section 4) is what makes that second step
  one line, and leaves operators' ad hoc signing untouched.

### 3.8 The per-step rule

A signing request names the plan, the step, the attempt number the hub wants,
and the job the hub compiled: target, target kind, function, arguments,
keyword arguments and environment. The service answers in this order, and each
refusal names its rule:

1. **The caller** has the hub's role (3.6).
2. **The plan's signature** verifies against a key now in `plan_signer_keys`.
3. **The plan is live**: not cancelled, and `now` is before `not_after`.
4. **The step is in the plan**, and is not `sign: false`.
5. **The job equals the plan's step.** Compared as `jobsign.Canonical`
   (`jobsign.go:130`) of the payload with `jid` and `expires` left empty, on
   both sides, so there is one encoding to agree on and no field-by-field
   comparison code to drift from it. Keyword arguments are compared after
   `WireValues`, so `test` and the pillar override are inside the comparison.
   A mismatch names the first field that differs.
6. **The attempt is within budget.** The budget is `1 + retries`. For attempt
   *n*:
   - *n* already signed and unexpired: **the same answer again**, same jid,
     expiry and signature. This is safe because that jid can run at most once
     on any node (`execute.go:494`, `:510`) and be dispatched at most once by
     the hub (`dispatch.go:590-593`). It is what makes a lost answer
     harmless (3.5).
   - *n* already signed and expired: refused, "attempt *n* expired at …; ask
     for attempt *n*+1".
   - *n* is the next attempt and within the budget: signed (rule 7).
   - Otherwise: refused, naming the budget and what has been spent.
7. **Mint and sign.** The jid comes from the service's own clock, in the
   21-character form the hub accepts for a signed job
   (`internal/job/job.go:48-53`, `:106-119`), strictly increasing so that no
   two attempts share one. The expiry is `now + job_ttl` (3.9). The payload is
   built through `job.SigningPayload`, as `prepareSigning` builds it
   (`run.go:520-553`), signed by the key, written and synced (3.5), and then
   returned.

The service never resolves a target and never needs to: the node checks that
it matches the signed target itself (`jobsignature.go:290-329`).

### 3.9 Expiry, and why a long orchestration works at the default TTL

The service gives each job a short expiry: the step's `job_ttl`, else the
plan's, else fifteen minutes, which is `job.DefaultTTL` (`job.go:24`), capped
by the service's `max_job_ttl`. The service refuses to start with a cap above
one hour, `job_signature_max_ttl`'s default (`jobsignature.go:237`), unless its
configuration also says the nodes allow more. The plan tool refuses a
`job_ttl` above the same cap.

**The expiry bounds delivery, not execution.** A node checks it when the job
arrives and never again (2.3). So a six-hour migration step is signed when the
hub reaches it, delivered within seconds, accepted inside its fifteen minutes,
and runs for six hours. Each step is signed only when the hub gets to it, so
**the length of the orchestration as a whole never meets the expiry**, at the
default `job_signature_max_ttl`, with no node setting changed. That is the
property B could not have.

What a long step still needs:

- **Its own `timeout`**, longer than it runs (2.10). The plan tool warns about
  a fleet step with none, because the default is five minutes.
- **2.10's defect fixed**, or at least known: while the step runs past its
  job's expiry, the hub calls the job `partial` and counts an expiry that did
  not happen. By the reading in 2.10 the step still completes.

What the expiry still bounds: anything whose **delivery** spans longer than
`job_ttl`. That is a batched step whose slices take longer than that in total
(3.15), and a step queued for an offline node that comes back after it. Both
fail loudly, as an expired batch (`batch.go:67-88`) or a node's refusal.

### 3.10 The hub's part

`OrchRequest` (`orch.go:29-45`) gains `Plan`, a plan id. When one is given:

1. **Compile under the plan's rules**: no configuration values and no
   nodegroups (3.4).
2. **Check everything first.** After the compile (`orch.go:117`) and before
   the runner starts (`:147`), the hub fetches the plan from the service,
   compares `env`, `test`, `pillar_digest` and the hub steps by name with its
   own run, computes `stepJob` for every fleet chunk, and sends the list to
   `check`. Any mismatch refuses the whole run before anything is
   dispatched, naming each step and field, for example `apply_web: the plan
   signs target "web*" and this compile targets "web* or db*"`. The whole run,
   rather than the one step, because a step refused mid-run sets off the
   `onfail` steps after it, and a rollback triggered by a stale plan is the
   wrong reaction to one. **This is a convenience for an honest hub, not the
   control.** A1 skips it; the service and the nodes are the control.
3. **At each fleet step**, in `dispatchAndWait` before `dispatchFor`
   (`orch_steps.go:303`), unless the step is `sign: false`: ask the service to
   sign attempt 1, set `sub.JID`, `sub.Expires` and `sub.Signature` from the
   answer, and dispatch as today. Policy is still asked
   (`runners_fleet.go:441`): the signature is in addition to authorisation,
   not instead of it.
4. **If signing fails, the run stops.** No later step runs, `onfail` steps
   included, and the run records that it stopped for the signer and at which
   step. The runner already stops a run after a failure for failhard
   (`internal/runner/runner.go:147-153`, `:181-184`), chosen per chunk or per
   run (`:655-660`); a step module asking for it is new and not designed here.
   The reason is that rollback steps need the same service: running them
   because the service is unreachable tries rollbacks it will also refuse,
   or, if it comes back halfway, runs some of them. Question 11.7 asks whether
   that is the behaviour wanted.
5. **The record** stores the plan id with the run, and with each step the
   attempt and the job id it was signed as, so `orch show` can say which
   signed job each step was. Written per step, as issue 802's fix writes the
   record.

An old hub given `--plan` refuses it rather than ignoring it, by reading:
`state.orchestrate`'s keyword arguments are bound against its signature
(`internal/hub/runner.go:333-340`), and an unknown one is refused as "is not
a parameter of this function" (`internal/signature/signature.go:337-355`).
Not run.

### 3.11 A hub restart, and resume

This section assumes issue 802's fix (2.5).

- **The run dies with the hub**; it is an in-process run. The step's job
  survives as a record, still signed (`model.go:190-203`), and the node that
  accepted it keeps running it.
- **Resume** is `orch resume <run> --from <step> --plan <id>`. Steps before
  `--from` that the record says finished are carried forward as today
  (`orch.go:394-410`) and dispatch nothing, so they need no signature. The
  pre-flight checks only the steps from `--from` on.
- **The step that was in flight** has a job id the hub recorded (802) and the
  service recorded (3.5). Proposed: if that job is in the hub's cache, the
  resumed run **adopts it**, waiting on its returns, rather than dispatching
  again. That spends no attempt and runs nothing twice. Running the step again
  instead is attempt 2, and needs `retries`. If 802's record does not name the
  in-flight job, the service's attempt log does (`GET /v1/plans/<id>`); under
  A1 neither source binds anything, and both exist for the honest hub.
- **A plan with no budget left** cannot be resumed past that step. The
  operator plans the rest again, `orch plan --from <step>`, and submits it: a
  new plan, with new budgets, signed again. That is the one way to authorise
  more than a plan authorised, and it is deliberate.
- A resume with a different pillar override is already refused
  (`orch.go:348-362`), and under a plan its keyword arguments would differ
  from the plan's too.

### 3.12 A service restart, and the service unreachable

- **A service restart** loses nothing (3.5). Requests during it fail as
  "unreachable", below. Afterwards, rule 6 answers a repeated request with
  the attempt already signed.
- **Unreachable**: the hub retries a signing call a bounded number of times
  with backoff, proposed five attempts over about a minute, each failure
  logged at `warn` with its error. Then the run stops (3.10, step 4). What the
  operator sees in `orch show` and the run's return: `migrate_db: the signing
  service at https://signer.example:4510 did not answer after 5 attempts
  (last: connection refused); the run stopped before dispatching this step;
  when the service is back, resume with orch resume <jid> --from migrate_db
  --plan sha256:…`.
- **A step already dispatched is unaffected**: it has its signature, and the
  node has accepted it.
- **Ad hoc signed jobs are unaffected**: `run --sign-extension` does not use
  the service.

### 3.13 Retries

- **`retries: N`** on a step lets the service sign *N* more attempts of it,
  each a new job id. Zero by default.
- **What a retry means under A1, said plainly:** each attempt is a new job,
  so every node its target matches may run the step once more. `retries: 1`
  on a `cmd.run` step means "this command may run twice on each of these
  nodes". `plan show` prints it in those words.
- **An honest hub asks for attempt *n*+1 only when the operator asks** (`orch
  resume`), never by itself; the step's job reports per node, and only the
  operator can judge whether running it again on the nodes that succeeded is
  safe.
- **Phase two** (3.15) narrows a retry to the nodes the hub names, within the
  planned target, which is what an honest hub wants; under A1 the bound is
  unchanged.

### 3.14 Steps that need no signature, hub-side steps, test plans

- **Every fleet step is signed by default**, because neither the plan tool
  nor the hub knows which classes each node requires, and a node ignores a
  signature it does not need (`jobsignature.go:200-203`). A step marked `sign:
  false` is dispatched unsigned as today: right for read-only steps, and for a
  step that needs `run_parallel` (3.16). A node that requires a signature for
  it refuses it, as today.
- **Hub-side steps** are listed by id and name, and an honest hub's
  pre-flight refuses a run whose hub steps differ from the plan. That is all.
  They are authorised by hub policy, as today.
- **A test plan** (`orch plan --test`) signs state steps with `test` inside
  the signature (2.6). Function, runner and wait steps are not dispatched in a
  test run, so a test plan has nothing to sign for them. The plan's `test`
  must equal the run's, and `test` inside each job means a test plan cannot
  sign a real job.

### 3.15 Batch and subset

**Version one.** A batched signed step is one signed job delivered in slices
from its record (`model.go:167-188`, `:190-203`), so it has one signature and
one expiry. The batch sends the next slice when the previous one has
returned (`batch.go:100`), and sends nothing to a job past its expiry
(`batch.go:67-88`). So **every slice but the last must have run and returned
within the step's `job_ttl`**, at most an hour. A rolling deployment over five
hosts at ten minutes each fits; a rolling migration of an hour per host does
not, and fails at the second slice as an expired batch. The plan tool warns
about every batched step, with that arithmetic. `subset` is not a bound under
A1: every node the target matches will accept the job. An operator who wants
a signed canary targets it by name.

**Phase two, per-slice signing with narrowed targets.** For a batched or
subset step the hub asks for one signature per slice and names the slice's
nodes. The service signs a job whose target is

```
( <the planned target, as a compound term> ) and L@<the slice's nodes>
```

with kind compound. The compound grammar takes the parentheses and every
kind's sigil (`internal/target/compound.go:14-21`,
`internal/target/target.go:83-92`). A node checks both halves itself
(`jobsignature.go:290-329`), so A1 cannot reach a node outside the planned
target. The service keeps a per-step count per node, so no node is signed for
more than `1 + retries` times, and the total of distinct nodes is at most
`subset`. That makes **`subset` and the batch size into bounds A1 cannot
exceed**: it still chooses which nodes, and when. Pacing, `batch_safe_limit`
and `tolerate_failures` stay controls for an honest hub, because the service
never sees a return. The costs: a signed batched step becomes a series of
jobs, which is a second batching path beside `job.Batch` that has to agree
with it; and a planned target that will not go into a compound term, such as
a glob or regex containing whitespace, is refused at plan time. Whether
phase two is wanted is question 11.6.

### 3.16 `parallel` and `run_parallel`

As decided (decision 4): outside the signature, and ignored on signed jobs.

- The plan carries no `run_parallel`, and the service neither receives nor
  compares it. The hub still sets `Submission.Parallel` from the step
  (`orch_steps.go:249`), and a node ignores it on a signed job
  (`jobqueue.go:37`), so only the node's own `parallel_jobs` lets a planned
  step run beside another job. That is the existing rule ("a hub may make a
  signed job wait, never make one run alongside another that its signer did
  not ask for", `jobqueue.go:20-24`).
- **A planned run changes what `run_parallel` does on nodes that require no
  signature**: those nodes would honour it on an unsigned job and ignore it on
  the signed one. The plan tool warns about a step that sets `run_parallel:
  true` and is signed, and says that `sign: false` keeps the flag for a step
  that needs no signature.
- `salt.parallel` is refused when it runs (2.7), and a state run's
  `run_parallel: true` is refused at compile time (2.4), at both ends.

### 3.17 What is logged, and where

- **The service's `audit.jsonl` is the authority on what was signed.** One
  synced line per request: time, client name, endpoint, plan id, step,
  attempt, the outcome (`signed`, `repeated`, `refused`), the reason for a
  refusal, and for a signature the jid, expiry, payload digest and tree
  manifest id. Plan submissions and cancellations are in it too, with the
  plan key's name. A signed job a node ran with the orchestration key's name
  in its evidence record (`execute.go:549`) and no line here means the key was
  used outside the service. With KMS, CloudTrail is an independent second
  record of every `Sign` call (`cmd/halite-ext-signer-kms/main.go:1-7`).
- **The service's log**, structured: `info` per signature, `warn` per refusal,
  with the same fields.
- **The hub**: `info` per signed step dispatched (`step`, `plan`, `attempt`,
  `job_jid`, and `signed: true` on the existing step event,
  `orch_steps.go:307-310`); `warn` per pre-flight refusal and per failed call
  to the service, each attempt.
- **The node**: nothing new. A refused step produces the existing refusal
  and evidence record, and an accepted one records the signer's name
  (`execute.go:549`).
- **Metrics, proposed**: on the service,
  `halite_signer_requests_total{endpoint,result}` and
  `halite_signer_plans{state}`; on the hub,
  `halite_hub_orch_signer_requests_total{result}` and
  `halite_hub_orch_plan_refusals_total{reason}`. An alert on the hub's
  failures, and one on the absence of the service's `up` series.

## 4. The threat model, after A'

| Attacker | Can | Cannot |
|---|---|---|
| **A1, a compromised hub** | Have any planned step signed, up to its budget, in any order, and deliver it to any subset of its target; skip steps; ask for a rollback nobody needed; hold a signed job up to its expiry and deliver it then, **including after a cancellation**, which stops only new signatures; run hub-side steps; spend budgets so the honest run cannot finish; misreport in `orch show`; send unsigned jobs, which nodes requiring a signature refuse | Get a step signed that no plan has, or with any field changed; widen a target; extend an expiry; replay a job; turn a test job into a real one; get a plan accepted (it holds no plan key); undo a cancellation; impersonate the service to an operator (the signer CA, 3.6) |
| **A2, the tree** | Change what the hub compiles | Change what the service signs: a changed step mismatches its plan |
| **A3, an insider without a plan key** | Run any orchestration policy allows, unsigned | Make any of it run where signatures are required |
| **A4, the service** | Sign any job with the orchestration key | Reach a node. **Fleet compromise needs A4 and A1 together** |
| **A5, a client certificate** | Cancel, read plans, spend budgets | Anything that widens what is signed |
| **A stolen plan key** | Write and sign plans the service will honour | Deliver them without the hub. Revoked at the next request by removing the key (3.7) |

**A compromised service compared with a stolen operator key.** Both can sign
any job for the nodes that trust that key, and both need the hub to deliver
it. The difference is exposure: an operator's key is used now and then, from
the operator's machine; the service is a network listener that is up for
every orchestration. So it is the larger surface, and the design narrows it:

- the key is in KMS, not extractable, and every `Sign` is a CloudTrail
  record; IAM can withdraw the service's right to sign in one change;
- it is **a key of its own**, so nodes can drop it with one line of
  `job_signer_keys` without touching operators' keys;
- only the hub and operators can reach it, over mutual TLS with a CA the hub
  does not hold;
- it holds no plan key, and refuses to start if its own key is a plan key, so
  it cannot approve its own work.

**Key separation.** Four keys, four roles:

| Key | Held by | Trusted by | Domain | Signs |
|---|---|---|---|---|
| Plan key (new) | The operator | The service, `plan_signer_keys` | `halite-orch-plan-v1` | Plans |
| Orchestration job key (new) | The service, behind KMS | Nodes, one `job_signer_keys` line | `halite-job-v2` | Step jobs |
| Operator job key (exists, `alias/halite-jobs`, DIVERGENCE 5.276) | The operator | Nodes, `job_signer_keys` | `halite-job-v2` | Ad hoc jobs |
| Tree key (issue 764) | CI | Nodes, `state_signer_keys` | `halite-tree-v1` | Manifests |

The domains keep any one signature from verifying as another. Separate keys
keep the compromise of one role from being any other, and make revocation per
role. Two of the separations are not optional: the orchestration job key must
not be the operator's job key, or retiring the service means re-keying the
operators; and a tree key must never be a plan key, because publishing a tree
must not authorise running it. Whether the plan key may be the operator's job
key is question 11.5.

## 5. Alternatives considered

| Option | Keeps authority off the hub? | Verdict |
|---|---|---|
| **A. A bare signer the hub calls per step** (`plan.md:2686-2697`'s sketch) | **No.** It signs any digest the hub sends (2.9) | Rejected. It is the hub-held key, moved somewhere else |
| **A'. A plan-checking signing service off the hub** (this proposal) | Yes | **Chosen.** Each step is signed when the hub reaches it, so the orchestration's length never meets the job expiry, at the default node settings. Costs a service to run, secure and keep available |
| **B. A plan of pre-signed jobs** (the first version) | Yes | **Rejected as the primary design.** Every job is signed at one moment, so the whole orchestration, every step, wait and batch, must finish within one `job_signature_max_ttl`, an hour by default, or the nodes must raise it for every signed job they accept, which weakens the bound 5.330 set. The orchestrations expected here run for hours. Its one advantage, needing no service, is not enough; not planned as a fallback |
| **C. The operator's terminal signs each step live** | Yes | Ties a run of hours to a terminal, which is the failure the hub-owned batch exists to avoid (`model.go:167-174`). A disconnect stalls the run |
| **D. A key on the hub** | No | Rejected in DIVERGENCE 5.169 (`docs/DIVERGENCE.md:16630-16637`). It is also what Salt does (section 10) |
| **E. Nodes verify a signed plan** | Yes | A node change and a larger message per job, and nodes would have to keep per-plan state to enforce budgets. Nothing gained over A', whose jobs a node verifies alone |
| **F. Sign only the orchestration SLS** (issue 764's manifest) and let the hub compile | No | The hub compiles, so A1 compiles what it likes. Kept inside A' as the tree binding (3.4), a record and not a control |
| **G. The service compiles the orchestration itself**, from the signed tree | Yes | Needs the tree, the compiler and the pillar override on the service, which makes it a second hub. The plan is already the authority; the hub's compile matters only to an honest hub's pre-flight |
| **H. The service on the hub's host**, as another account or a jail | No | The hub host's root reaches it |
| **I. The service trusts the hub's CA** | Partly | Lets A1 impersonate the service to operators (3.6) |
| **J. A serverless function with KMS** | Yes | Availability without a host to run. Ties the design to one provider, and the durable state needs a database with conditional writes for the budgets. Part of question 11.1 |

## 6. The operator's workflow

All commands, flags and settings are proposed; none exists.

```sh
# Once: a plan key and the service's key, both in KMS, both new.
#   alias/halite-plans   signs plans; its public half goes in the
#                        service's plan_signer_keys
#   alias/halite-orch    the service's job key; its public half goes in
#                        each node's job_signer_keys as "orch <base64>"

# Plan from the operator's own checkout, without the hub.
halite-hub orch plan deploy.rolling --root ~/estate/state --env base \
    --pillar '{"version":"1.4.2"}' --job-ttl 15m --retries apply_web=1 \
    --state-signer-keys ./tree-keys --out deploy-1.4.2.plan.json

# Read it: every job, target, argument, budget and the tree it binds to.
halite-hub orch plan show deploy-1.4.2.plan.json

# Sign it with the plan key, and hand it to the service directly.
HALITE_EXT_SIGNER_KMS_KEY=alias/halite-plans \
    halite-hub orch plan sign deploy-1.4.2.plan.json \
    --sign-extension ./halite-ext-signer-kms > deploy-1.4.2.plan.sig
halite-hub signer submit deploy-1.4.2.plan.json deploy-1.4.2.plan.sig
#   plan sha256:7d0a… accepted: 3 fleet steps (2 signed), 1 hub step,
#   not after 2026-11-23T18:10:00Z

# Run it. The hub refuses before the first step if its compile disagrees.
halite-hub orch run deploy.rolling --pillar '{"version":"1.4.2"}' \
    --plan sha256:7d0a…

# Watch what has been signed, from the service rather than the hub.
halite-hub signer status sha256:7d0a…

# Stop it: nothing more is signed. Jobs already signed run out their expiry.
halite-hub signer cancel sha256:7d0a… --reason "wrong version"

# A step failed and the cause is fixed: resume. Attempt 2 needs retries.
halite-hub orch resume 20261122T181000123456 --from apply_web \
    --pillar '{"version":"1.4.2"}' --plan sha256:7d0a…
```

`orch run` without `--plan` behaves as today. `orch plan --test` writes a test
plan for `orch run --test --plan`.

## 7. Failure modes

| Failure | What the operator sees | Recovery |
|---|---|---|
| The hub's tree differs from the operator's checkout | The pre-flight refuses the run before any step, naming each step and field that differ | Pull, plan again |
| The orchestration reads hub configuration | `orch plan` fails under strict undefined names, naming the value | Stop reading it (decision 5) |
| A step targets a nodegroup | `orch plan` refuses it with the node's reason (`jobsignature.go:301-310`) | Target by something a node can evaluate about itself |
| The plan's signature does not verify, or its key was removed | `signer submit` refuses it; a later signing request is refused, naming the key | Sign with a key the service trusts |
| The plan is cancelled or past `not_after` | The next signing request is refused, and the run stops at that step (3.10) | Plan again |
| The service is unreachable | The run stops before the step, after bounded retries, each logged (3.12) | Bring it back, resume |
| A step's budget is spent | Refused, naming the budget and the attempts | Plan the rest again (3.11) |
| A batched step outlasts its job's expiry | The batch stops as expired at the next slice (`batch.go:67-88`) | Smaller batches, a longer `job_ttl` within the cap, or phase two |
| A step runs longer than its own `timeout` | The step fails with nodes missing, while the nodes keep running | Set `timeout` (2.10); the plan tool warns |
| A long step runs past its job's expiry | It succeeds; meanwhile `jobs list` shows its job `partial` and the expiry counter rises (2.10) | Nothing, once 2.10's defect is fixed |
| `job_ttl` above the nodes' `job_signature_max_ttl` | Refused by the plan tool and the service's cap. If the nodes' setting is lower than the cap, every node refuses: "this node accepts a signed job for at most …" (`jobsignature.go:264-268`) | Lower the cap to the nodes' setting |
| The hub already has the minted jid | Dispatch refuses it (`dispatch.go:590-593`); the step fails | Resume; the next attempt has a new jid. A microsecond collision between two clocks, possible and unlikely |
| A hub older than the feature given `--plan` | Refused as an unknown argument (3.10), by reading | Upgrade the hub |

## 8. Migration and rollout

- **Nodes: no code change.** Every job the service signs is an ordinary SPEC
  25.6 signed job, verified by the code in 2.3. **One configuration change**:
  the orchestration key's public half added to `job_signer_keys`, built the
  way 5.276 built the operator key's line. A node that does not trust it
  refuses planned steps exactly as it refuses unsigned ones today.
- **The hub and the CLI**: `orch plan`, `plan show`, `plan sign`, the
  `signer` client commands, `OrchRequest.Plan`, the pre-flight, the per-step
  call, the stop on signer failure, the per-step record, the four
  `orch_signer_*` settings.
- **The service**: `signer serve`, its configuration, its state directory, an
  rc.d script and a systemd unit under `contrib`, and a runbook.
- **SPEC and docs, when built**: SPEC 25.6 and 19 describe the service and the
  plan; the hardening chapter's "Orchestration steps are not signed"
  (`docs/handbook/28-hardening.md:109-112`) becomes a section on signing a
  plan, with section 1's limits; chapter 21 gains the planned run. None of
  that is in this change.
- **Order**: issue 802's record first, already in progress; then the
  `stepJob` extraction, a refactor with no behaviour change, proven by the
  hub's existing orchestration tests; then the plan format, the plan tool and
  `plan show`; then the service and its tests; then the hub's client,
  pre-flight and per-step call; then the lab (section 9); then the docs.
  2.10's defect is separate and can go at any time.

## 9. Test plan

The issue's acceptance criteria ask for initial execution and resume on a
signature-enforcing node, and for changed arguments and targets requiring
fresh authorisation. Every item below is to be broken on purpose and seen to
fail.

**Unit and package**

- **`stepJob` equivalence**: for every orchestration in the repository's test
  data and the handbook's chapter 21 examples, the job the hub's step module
  dispatches and the job the plan tool writes have identical
  `jobsign.Canonical` bytes. This is the two-paths test, and the one that
  decides whether the design is usable.
- **The plan**: the signature is over the exact bytes; a changed byte, the
  job domain instead of the plan domain, and a key not in `plan_signer_keys`
  are each refused.
- **The per-step rule**: one test per rule in 3.8, each asserting the refusal
  and that nothing was signed or spent. Rule 5 for every payload field.
- **Durability**: kill the service between the write and the answer; the
  repeated request returns the same jid and signature, and the attempt count
  is unchanged. Kill it before the write; nothing is recorded and the next
  request signs attempt 1.
- **Budgets**: attempt 2 with `retries: 0` refused; with `retries: 1`
  signed, and attempt 3 refused.
- **The service's own refusals at start**: its key among the plan keys; a cap
  above an hour without the setting that allows it.
- **The hub's pre-flight**: each mismatch refuses the whole run **before the
  first dispatch**, asserted by a dispatch counter at zero.
- **A pillar override typed in another key order** still matches, because of
  `WireValues`.
- **TLS**: a client certificate from the hub's CA is refused at the
  handshake; a certificate from the signer CA with an unlisted name is refused
  with 403; the hub refuses a service whose certificate chains to anything but
  `orch_signer_ca_file`.

**Integration: a real hub, a real service and a real node with
`require_job_signature: [arbitrary_code, state]`, on loopback**

1. An orchestration with a `salt.state` step, a `salt.function` step naming
   `cmd.run`, a `salt.runner` step and an `onfail` rollback. Without a plan,
   the two fleet steps are refused by the node. With one, they run, and the
   node's evidence names the orchestration key for each.
2. **Changed arguments**: edit the SLS after planning. The pre-flight refuses.
   With a test hook that skips the pre-flight, standing in for A1, the service
   refuses the step by rule 5. With a second hook that sends the changed job
   with the old signature, the node refuses it.
3. **Changed target**: the same with `tgt` widened. The node outside the
   planned target refuses by the target check.
4. **Replay**: the same attempt dispatched twice. The hub refuses the second;
   with that bypassed, the node refuses it as a replay.
5. **A long step**: a step that runs longer than its job's expiry (a two-minute
   `job_ttl` and a five-minute sleep). It completes, the run succeeds, and
   2.10's `partial` is observed, then `complete`.
6. **Hub restart mid-step**, with 802's record: the in-flight job is adopted
   on resume and nothing runs twice; resuming with attempt 2 instead needs
   `retries`.
7. **Service restart mid-run**, and **service down**: the run stops before
   the next step, names the service, and resumes once it is back.
8. **Cancellation mid-run**: the step in flight finishes; the next is refused,
   and the run stops.

**On real machines**

- The integration list with the hub, the service and the node on three
  different hosts: a FreeBSD lab node (tier 1) and a Linux lab node from
  `contrib/tofu`, signing through `halite-ext-signer-kms` against a real KMS
  key, as 5.276 did, with the lab torn down in the same session.
- The service run under its rc.d script on FreeBSD, and restarted by it.
- **One real long orchestration**, of the database-initialisation kind the
  maintainer expects, at the default `job_signature_max_ttl`, timed, with
  each step's signing latency recorded.
- A relay in the path: a planned step reaching a node behind a relay. The
  relay forwards the job message as received (`forwardDown`,
  `internal/relay/relay.go:261-270`; the `Target`, `TargetKind` and
  `Signature` fields of `internal/transport/wire.go:165-170`), but nobody has
  seen a signed job pass through one.

## 10. What Salt does

Read in `~/saltstack` at `221495872f` (2026-11-19), for the first version of
this proposal; `salt/version.py:85` names 3008 (Argon) as the latest release.

Salt's orchestration runs on the Salt server and publishes each step as an
ordinary job. Its only publication signature (`sign_pub_messages`) is made
with the server's own key (`salt/channel/server.py:1950-1956`) and checked by
the receiver against that key (`salt/channel/client.py:767-780`). That is
option D: it authenticates the server and gives no protection from it. Salt
has no operator-held job signing, so there is nothing to be compatible with.
An existing Salt orchestration runs unchanged without a plan, and with one it
needs only the plan steps around it.

## 11. Open questions for the maintainer

Only the ones the design cannot settle from the code.

1. **Where does the service run?** It must not be the hub's host, nor a jail
   on it (3.5). A small dedicated host or VM in the estate; a cloud instance
   whose role can only call `kms:Sign` on the orchestration key; or a
   serverless function, which trades the host for a provider and a database
   (option J). Which machine is a fact about the estate this proposal does not
   have.
2. **One instance, or highly available?** I recommend one. While it is down,
   planned orchestrations stop at their next signed step and resume
   afterwards, and nothing else is affected. Two instances must share the
   budgets with a consistent write, or one step could be signed twice; that is
   a database, not a directory.
3. **Plan lifetime.** A default for `not_after`, and a maximum the service
   enforces. I suggest 24 hours by default and seven days at most: long enough
   for an orchestration of hours and a resume the next morning, short enough
   that a forgotten plan stops being signable on its own.
4. **A subcommand or its own binary?** `halite-hub signer serve` adds no
   artifact, archive or release plumbing, and it shares the code it needs.
   Its own binary, say `halite-signer`, makes "this host runs the hub" and
   "this host runs the signer" visibly different, and keeps the compiler and
   the hub's code off the signer's host. I lean to the subcommand for version
   one.
5. **May the plan key be the operator's existing job key** (`alias/halite-jobs`)?
   It is sound by domain separation, and it adds no authority, because that
   key can already sign any job. A separate key makes revocation per role. I
   recommend separate.
6. **Phase two (3.15): per-slice signing with narrowed targets.** Is it
   wanted? Without it, a batched signed step must finish all but its last slice
   within one `job_ttl`, and `subset` is not a bound under a compromised hub.
7. **A signer failure stops the run without running `onfail` steps** (3.10).
   That differs from every other step failure. Is that the behaviour wanted,
   or should `onfail` run and be refused in its turn?
8. **2.10's defect.** May I open an issue for `Settle` marking a still-running
   job `partial`? It affects unsigned orchestration steps today and is
   independent of this design.
9. **Retries by default.** I propose zero, with a retry meaning "may run once
   more on every node of the target" (3.13). Is there a class of step, such as
   `state.apply`, for which one retry should be the default?

## 12. What I did not verify

- **Nothing here has been run.** Every "today" is a reading of `33dc8e21`.
- That nothing on a node checks the expiry after acceptance (2.3): read from a
  search of the node's command package and `internal/job/guard.go`, not by
  running a job past its expiry.
- 2.10 entire: that `Settle` marks a still-running job `partial`, that the
  late return is filed and completes it, and that the orchestration step then
  succeeds. Read, not run.
- That an off-hub compile reproduces the hub's for real orchestrations once
  both use no configuration values and no nodegroups. The compiler is shared
  code; the trees, as gitfs materialises one and as a checkout holds it, may
  still differ.
- That an old hub refuses `--plan` (3.10): read from `runner.go:333-340` and
  `signature.go:337-355`, not run.
- That a relay passes a signed job intact (section 9).
- That a long orchestration of the kind expected here works end to end at the
  default TTL. The design says it should; nobody has run one.
- PR 809 was open, not merged, when this was written. `treesign.ID` and the
  absence of `tree_revision` were read on its branch at `5e57f684`, and may
  change before it merges.
- The claims about AWS KMS key policies and CloudTrail are general knowledge
  of AWS, not checked against the key `halite-ext-signer-kms` uses.
- Salt's behaviour was read in source, for the first version, and not run.
