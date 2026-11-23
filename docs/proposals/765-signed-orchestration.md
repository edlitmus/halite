# Proposal: orchestration on nodes that require signed jobs (issue 765)

**Status: a design, not built.** Written 2026-11-22 against `main` at
`d0d5925c`. Every statement about what the code does today cites the file
and line it was read at, at that commit. Nothing here has been run; section
11 lists what was not verified.

Nothing holds this file to the code: `internal/docsaudit` reads only
`docs/*.md` (`internal/docsaudit/docsaudit_test.go:157-165`, `:200-212`).
When part of it is built, that part's ledger entry is the record.

**Confidence: 3/5.** The recommended design changes nothing on the node and
nothing on the wire. Each orchestration step becomes an ordinary SPEC 25.6
signed job, which the node already verifies, so the security argument rests
on code that exists and has been demonstrated (DIVERGENCE 5.128, 5.276). Two
things keep this at 3 rather than 4. Whether an off-hub compile reproduces
the hub's compile exactly for real orchestrations is unknown, and the
design depends on it. And the expiry bound (3.6) may be too tight for the
orchestrations an estate would actually run. This estate runs none today
(2.8), so nobody has measured either.

## 1. The problem, and who the attacker is

A node with `require_job_signature` refuses a job of a class it requires a
signature for unless the job carries a valid signature from a key in its
`job_signer_keys` (`cmd/halite-node/jobsignature.go:199-226`). An
orchestration's steps are jobs the hub dispatches itself, unsigned. So on
such a node, a `salt.state` or a `salt.function` step that names an
`arbitrary_code` or `state` function is refused, and the hardening chapter
says so (`docs/handbook/28-hardening.md:109-112`).

| Attacker | Controls | What this design must still guarantee |
|---|---|---|
| **A1, a compromised hub** | It compiles the orchestration, so it chooses every step's target, arguments and job id, and it dispatches. It can call any service it has credentials for, a signer included | A step of a class the node requires a signature for runs only if a holder of a trusted key authorised **that exact job**: its id, target and target kind, function, arguments, keyword arguments, environment and expiry. A1 cannot create one, alter one, send it to a node its target does not match, extend its life, or replay it |
| **A2, someone who can push to the orchestration's tree** | The orchestration SLS the hub compiles | The same. A changed orchestration compiles to different steps, which nobody signed |
| **A3, an insider with API access and no signing key** | Can ask the hub to run any orchestration their policy allows | The same. Policy decides what the hub will try; the signature decides what a node will run |

**What this does not deliver:**

- **Sequence.** A signed plan authorises a set of jobs, not an order. A1
  holding a plan can send its steps in another order, skip a step (the
  drain before a deploy), send a rollback step when nothing failed, or hold
  everything back. Each step it sends is still exactly what was signed, to
  exactly the nodes its target matches, within its expiry. Nodes see one job
  at a time and cannot check an order. Open question 10.3.
- **Hub-side steps.** `salt.runner`, `salt.wheel` and `salt.wait_for_event`
  run on the hub (`internal/hub/orch_steps.go:302-342`, `:344-395`). A1
  runs them regardless. Nothing a node does can constrain them.
- **Batch, subset, safe limit and `tolerate_failures`.** None of these is in
  the signed payload (`internal/job/model.go:398-409`). They are applied by
  the hub (`internal/hub/dispatch.go:101-125`). A1 can ignore `subset: 1`
  and send a signed canary step to every node its target matches. Under
  signatures they are controls an honest hub honours, not authorisations.
- **Parallelism**, which section 3.10 covers.

## 2. What happens today

### 2.1 A step is a job the hub dispatches unsigned

- The step modules are built per run (`orchStates`,
  `internal/hub/orch_steps.go:23-185`). A fleet step's `Submission` is built
  by `submission` (`orch_steps.go:238-264`) with target, target kind,
  function, environment, `test`, batch and subset. It has no job id, expiry
  or signature. `applyState` adds the SLS names, `test` from the run, and the
  `pillar` and `pillarenv` keyword arguments (`orch_steps.go:188-212`).
  `callFunction` adds positional and keyword arguments
  (`orch_steps.go:215-234`).
- `dispatchAndWait` sends it through `dispatchFor` and waits for the returns
  (`orch_steps.go:274-300`). `dispatchFor` authorises the step against
  policy as the run's principal, and the operator it acts for if any, before
  `Dispatch` (`internal/hub/runners_fleet.go:441-463`).
- An unsigned job's id and expiry come from the hub's clock (`identify`,
  `internal/hub/dispatch.go:570-577`).

### 2.2 The hub can already carry a signed job it did not sign

- `Submission` has `JID`, `Expires` and `Signature`, "set together or not at
  all". On a signed submission the hub checks them and does not choose them
  (`dispatch.go:47-58`).
- `identify` refuses a signed submission whose id is invalid, whose expiry
  is missing or past, or whose id **this hub already has a job for**
  (`dispatch.go:578-596`). So the hub will not deliver one signed job twice
  under one id.
- The record keeps the signature, so that a queued job or a resumed batch is
  delivered from the record still signed (`internal/job/model.go:190-203`).

### 2.3 What a node checks

`checkJobSignature` (`cmd/halite-node/jobsignature.go:199-226`), in order:

1. Whether this function needs a signature at all: everything under `true`,
   or by class (`arbitrary_code`, `state`, `mutating`), and any function
   this build does not ship whenever anything is required
   (`jobsignature.go:121-159`).
2. The signature verifies against a key in `job_signer_keys`
   (`internal/jobsign/jobsign.go:475-518`), over `job.SigningPayload`
   (`internal/job/model.go:398-409`).
3. The expiry lies no further ahead, on arrival, than
   `job_signature_max_ttl`, an hour by default, with a minute of skew
   (`jobsignature.go:228-271`).
4. This node matches the signed target (`jobsignature.go:273-329`). A
   nodegroup target is refused, because a node cannot evaluate one about
   itself (`:283-289`, `:301-309`).

And before that check, a signed job id this node has already accepted is
refused as a replay, from a record on disk that is synced before the job
can run (`cmd/halite-node/execute.go:465-488`).

### 2.4 The orchestration is compiled on the hub, from the hub's tree

`compileOrchestration` (`internal/hub/orch.go:184-222`) compiles from
`s.Files` with the run's pillar override, no grains, the hub's configuration
values, the hub's nodegroups, no `salt` dispatcher, and strict undefined
names. `orch lint` (`state.orchestrate_show_sls`) is a runner call that does
the same compile on the hub (`cmd/halite-hub/orch.go:49-73`). There is no
off-hub compile today.

### 2.5 Resume, and a hub restart

- A resume is a new run with a new id from the hub's clock (`orch.go:98`),
  compiled again. It is refused if the pillar override differs from the one
  the run started with (`orch.go:287-309`) and, for a real resume, if the
  earlier run was a test run (`orch.go:311-339`). Steps before `--from` that
  the earlier run recorded are carried forward as results. A step it never
  reached is not seeded and runs now (`orch.go:341-357`).
- **The run's record is written twice: at the start, with no steps, and at
  the end** (`s.recordOrch(run)` at `orch.go:144` and `:176`; `run.Steps` is
  assigned at `:159`). By that reading, a hub that stops mid-run leaves a
  record with no steps. A resume of it then carries nothing forward and runs
  every step before `--from` again. I found no code that marks such a run
  interrupted: a search of `internal/hub` for the running state finds only
  its definition (`orchstore.go:63`) and its assignment (`orch.go:106`). I
  have not run this. Under signatures it matters (3.7).
- A step's own job survives a hub restart as a record, still signed, and a
  batched one can be continued with `jobs resume` (`model.go:167-203`).

### 2.6 Test mode

In a test run, state steps are dispatched with `test` set
(`orch_steps.go:197-201`), and `test` becomes a keyword argument on the
wire (`job.WireKwargs`, `model.go:377-389`), so it is inside the signature.
Function steps are not dispatched at all in a test run
(`orch_steps.go:275-277`). A signed dry run cannot be turned into a real one,
because the encoding types the value (DIVERGENCE 5.330,
`internal/jobsign/jobsign.go:73-81`).

### 2.7 `parallel` under signatures today

- `Job.Parallel`, the operator's `--parallel`, is kept on the record
  (`model.go:160-165`) and sent on the message, and it is **not** in the
  signed payload (`model.go:398-409`). On a signed job, a node ignores it and
  runs the job alone unless its own `parallel_jobs` says otherwise
  (`cmd/halite-node/jobqueue.go:21-42`, the check at `:38`). A state run is
  always alone (`jobqueue.go:14-19`, `:32-34`).
- An orchestration step cannot ask for `--parallel`: `submission` never sets
  it (`orch_steps.go:248-259`). The commit that let an API caller ask for it
  says "Orchestration steps and reactions still cannot ask for it"
  (`d0d5925c`).
- `salt.parallel` is refused when it runs (`orch_steps.go:167-182`), and a
  state's `parallel: True` is parsed, warned about and ignored
  (`internal/state/compile.go:634-645`).
- **There is no `run_parallel`** in this repository. A search of every `.go`
  and `.md` file for it returns nothing. Salt's nearest are the
  `salt.parallel_runners` state (`salt/states/saltmod.py:911` in
  `~/saltstack`) and the `parallel` keyword (`salt/state.py:2515`).

### 2.8 What the record already says about this

- DIVERGENCE 5.169 (`docs/DIVERGENCE.md:16582-16642`) explains why this is
  not a wiring gap. A signer has to choose the job id and expiry before it
  signs (`cmd/halite-hub/run.go:505-554` mints the id on the signer's
  machine). A step's target and id are decided by the hub mid-run. It names
  two ways forward, "a live signer" and "a pre-approved plan", and rejects a
  hub-held key (`:16630-16637`).
- `plan.md:2256-2258` and `:2686-2697` keep the choice open as "a live
  call per step out of `orchRunner.dispatchAndWait`, or a pre-approved plan".
- **The estate runs no orchestration today.** A search of the estate's state
  tree (2026-11-22) for `salt.state`, `salt.function` and `salt.runner` finds
  nothing. So this feature has no
  current consumer here, which bears on its priority (open question 10.8).

### 2.9 A finding about 5.169's "live signer"

5.169 describes the live signer as one where "the hub calls out, per step
... and the KMS signs bytes it is handed rather than a private key the hub
holds" (`docs/DIVERGENCE.md:16612-16616`). Read against the signer that now
exists, that sentence concludes too much. The bridged signer protocol hands
over a digest and gets back a signature (DIVERGENCE 5.170,
`docs/DIVERGENCE.md:16653-16657`). The KMS signer refuses only a digest that
is not 32 bytes (DIVERGENCE 5.276). A KMS key policy authorises an identity
to sign, and it does not read what the 32 bytes are the digest of. So a hub
that can call the signer can get **any** payload signed. Moving the key into
a KMS removes the key from the hub's disk, and it does not remove the
hub's ability to sign. That ability is what SPEC 25.6 exists to deny
(`SPEC.md:2554`). A live signer keeps authority off the hub only if the
signer itself checks each request against something the hub cannot forge.
That is a plan, which is why section 4's option A' is a plan-checking
service and not a bare KMS call.

## 3. The design: a plan of pre-signed jobs

### 3.1 In one paragraph

The operator compiles the orchestration **off the hub**, from their own
checkout, with the same compiler the hub uses. Each fleet step becomes the
exact `Submission` the hub would build, and from it a complete SPEC 25.6
job with an id and an expiry the operator mints. The operator signs each
job with the signer they already use (`--sign-key` or `--sign-extension`)
and writes the lot to a plan file. `orch run --plan` hands the plan to the
hub. The hub compiles as it does today and compares every fleet step with
the plan before it dispatches anything. At each step it sends the plan's
signed job in place of an unsigned one. The node verifies each job exactly
as it verifies any signed job today. **The node does not know plans exist.**

### 3.2 Compiling off the hub, with one path for both ends

- The plan tool (`halite-hub orch plan`, proposed) runs `state.Compiler`
  with the inputs `compileOrchestration` gives it (`orch.go:190-221`): the
  operator's checkout as the loader, the same pillar override, no grains,
  no `salt` dispatcher, strict undefined names. It cannot have the hub's
  nodegroups, and needs none: a signed nodegroup target is refused by every
  node (2.3), so the plan tool refuses one at plan time with the node's
  reason. The hub's configuration values (`orch.go:202`, `:229-236`) are an
  input it does not have. It takes them from `--hub-config <file>`, or
  compiles with none, and a template that reads one then fails at plan time
  under strict undefined (open question 10.5).
- **The step-to-job translation must be one function.** Today it is spread
  through `submission`, `applyState` and `callFunction`
  (`orch_steps.go:188-264`), interleaved with dispatch. Building this means
  extracting a pure `stepJob(chunk, run) (Submission, error)` that the hub's
  step modules, the hub's pre-flight (3.5) and the plan tool all call. Two
  copies of "what job does this step make" would be two things that must
  agree, and this design fails closed when they do not, which is loud and
  safe but would make it unusable.
- Keyword arguments go through `jobsign.WireValues` before signing, as
  `prepareSigning` does (`cmd/halite-hub/run.go:505-554`). The step's
  `pillar` mapping is an ordered map from the value model (`orch_steps.go:202`),
  and a signature over the typed order would not verify against what the
  node decodes.

### 3.3 The plan file

Proposed schema `halite.orch-plan/1`. JSON, written by the plan tool, read by
the hub:

```json
{
  "schema": "halite.orch-plan/1",
  "sls": ["deploy.rolling"],
  "env": "base",
  "test": false,
  "pillar_digest": "sha256:4b2f…",
  "created": "2026-11-22T18:10:00Z",
  "signer": "ops",
  "steps": [
    {
      "id": "apply_web",
      "step": "salt.state",
      "job": {
        "jid": "20261122T181000123456",
        "target": "web*", "target_kind": "glob",
        "fun": "state.apply", "arg": ["web"], "kwarg": {"pillar": {"version": "1.4.2"}},
        "env": "base", "expires": "2026-11-22T19:05:00Z"
      },
      "signature": "MEUCIQ…"
    }
  ],
  "hub_steps": [
    {"id": "announce_start", "step": "salt.runner", "name": "event.send"}
  ]
}
```

- `steps` holds one entry per fleet step (`salt.state`, `salt.sls`,
  `salt.highstate`, `salt.function`) the compile produced, keyed by state ID.
  `job` is `job.SigningPayload`'s field set (`model.go:398-409`) and nothing
  else, so the plan cannot carry something the node does not check.
- Job ids are minted by the plan tool in the 21-character form the hub
  accepts for a signed job (`internal/job/job.go:48-53`, `:119-126`), one
  microsecond apart so that no two steps share one. They carry the time the
  plan was made, not the time the step runs.
- `pillar_digest` is the same digest a run records for its resume check
  (`orch.go:109`, `:295-309`), so a plan for one override cannot be run with
  another.
- `hub_steps` lists the runner, wheel and wait steps for the operator to
  read. It is informational, because nothing can enforce it (section 1).
- **No signature over the plan as a whole.** Nodes never see the plan, and
  the hub is the party it would have to be protected from, so a plan-level
  signature would be checked only by the attacker. Each step's job signature
  is the authority. A plan file edited in transit can only drop steps, or
  pair a signature with the wrong job, and either one fails at the node.

### 3.4 Who signs, with what

The signer is whatever already signs jobs: a local key
(`halite-hub keys signer create`) or a bridged signer extension such as
`halite-ext-signer-kms`. The operator runs it **on their own machine, against
payloads their own compile produced**. That is what keeps the authority
independent of the hub. The hub never asks for a signature and holds nothing
that can make one. No new key type, no new trust list, and no new node
setting: the node's `job_signer_keys` already decides.

### 3.5 The hub's part: check everything first, then dispatch

`OrchRequest` (`orch.go:27-44`) gains a `Plan`. When one is given:

1. **Before any step runs**, after the compile at `orch.go:116` and before
   `Runner.Run` at `:146`, the hub computes `stepJob` for every fleet chunk
   in `compiled.Low`, puts the plan entry's id and expiry into it, and
   compares its `jobsign.Canonical` bytes, the bytes the node will check,
   with those of the plan entry of the same state ID. Then:
   - a fleet step with no plan entry: refuse the run;
   - a plan entry with no fleet step: refuse the run;
   - a field that differs: refuse the run, naming the step and the field
     (for example `apply_web: the plan signs target "web*" and this compile
     targets "web* or db*"`);
   - `test`, `env` or `pillar_digest` differing from the run: refuse the run;
   - a plan expiry already past, or a job id this hub already has
     (`dispatch.go:590-596`): refuse the run.

   The whole run is refused, rather than the one step, because a step
   refused mid-run triggers the `onfail` steps after it, and a rollback set
   off by a mismatch is the wrong reaction to a stale plan.
2. **At each step**, `dispatchAndWait` (`orch_steps.go:274-300`) sets
   `sub.JID`, `sub.Expires` and `sub.Signature` from the plan before
   `dispatchFor`. Policy is still asked, as today (`runners_fleet.go:441-463`):
   the signature is in addition to authorisation, not instead of it.
3. **The record**: the run stores the plan's digest and each step's signed
   job id, so `orch show` can say which signed job each step was.

The pre-flight is a convenience for an honest hub, so that a stale plan
fails at once, by name, before anything runs. **It is not the control.** A1
skips it. The nodes are the control, and they need nothing new.

### 3.6 The expiry budget

A node refuses a signed job whose expiry is further ahead, on arrival, than
`job_signature_max_ttl` (an hour by default, `jobsignature.go:237`). All of a
plan's jobs are signed at one moment and run at different ones. So:

- the plan tool gives every step the same expiry, `created + --ttl`, and
  refuses a `--ttl` over one hour unless `--max-ttl` says the nodes allow
  more;
- **the whole orchestration, every batch and every wait included, must
  finish within that window**, or its later steps are refused as expired by
  the hub (`dispatch.go:585-589`) or by the node;
- an orchestration that needs longer is split into plans, or run against
  nodes whose `job_signature_max_ttl` is raised. Raising it lengthens the
  life of every signed job those nodes accept, not only orchestration's. That
  is the trade-off, and it is the reason to consider option A' (section 4).

### 3.7 Resume, a hub restart, and changed steps

- **A resume always needs a new plan.** `orch plan --resume-of <jid> --from
  <step>` compiles the same orchestration and signs fresh jobs for `--from`
  and every fleet step after it, with new ids and a new expiry. It does not
  reuse the original plan's signatures, for two reasons. The step that
  failed has a job id that is spent: the hub refuses it
  (`dispatch.go:590-596`) and every node that accepted it refuses it
  (`execute.go:465-473`). And the original expiry may have passed.
- **Steps before `--from`** are carried forward by the hub from its own
  record, as today. They dispatch nothing, so they need no signature. Under
  A1 the hub can claim anything about which steps ran, but it cannot run
  anything unsigned, so a lie costs the honest outcome and grants nothing.
- **The interrupted-run case (2.5)**: a run stopped by a hub restart has a
  record with no steps, so a resume of it would re-run every earlier step.
  With a resume plan that signs only `--from` onwards, those earlier steps
  have no plan entry and the pre-flight refuses the run, naming them. That
  is the correct outcome, and it is loud. The operator decides whether to
  sign the earlier steps again. The record itself should be fixed so that it
  is written after each step, separately and before this (open question
  10.6).
- **A step's own job across a hub restart**: the signed job record survives
  (`model.go:190-203`), and `jobs resume` continues a batched step with the
  same signature until its expiry. A node that already ran it refuses the
  replay, which is right.
- **Changed arguments or targets need fresh authorisation by construction.**
  A changed orchestration, pillar override or tree compiles to different
  steps. The pre-flight refuses a mismatch. A hub that skips the pre-flight
  and sends the changed job sends it with a signature over the old payload,
  and the node refuses it ("the signature does not verify against any of
  this node's ... trusted signer key(s)", `jobsign.go:516-517`). A hub that
  sends the old, signed job to a node the new target names but the old one
  does not is refused by the target check (`jobsignature.go:323-326`).

### 3.8 Steps that need no signature, and runs without a plan

- With a plan, **every** fleet step must be in it. The plan tool cannot know
  which classes each node requires, so it signs all of them. A read-only
  `test.ping` step signed and sent to a node that requires nothing runs as it
  always did: a node ignores a signature it does not need
  (`jobsignature.go:200-203`).
- Without a plan, behaviour is unchanged: steps are unsigned, and nodes that
  require signatures refuse them, as the hardening chapter already says.
- A test plan (`orch plan --test`) signs state steps with `test: true`
  inside the signature, and signs no function steps, because the hub
  dispatches none in a test run (2.6). A test plan cannot run as a real one:
  `test` differs, and the pre-flight and the node both refuse.

### 3.9 Hub-side steps

`salt.runner`, `salt.wheel` and `salt.wait_for_event` are listed in the
plan's `hub_steps` and compared by the pre-flight by name, so that an honest
hub refuses an orchestration whose hub steps changed since it was planned.
That is the whole of it. A compromised hub runs runners whatever a plan
says, and runners are authorised by hub policy (`runners:` grants), which
this design does not change.

### 3.10 `parallel` and `run_parallel` under signatures

- **The `--parallel` flag stays outside the signature and ignored on signed
  jobs** (`jobqueue.go:38`). A plan never sets it, and a hub that sets it on
  a signed step changes nothing on the node. Only the node's own
  `parallel_jobs` lets a signed job run beside another. This is the existing
  rule ("a hub may make a signed job wait, never make one run alongside
  another that its signer did not ask for", `jobqueue.go:21-26`), and this
  design keeps it.
- **Putting `parallel` inside the signature** would mean a v3 encoding: a new
  domain string, because the field list is the domain's contract
  (`jobsign.go:68-81`). Every node older than the change would refuse every
  v3-signed job, since `Verify` checks the v2 domain only and names v1 for
  diagnosis (`jobsign.go:500-517`). So signers would have to move after the
  last node, not before. Not proposed now (open question 10.4).
- **`salt.parallel` and `parallel: True`** do not interact with signatures
  as built: the hub runs one step at a time (`orch_steps.go:178-180`;
  `compile.go:634-645`), and each step is its own signed job either way. If
  either is ever built, concurrency changes nothing about what is authorised.
  It only spends the expiry budget (3.6) faster, because steps could then run
  side by side with the same window.
- **`run_parallel` does not exist** here (2.7), so there is nothing for it to
  do under signatures. If the name was meant as Salt's
  `salt.parallel_runners`, that runs runners, which are hub-side steps (3.9).

### 3.11 Batch, subset and safe limits

These stay hub-side and unsigned (section 1). A batched signed step is one
signed job delivered in slices from its record (`model.go:167-188`,
`:190-203`), so batching needs no extra signatures, only an expiry that
outlasts every slice and wait (3.6). `subset` is the one worth a warning in
the operator's documentation. Under A1, `subset: 1` does not limit a signed
step to one node. Every node its target matches will accept it. An operator
who wants a signed canary should target the canary by name.

### 3.12 What is logged

Names proposed:

- Hub, at `warn`, for a pre-flight refusal, with `jid` (the run), `step`,
  `field`, `plan_digest`. Example: `orchestration 2026… refused before it
  started: step apply_web is signed for target "web*" and compiles to
  "web* or db*"; plan the orchestration again and sign the new plan`.
- Hub, at `info`, for each signed step dispatched: `step`, `job_jid`, and
  `signed: true` on the existing step event (`orch_steps.go:284-287`).
- Node: nothing new. A refused step produces the existing refusal and
  evidence record, and an accepted one records the signer's name, as for any
  signed job (`execute.go:522-523`).
- Metric, proposed: `halite_hub_orchestration_plan_refusals_total{reason}`.

## 4. Alternatives considered

| Option | Keeps authority off the hub? | Why not, or not yet |
|---|---|---|
| **A. A live signer the hub calls per step** (plan.md's "live call") | **No.** The signer signs any digest the hub sends it (2.9) | Rejected as described. It is the hub-held key with the key moved somewhere else |
| **A'. A plan-checking signing service off the hub.** The operator signs a plan with their own key. A separate service holds the job key, and when the hub asks for a step it signs only a job that matches an approved plan, minting a short expiry itself, and only once per step unless the plan allows a retry | Yes | The best long-term shape: it removes the expiry budget (3.6), so an orchestration of any length runs against nodes with the default `job_signature_max_ttl`. Not now, because it is a new network service to run, secure, monitor and keep available for every orchestration, for an estate with one maintainer and no orchestrations. The plan schema in 3.3 is chosen so that this service can consume it later unchanged |
| **B. Pre-signed plan** (this proposal) | Yes | Recommended. No node change, no wire change, no new service. Limited by the expiry budget |
| **C. The operator's terminal signs each step live.** The hub sends each step's payload back over the open runner call and the CLI signs it | Yes | Ties the run to the terminal, which is the failure the hub-owned batch exists to avoid (`model.go:167-174`). A disconnect stalls the run, and resume after one has nothing to sign with. Needs a two-way protocol on runner calls that does not exist |
| **D. A key on the hub** | No | Rejected in DIVERGENCE 5.169 (`docs/DIVERGENCE.md:16630-16637`). It is also what Salt does (section 9) |
| **E. Nodes verify a signed plan** and check that the step they receive is in it | Yes | A node change, a larger message per job, the same expiry problem, and nothing gained over B, because each step in B is already a job the node can verify alone |
| **F. Sign only the orchestration SLS** (with issue 764's manifest) and let the hub compile it | No | The hub compiles, so A1 compiles whatever it likes. Useful alongside B, to bind the operator's checkout to a signed revision (open question 10.7), and not a control on its own |

## 5. The operator's workflow

All commands and flags are proposed; none exists.

```sh
# Plan from the operator's own checkout, without the hub, and sign.
halite-hub orch plan deploy.rolling --root ~/estate/state --env base \
    --pillar '{"version":"1.4.2"}' --ttl 45m \
    --sign-extension ./halite-ext-signer-kms --out deploy-1.4.2.plan.json

# Read it: every job, target, argument and expiry, in step order.
halite-hub orch plan show deploy-1.4.2.plan.json

# Run it. The hub refuses before the first step if its compile disagrees.
halite-hub orch run deploy.rolling --pillar '{"version":"1.4.2"}' \
    --plan deploy-1.4.2.plan.json

# A step failed and the cause is fixed: plan the resume, and run it.
halite-hub orch plan deploy.rolling --root ~/estate/state \
    --pillar '{"version":"1.4.2"}' --resume-of 20261122T181000123456 \
    --from verify_web --sign-extension ./halite-ext-signer-kms --out resume.plan.json
halite-hub orch resume 20261122T181000123456 --from verify_web \
    --pillar '{"version":"1.4.2"}' --plan resume.plan.json
```

`orch run` without `--plan` behaves as today. `orch plan --test` writes a
test plan for `orch run --test --plan`.

## 6. Failure modes

| Failure | What the operator sees | Recovery |
|---|---|---|
| The hub's tree differs from the operator's checkout | The pre-flight refuses the run before any step, naming the first step and field that differ | Pull, plan again |
| The orchestration reads a hub configuration value the plan tool was not given | `orch plan` fails under strict undefined, naming the value | Pass `--hub-config`, or stop reading it |
| A step targets a nodegroup | `orch plan` refuses it with the node's reason (`jobsignature.go:283-289`) | Target by something a node can evaluate about itself |
| The run outlasts the plan's expiry | Later steps are refused by the hub as expired (`dispatch.go:585-589`). The run fails at that step and its `onfail` steps run if they are in the plan and unexpired | Plan the resume, with a longer `--ttl` the nodes allow, or split the orchestration |
| `--ttl` above the nodes' `job_signature_max_ttl` | Every node refuses every step: "this node accepts a signed job for at most …" (`jobsignature.go:264-268`) | Plan again with a shorter `--ttl` |
| A hub older than this feature given `--plan` | It must refuse the unknown argument, not ignore it. If it ignored it, it would dispatch unsigned steps, which nodes refuse, and that is loud. Whether `state.orchestrate` refuses an unknown keyword today is not checked | Upgrade the hub |

## 7. Migration and rollout

- **Nodes: nothing.** No setting, no code and no wire change.
- **The hub and the CLI:** `orch plan`, `OrchRequest.Plan`, the pre-flight,
  and the `stepJob` extraction (3.2). The extraction is a refactor with no
  behaviour change, and goes in its own change first, with the hub's existing
  orchestration tests as the proof that nothing moved.
- **Docs:** the hardening chapter's "Orchestration steps are not signed"
  (`docs/handbook/28-hardening.md:109-112`) becomes a section on signing a
  plan, including the limits in section 1 and the canary warning in 3.11.
- Order: extraction, then plan tool and pre-flight, then the lab
  demonstration (section 8), then documentation.

## 8. Test plan

The issue's acceptance criteria ask for initial execution and resume on a
signature-enforcing node, and for changed arguments and targets requiring
fresh authorisation. Each item below is to be broken on purpose and seen to
fail.

**Unit and package**

- `stepJob` equivalence: for every orchestration under the repository's
  test data and the handbook's chapter 21 examples, the job the hub's step
  module dispatches and the job the plan tool signs have identical
  `jobsign.Canonical` bytes. This is the two-paths test, and the one that
  decides whether the design is usable.
- The pre-flight: each row in 3.5, step 1 (missing step, extra step, each
  payload field, `test`, `env`, pillar digest, expired, spent id) refuses
  the whole run **before the first dispatch**, asserted by a dispatch
  counter at zero.
- A pillar override whose mapping is typed in a different key order from
  the one the hub holds still matches, because of `WireValues` (3.2).
- A test plan run for real, and a real plan run under `--test`: both
  refused.

**Integration, through a real hub and a real node with
`require_job_signature: [arbitrary_code, state]`**

1. An orchestration with a `salt.state` step, a `salt.function` step naming
   `cmd.run`, a `salt.runner` step and an `onfail` rollback: unsigned, the
   two fleet steps are refused by the node; with a plan, they run, and the
   node's evidence records the signer for each.
2. **Changed arguments**: edit the orchestration's SLS after planning. The
   pre-flight refuses. Then, with a test hook that skips the pre-flight
   (standing in for A1), the node refuses the changed job as unsigned for
   its payload.
3. **Changed target**: the same, with `tgt` widened. The node outside the
   signed target refuses it by the target check.
4. **Replay**: dispatch a plan's step twice. The hub refuses the second, and
   with the hub's check bypassed, the node refuses it as a replay.
5. **Resume on a signing node**: make a step fail, fix the cause, plan the
   resume, run it. The resumed steps run and the carried steps dispatch
   nothing. Resume with the *original* plan: refused, because the failed
   step's id is spent.
6. **Hub restart mid-run**: stop the hub during a batched step and start it
   again. `jobs resume` continues the step's batch with its signature. Then
   resume the orchestration with a resume plan, and watch the pre-flight
   refuse the earlier steps the empty record would re-run (2.5, 3.7).
7. **Expiry**: a plan whose `--ttl` ends mid-run. The later step is refused
   by the hub as expired, and the run fails at that step rather than
   hanging.

**On real machines**

- The integration list on a FreeBSD lab node (tier 1) and a Linux lab node
  from `contrib/tofu`, signing through the AWS KMS signer that already works
  (DIVERGENCE 5.276), with the lab torn down in the same session.
- One measured orchestration of the shape this estate would run, timed, to
  find out whether an hour's expiry budget (3.6) is enough in practice.
- A relay in the path: a plan's step reaching a node behind a relay. The
  relay forwards the job message unchanged (the `Signature`, `Target` and
  `TargetKind` fields of `internal/transport/wire.go:165-170`, passed on as received by `forwardDown`, `internal/relay/relay.go:245-255`), but I have
  not seen a signed job pass through one.

## 9. What Salt does

Read in `~/saltstack` at `221495872f` (2026-11-19); `salt/version.py:85`
names 3008 (Argon) as the latest release.

Salt's orchestration runs on the Salt server and publishes each step as an
ordinary job. Its only publication signature (`sign_pub_messages`) is made
with the server's own key (`salt/channel/server.py:1950-1956`) and checked by
the receiver against that key (`salt/channel/client.py:767-780`). That is
option D: it authenticates the server and gives no protection from it. Salt
has no operator-held job signing, so there is nothing to be compatible with.
An existing Salt orchestration runs unchanged without a plan, and with one
it needs only the plan step.

## 10. Open questions for the maintainer

1. **B now, A' later, or A' only?** I recommend B, because it changes
   nothing on the node and needs no new service. A' is the better system if
   orchestrations here will routinely outlast an hour or need resuming hours
   later.
2. **Is an hour enough?** What length are the orchestrations you expect to
   run? If they routinely exceed `job_signature_max_ttl`, B means raising it
   on those nodes for every signed job, which weakens the bound 5.330 set.
3. **Sequence.** Accept that a plan authorises a set and not an order
   (section 1), or invest in ordering? One partial measure: make the drain
   and the deploy one step, so that there is no order to subvert.
4. **`parallel` in the signature (v3)?** Leave it ignored on signed jobs, as
   now, or plan a v3 encoding that covers it, with the rollout order that
   forces (3.10)?
5. **Hub configuration values in orchestration templates.** Should the plan
   tool take `--hub-config`, or should a planned orchestration be forbidden
   to read hub configuration at all? The second is simpler and keeps the
   operator's compile independent of the hub's settings.
6. **The interrupted-run record (2.5).** Fix it first, in its own change, by
   recording the run after every step? It is a defect with or without
   signatures, if the reading is right, and it makes resume after a restart
   re-run steps that already happened.
7. **Bind the plan to issue 764's signed tree?** A plan could record the
   manifest digest of the checkout it was compiled from, so that "the
   operator compiled the signed revision" is checkable afterwards.
8. **Priority.** The estate has no orchestration today (2.8). Is 765 worth
   building before 764, which every highstate would use?
9. **5.169's live-signer sentence (2.9).** It reads as though a KMS signer
   called by the hub keeps authority off the hub. Add a ledger subsection
   correcting it, as the ledger's convention is, rather than editing it?

## 11. What I did not verify

- **Nothing here has been run.** Every "today" is a reading of `d0d5925c`.
- That a resume of a run interrupted by a hub restart re-runs earlier steps
  (2.5). Read from `orch.go:144`, `:159`, `:176` and `:341-357`, not run.
- That an off-hub compile can reproduce the hub's for real orchestrations.
  The compiler is shared code, but the inputs (configuration values, the
  tree as gitfs materialises it) may differ, and nothing has compared them.
- That `state.orchestrate` refuses an unknown keyword argument, which
  decides whether an old hub given `--plan` fails loudly (section 6).
- That a signed job passes through a relay intact (section 8).
- That an hour is enough for a real orchestration (3.6). Nobody has measured
  one here, and the estate has none.
- The claim in 2.9 about KMS key policies is general knowledge of AWS KMS,
  not something I checked against the key `halite-ext-signer-kms` uses.
- Salt's behaviour was read in source, not run.
