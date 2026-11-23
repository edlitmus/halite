# Hardening: signing, the sandbox and evidence

<!-- abstract: The controls that limit what a compromised part can do: signed jobs, the render sandbox, unprivileged accounts, signed trees, the node's evidence chain, FIPS, and a checklist. -->
<!-- kind: guide -->
<!-- index: hardening; security model; threat model; job signing; require_job_signature; job_signer_keys; keys signer create; --sign-key; --sign-extension; render sandbox; evidence; verify-evidence; evidence anchors; hash chain; FIPS; gitfs_verify_signatures; least privilege; file modes; exec_path; clean environment; secrets in logs; redaction; checklist; signed state content; require_state_signature; state_signer_keys; state_manifest_max_age; state_signature_local; halite-hub tree; _halite; tree manifest; high-water mark -->

Part V so far has been the model that keeps strangers out and keeps
authorized people inside their limits. This chapter is about the controls
for when something has already gone wrong: a node is compromised, the hub
is compromised, a tree is malicious. It states what each control
establishes and, just as importantly, what it does not.

## The threats, and what answers them

| Who or what has been compromised | What an attacker has | The answers |
|---|---|---|
| **A network attacker** | The ability to send bytes to the hub's port | TLS 1.3 in the standard library; one port; every endpoint refuses a caller without a certificate except two: a health check that returns the hub's version, and enrollment, which reads a bounded request and at most leaves a pending request for you to decide |
| **One node** | That node's key, its grains, its pillar | Hub-side targeting (it sees only its own jobs); trusted-grain allowlist for pillar; events confined to its own prefix; deny-by-default mine access; short-lived, revocable certificate; evidence chain anchored at the hub |
| **The hub** | The hub's process and files, including its CA key | Optional **detached job signing** with a key the hub does not hold; node-side expiry and replay caches; signed and pinned extensions; node-local evidence the hub cannot rewrite. Signed git refs are not on this row: the hub checks them, so a hub that has been taken over need not. A node that checks the state tree itself is issue 764, not yet built |
| **A state tree** (someone who can push to it) | The ability to put states, templates and files in front of every node | Signature verification on git refs; strict undefined names; render sandbox and limits; no YAML object construction; `cmd.run` as a distinct permission |
| **An insider with an operator credential** | Whatever that credential's policy allows | Deny-by-default policy with target, function and argument limits; arbitrary-code functions never wildcarded; every decision logged |
| **A local unprivileged user on a node** | A shell | Modes on every path; pillar not written to disk in plaintext; no secret in a command line or a child's environment |
| **A supplier** | The ability to ship a bad library | One third-party dependency, vendored and pinned; reproducible builds; a software bill of materials; signed provenance |

The rest of this chapter takes the less obvious controls in turn.

## Signing jobs

By default a node runs any job the hub sends it that passes the checks of
[chapter 12](12-hub-and-nodes.md). So whoever controls the hub controls
every node. For a regulated estate, or just for defence in depth, **signed
jobs** break that equivalence: a node can be told to run only jobs signed by
a key the **hub does not hold**. A hub that has been taken over can then
dispatch nothing that node will act on.

```sh
# on a machine that is NOT the hub
halite-hub keys signer create ops --out signer/ops
```

```
signer    ops
key       signer/ops.key
public    signer/ops.pub

Put this line in job_signer_keys on every node that should accept it:

  ops MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEHgSQtrImk8pEquJUGa2s…

Sign with `halite-hub run --sign-key signer/ops.key`, and keep the private
half off the hub: a signature the hub can produce protects nobody from a hub.
```

On each node that should enforce it, in `node.yaml`:

```yaml
require_job_signature: true
job_signer_keys:
  - 'ops MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEHgSQtrImk8pEquJUGa2s…'
```

Run in the lab, against a node that requires signatures:

```
$ halite-hub run db1.lab test.ping
db1.lab:
refused: this node requires a signature for test.ping (every job): the job carries no signature

$ halite-hub run db1.lab test.ping --sign-key signer/ops.key
db1.lab:
True

$ halite-hub run db1.lab test.ping --sign-key signer/ops2.key       # a key the node does not trust
db1.lab:
refused: this node requires a signature for test.ping (every job): the signature does not verify
against any of this node's 1 trusted signer key(s)
```

What is signed, so that the signature says *where* as well as *what*: the
job ID, the target and its kind, the function, every argument in order,
the keyword arguments sorted by name and each written with its type (so
`true` and `"true"` sign differently, and a hub cannot turn a signed dry
run into a real one), the environment, and the expiry. A node refuses a
signed job with no expiry, or one further off than `job_signature_max_ttl`
(an hour by default) when it arrives.
A node also checks that **it matches the signed target**: sign by node ID,
glob, grain, pillar, list, regex or CIDR (anything a node can evaluate about
itself). A nodegroup is resolved from the hub's configuration, which a node
cannot check, so a node refuses a signed job targeted that way.

The signature is ECDSA over a SHA-256 digest of a defined byte string, so
any signer can produce one: a CI job, a hardware token, a KMS. The
`--sign-extension` flag asks a **signer extension** for the signature instead
of reading a key file, and `halite-ext-signer-kms` does it with a key that
never leaves AWS KMS: neither the hub nor the machine running the command
holds anything that can sign, and every signature is a cloud audit record.

The requirement can be `true` for every job, `false` for none, or a list of
function classes: `arbitrary_code`, `state`, `mutating`. The recommended
setting is `[arbitrary_code, state]`: require a signature for anything that
can run code and for state application, and leave read-only inspection
(`test.ping`, `grains.items`) unsigned so that ordinary operation does not
need a signing step. A setting the node cannot parse **stops the node at
start**: one that quietly required nothing would be worse than one that
required nothing loudly.

Two limits to know:

- **An orchestration needs a plan.** Its steps are jobs the hub sends, so
  on a node that requires signatures they run only from a plan signed off
  the hub: see [Signing an orchestration](#signing-an-orchestration).
- **Agentless mode does not check a signature.** `halite-hub ssh` runs the
  binary over SSH, so the caller already has a shell there; a signature
  check would establish nothing.

## Signing an orchestration

An orchestration's steps are jobs the hub makes as it runs, and a hub that
signed them would be a hub that can sign anything. So you sign them before
the run: `halite-hub orch plan` compiles the orchestration on your machine,
from your checkout, with the hub's compiler, and signs every step that
sends a job with the same key or KMS signer `run` uses. The hub checks the
plan against its own compile and sends the signed jobs. Nodes need nothing
new: each step is an ordinary signed job, and a node verifies it as one.

What a plan does **not** protect:

- **Order.** A plan authorises a set of jobs. A hub that has been taken
  over can send them in another order, skip the drain before a deploy,
  send the rollback when nothing failed, or send nothing. Every job it
  sends is still exactly the signed one, to the nodes its target names.
  Where order matters, make the dependent actions one step.
- **Batch, subset, safe limit, `tolerate_failures` and `run_parallel`.**
  These are outside the signature, so they are controls an honest hub
  honours. `subset: 1` is not a signed canary: every node the target
  matches accepts the job. To sign a canary, target it by name. A node
  ignores `run_parallel` on a signed job and runs it alone unless its own
  `parallel_jobs` says otherwise.
- **Steps on the hub.** `salt.runner`, `salt.wheel` and
  `salt.wait_for_event` run on the hub and are authorised by its policy.
  The plan lists them so an honest hub notices a change; a compromised one
  runs what it likes.

**The expiry budget.** All the jobs of a plan are signed at one moment and
expire together, an hour later by default, and a node refuses a signed job
whose expiry is further off than its `job_signature_max_ttl` (an hour by
default). So the whole orchestration, every batch and every wait, must
finish inside the window, and `orch plan` refuses a `--ttl` longer than
`--max-ttl`. For a longer one, split it into plans signed one after
another, or raise `job_signature_max_ttl` on the nodes it runs on and pass
the same value as `--max-ttl`. Raising it lengthens the life of **every**
signed job those nodes accept, not only the orchestration's.

A plan records the revision of a signed tree ([below](#signing-the-state-content))
when your checkout carries one and matches it, and a hub serving another
revision refuses the plan. A planned orchestration cannot read the hub's
configuration (`opts`): the plan tool has none to give it.

### Runbook: an orchestration on nodes that require signatures

1. **Check out the tree the hub serves**, at the revision it serves. If
   the tree is signed, `halite-hub tree verify <dir> --env base --keys
   <file>` first.
2. **Plan and sign it** on a machine that is not the hub:

   ```sh
   halite-hub orch plan deploy.web --tree ~/estate/state --pillar '{"version":"1.4.2"}' \
       --ttl 45m --sign-extension ./halite-ext-signer-kms --out deploy.plan.json
   ```

   A refusal here names the step: a template that reads `opts`, a nodegroup
   target, `retry` on a step, or a `--ttl` over `--max-ttl`.
3. **Read it**: `halite-hub orch plan show deploy.plan.json` prints every
   signed job, its target, arguments and expiry, and the hub steps.
4. **Rehearse it**, if you want a dry run on the nodes: plan again with
   `--test` and run that plan with `orch run --test`. A test plan does not
   run for real, and a real plan does not run as a test.
5. **Run it** with the same SLS, environment and `--pillar`:

   ```sh
   halite-hub orch run deploy.web --pillar '{"version":"1.4.2"}' --plan deploy.plan.json
   ```

   "refused before it started: step X is signed for target ... and compiles
   to ..." means the hub's tree or your arguments moved after you planned.
   Pull, plan again and sign again; nothing was sent. "the plan expired at"
   means the same. The hub logs the refusal at warn with the step and the
   field, and counts it in `halite_orch_runs_total{result="plan_refused"}`.
6. **If a step fails**, fix the cause and plan the resume: a resume always
   needs a new plan.

   ```sh
   halite-hub orch plan deploy.web --tree ~/estate/state --pillar '{"version":"1.4.2"}' \
       --resume-of 20261122T181000123456 --from verify_web \
       --sign-extension ./halite-ext-signer-kms --out resume.plan.json
   halite-hub orch resume 20261122T181000123456 --from verify_web \
       --pillar '{"version":"1.4.2"}' --plan resume.plan.json
   ```

   The resume carries forward the steps the earlier run finished, and a
   step that was out when its hub stopped once every node has answered its
   job; neither needs signing. A step that was out and is still short of a
   node refuses the resume, naming the job: wait for it, or resume (and
   plan) from that step to run it again. A refusal naming a step before
   `--from` as not in the plan means the earlier run did not reach it, so
   the resume would run it; if it should, plan from it instead.

## The render sandbox

All the YAML parsing and template rendering on a node is the largest and
most attacker-adjacent code in the system, and none of it needs privilege.
With `render_sandbox: true` it runs in a **child process**, which a node
running as root drops to `render_sandbox_user`. The sandbox returns *data*,
never a callable and never a command to run without validation: the compiled
low state is checked against the module signatures before anything executes.
Module dispatch, template loading and gpg decryption stay in the node.

It is **off by default**, and what it can apply depends on the platform:

| Control | Linux | FreeBSD, macOS, other unix | Windows |
|---|---|---|---|
| Process boundary | yes | yes | yes |
| Unprivileged account (`identity`) | when the node is root and `render_sandbox_user` is set | when the node is root and `render_sandbox_user` is set | no |
| No network (`network`) | when the node is root: a network namespace of its own, loopback down | no | no |
| Filesystem restriction | no: the child reads what its account can read | no | no |

To make it a requirement rather than a hope, list the controls in
`render_sandbox_require`:

```yaml
render_sandbox: true
render_sandbox_user: halite-render
render_sandbox_require: [identity, network]   # on FreeBSD: [identity]
```

Each is checked against what the child reports about itself once it has
started, not against the settings. A requirement that does not hold refuses
**every render** with the reason; the node still starts and takes jobs, so
it can be fixed from the hub. It says so at startup, at error, and
`halite-node doctor` reports the child's effective uid, the interfaces it
can see and the filesystem restriction under **render isolation**.

Related limits are enforced on every render regardless of sandbox:
output at most 64 MiB, ten million loop iterations, recursion depth 100,
include depth 25, sixty seconds of wall clock
([chapter 7](07-templates.md)).

The same discipline applies to the hub: templates it renders (pillar,
reactions, orchestration) get a **restricted function set** with no `cmd.*`,
no file writes and no `module.run`, because a pillar template that could
run a command would turn "can edit the pillar tree" into "root on the hub".

Child processes a node starts (`cmd`, extensions) receive a **clean
environment**: no hub credentials, no pillar values, and an explicit `PATH`.
If a state finds its binary by hand and breaks under the service, the
difference is usually the search path, which `exec_path` in `node.yaml` pins
(it replaces rather than extends).

## Trusting the tree

A malicious or compromised state tree is a way to run code on every node, so
the file server has controls of its own:

- **Signed refs.** With `gitfs_verify_signatures: true` and a
  `gitfs_keyring`, a ref whose tip commit is not signed by a trusted key is
  **not served**, and the failure is an event. It turns the file server from a
  code-delivery path into a *signed* code-delivery path
  ([chapter 10](10-top-and-files.md)).
- **Containment.** Path traversal out of a root is checked after symlink
  resolution, on every request, with a fuzz test behind it.
- **Strict undefined and no object construction in YAML**, so a typo cannot
  silently write a bad value and a document cannot construct code.
- **A separate permission for `cmd.run`** and the other arbitrary-code
  functions ([chapter 26](26-authorization.md)), because anything that can
  write into the served tree writes code that nodes will fetch and run.

## Signing the state content

Signed git refs are checked **by the hub**. Against a hub that has been
taken over, that is the attacker's own check, and the hash a node checks
each file against is one the hub sends with the file. A node that must not
trust its hub that far sets `require_state_signature`. Then it applies a
tree only as a signer that the hub holds no key for published it.

How it works:

- A signer off the hub, usually CI, lists an environment's tree into
  `_halite/manifest.json`. The manifest names every file with its digest,
  size and mode, the environment, and the time it was issued. The signer
  signs those exact bytes into `_halite/manifest.sig` with an ECDSA key.
  The hub serves the two files like any others.
- On its first read of an environment in a run, the node fetches the two
  files and checks them against `state_signer_keys`. It refuses the
  manifest if the signature does not verify, if it was signed for another
  environment, if it has expired or is older than `state_manifest_max_age`,
  or if it is older than the newest manifest the node has already accepted
  for that environment. That last rule stops a rollback. The node records
  the accepted manifest, synced to disk, before it reads any file.
- For the rest of the run, the manifest answers every read and every
  listing. A path that it does not name is not found, even if the hub
  serves it. A file whose content differs from the manifest is refused,
  whatever hash the hub sent with it.

On each node, in `node.yaml`, start with `warn`:

```yaml
require_state_signature: warn        # then true
state_signer_keys:
  - 'tree-ci MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE…'
```

`warn` checks everything and refuses nothing. It logs each refusal that
`true` would make and counts it in `halite_node_tree_refusals_total`. When
that count stays at zero over a week of real highstates, set `true`. A value
the node cannot parse stops it at startup. An empty `state_signer_keys` does
not: the node starts and refuses every state run, and says why.
[Chapter 33](33-configuration.md#7-sign-the-state-tree) has the procedure
for CI and for rotating the key.

The signing commands run off the hub, against the tree as the hub serves it:

```sh
halite-hub tree manifest "$TREE" --env base --source "commit $COMMIT"
halite-hub tree sign "$TREE" --sign-key tree-ci.key   # or --sign-extension ./halite-ext-signer-kms
halite-hub tree verify "$TREE" --env base --keys trusted-tree-keys
```

Run in the lab, with a node that requires signatures, a hub that changes a
file after it was signed is refused like this. The line is from the node's
log, shortened:

```
signed state content: refused  env=base reason=digest path=app/marker.txt
  base/app/marker.txt arrived with sha256 886b6748… (the hub published sha256:886b6748…)
  and revision sha256:8b6e9c73… (issued 2026-11-23T00:03:00.143923216Z, signed by tree-ci)
  says 2d27fbdf…; it was not used
```

and a hub that puts back an older, validly signed revision:

```
signed state content: refused  env=base reason=rollback
  the manifest for base was issued 2026-11-23T00:03:00.143923216Z and this node has already
  accepted one issued 2026-11-23T00:03:05.413218759Z; refusing an older revision
```

What it does not cover, and you should not infer:

- **Pillar.** The hub compiles pillar, per node, and a node never sees its
  sources. A signed state that puts a pillar value into a command, a file
  or a package name is still steered by a compromised hub through that
  value. Treat pillar as untrusted input in a tree you sign.
- **Which job runs, and when.** That is [signed jobs](#signing-jobs). With
  only signed content, a compromised hub can still choose when a signed
  tree is applied, and to which environment.
- **A freeze, beyond the bound.** A compromised hub can keep serving the
  newest manifest a node has accepted and withhold the next one. The node
  cannot tell that from a quiet week. `state_manifest_max_age`, thirty days
  by default, turns a long freeze into a refusal, so a tree must be signed
  again at least that often. The same bound means that **a hub outage longer
  than thirty days stops state runs** on every node that requires
  signatures. The node logs that the manifest is too old and names the
  setting, and `halite-node doctor` fails its `state signatures` check. Set
  `0` where a long outage matters more than a freeze.
- **Agentless mode.** `halite-hub ssh` builds its tree from what the caller
  sent, for the reason it does not check job signatures.

Each environment has its own manifest and its own high-water mark, and a
highstate that reads several environments' top files checks each one.
`state_signature_local`, on by default, holds the node's own `file_roots` to
the same rule. Without it, a file server that does not answer at startup, or
`--local`, would be a way round the requirement. The local tree then needs
its own signed `_halite/`.

## The evidence chain

Each node keeps an **append-only, hash-chained** record under
`<state_dir>/evidence` of every job it accepted, every job it **refused**,
the outcome of each, every extension bundle that changed, and the
configuration in effect each time the agent started. It is on by default.
Each record carries the hash of the one before it:

```json
{"schema":"halite.evidence/1","seq":50,"ts":"2026-10-11T18:59:48.149058Z",
 "kind":"node.start","node_id":"db1.lab","detail":{"version":"0.1.0-7-g8ad761ce275b+8ad761ce275b"},
 "prev":"sha256:12ce6298…","hash":"sha256:…"}
```

Check it on the node:

```sh
halite-node verify-evidence
```

```
halite-node verify-evidence — db1.lab

directory: …/db1/state/evidence
segments:  1
records:   254
from:      record 1 at 2026-10-11T18:52:40.836284Z
to:        record 254 at 2026-10-11T19:23:33.375991Z
head:      sha256:13fc3f9ed400c6813da24acf4ea0f43171dc11049c001fe704ae4b9eb7082f7e
receipts:  88 checked against …/pki/ca.crt

The chain holds: every record's contents match its hash and every record
follows the one before it.
```

and tamper with a **copy** of the log (one field of record 50 changed) to
see it fail:

```
1 break(s):
  evidence.jsonl line 50 (record 50): the contents of this record do not match its hash, so it has been altered
```

with exit status `1`. But a hash chain detects *editing*, not *rewriting*:
anything with root on the node can recompute the chain from its first record.
That is what the **anchor** is for. After every job (and when its stream
opens), the node reports the head of its chain to the hub, which records it
and answers with a **receipt**: a signature, by the enrolment CA, over the
node's identity, the head and the time. The node files the receipt in its own
chain. A head that contradicts the hub's record (the same number with a
different hash, or a number below the highest) is answered `409` with no
receipt, counted, and raised as `halite/node/<id>/evidence/conflict`.

```sh
halite-hub evidence anchors db1.lab > anchors.json
halite-node verify-evidence --anchors anchors.json
```

```
The chain holds: every record's contents match its hash and every record
follows the one before it.

It also agrees with the hub: every head the hub accepted from this node is
still the record at that number. What that does not establish: records
written after the last head the hub accepted are not anchored, and a node
and hub compromised together can agree on anything.
```

What this establishes, and what it does not, in the project's own words: a
compromised node cannot rewrite anything up to its last accepted report
without contradicting the hub's record, and a compromised hub cannot deny an
anchor without contradicting a receipt it signed. It does **not** protect
records written after the last report the hub accepted, and it does not
cover a node and hub compromised together. The conflict check at the hub is a
prompt and not the guarantee; the guarantee is the comparison
`verify-evidence --anchors` makes. Run it as part of an investigation, and
on a schedule if your obligations require it. `halite-node doctor` runs the
chain check too.

## FIPS builds

For estates under FIPS 140-3, a parallel set of artifacts, suffixed
`-fips`, is built so that all cryptography routes through the Go
Cryptographic Module, and runs with FIPS mode enforced. In that mode TLS is
restricted to approved suites and P-256/P-384 key exchange, Ed25519 is
unavailable, and the TOTP second factor (which needs SHA-1) is disabled.
`halite-node version` on a FIPS build reports the module and self-test
status. The `fips_mode` grain is the host kernel's state alone;
`fips_build`, `fips_enabled` and `fips_module` describe the binary. A mismatch
between the two is a `doctor` warning. The FIPS set is Linux only
(amd64 and arm64) for now; `make install-fips` installs it beside the ordinary
binaries, and the systemd drop-ins that enforce the mode are installed
separately because they change what a unit runs.

Each release publishes the set as its own archives,
`halite-<version>-linux-amd64-fips.tar.gz` and `…-linux-arm64-fips.tar.gz`,
for releases after v0.1.0 (which has none). Inside, `bin/` holds
`halite-hub-fips`, `halite-node-fips` and `halite-api-fips`, and
`service/systemd/fips/` holds the drop-ins. They are separate archives and not
extra files in the ordinary ones so that nobody gets a FIPS binary by copying
the wrong file. Ask the binary, not its name:
`halite-node-fips version` must report the module and `self-tests passed`.

> **What has and has not been run.** The three binaries from the published
> v0.1.1 `linux-amd64-fips` archive were run on Rocky Linux 9.8 (kernel
> 5.14.0-687.54.1) with the kernel in FIPS mode (`/proc/sys/crypto/fips_enabled`
> 1, crypto policy `FIPS`, SELinux enforcing), after `fips-mode-setup --enable`
> and a reboot. Each reported `fips mode on, module v1.0.0, self-tests passed`;
> `fips_mode` read `False` before the reboot and `True` after; `doctor` on the
> hub and on the node passed its FIPS row (`the kernel is in FIPS mode and this
> is a FIPS build`); and the ordinary `halite-node` on the same host got `warn
> ... the kernel is in FIPS mode and this is not a FIPS build`. A hub and a node
> ran under the shipped base units and `service/systemd/fips/` drop-ins (the
> processes' environment held `GODEBUG=fips140=on` and their executables were the
> `-fips` files), the node enrolled, answered `test.ping` and applied a state.
> **Not run:** a node on another host than the hub, on Rocky (that run had
> both on one host), and an upgrade. **A trap for whoever repeats it on a lab host:**
> after the reboot RHEL 9's `sshd` in FIPS mode refused an ed25519 login key, so
> a host raised with one cannot be reached; use an ECDSA or RSA key.
>
> On 2026-11-05 the published v0.1.1 `linux-arm64-fips` and `linux-amd64-fips`
> archives, and the ordinary archives, were fetched and checked against
> SHA256SUMS on two Ubuntu hosts already in kernel FIPS mode on Ubuntu Pro
> `fips-updates`: `host-c` (arm64, Ubuntu 24.04.5, kernel
> 6.8.0-1067-aws-fips) and `host-b` (amd64, Ubuntu 22.04.5, kernel
> 5.15.0-1117-aws-fips). All six `-fips` binaries reported `fips mode on,
> module v1.0.0, self-tests passed`. The hub (amd64), the API (amd64) and the
> node (arm64) ran as services under the shipped drop-ins, with only the
> `-fips` binaries installed. The node enrolled across the two hosts and
> answered `test.ping`, `grains.item` and a `state.apply`; the API logged in an
> account and ran `test.ping` through to the node. `doctor` showed all three
> branches of its FIPS row: pass for a FIPS build, warn for an ordinary build
> on a FIPS kernel, and warn for a FIPS build run with `GODEBUG=fips140=off`.
> **Not run in that pass:** `halite-hub-fips` and `halite-api-fips` on arm64
> (`version` only), the ordinary and `-fips` builds on a kernel that was not in
> FIPS mode (so `fips_mode: False` was not seen), Ubuntu Pro's certified `fips`
> kernel, a second node, token enrolment, TOTP, an upgrade, and any release
> after v0.1.1. The ledger entry is DIVERGENCE 5.282.
>
> On 2026-11-06 main (0.1.1-329-g5d314a40a398) was built from source on two
> arm64 Ubuntu 24.04.5 hosts, both on kernel 6.8.0-1067-aws-fips. The hub, the
> API and the node ran as services under the drop-ins, all on arm64. The
> `-fips` binaries were the only ones installed. A node enrolled with a token,
> with no `keys accept`. `grains.item` with six names returned all six. A third
> arm64 host, on Ubuntu 22.04.5 with a kernel that is not in FIPS mode, gave
> `fips_mode: False` for both builds. On that host `doctor` warned for the FIPS
> build and passed for the ordinary build, so all four branches of its FIPS row
> have been seen. The ledger entry is the same, DIVERGENCE 5.282.
> **Not run then:** main on amd64, Ubuntu Pro's certified `fips` kernel, a
> reboot or a move of a host into or out of FIPS mode, start at boot, TOTP, an
> upgrade, and a release archive of main.
>
> On 2026-11-23 main (`33dc8e21`) ran on amd64 in RHEL's FIPS mode. The host
> was Rocky Linux 9.8, built with `make build fips` and installed with `make
> install-fips`. The hub, the node and the API all ran under the drop-ins, and
> they started again after a reboot. On the same day, on three arm64 Ubuntu
> 22.04.5 hosts on Ubuntu Pro `fips-updates` (kernel 5.15.0-1121-aws-fips),
> the published **v0.2.0** `linux-arm64-fips` archive was checked against
> SHA256SUMS and its signature. It then ran as a hub, an API and a
> token-enrolled node on two hosts. Next, `make fips` built main (`5f5ae87c`)
> on Ubuntu. The hub, the API and the node were **upgraded** to it by hand, in
> the order of [chapter 36](36-upgrades.md), with the node's changed unit
> file. Both hosts were then **rebooted**, and every unit came back on its
> `-fips` binary with the node `up`. **TOTP under FIPS on Linux**: an account
> with `totp` was named at the API's start, and a correct code got `401`. The
> same code and binary with `GODEBUG=fips140=off` logged in. **Ubuntu Pro's
> certified `fips` kernel could not be tried**, because Pro refuses `fips` on
> any host that has ever had `fips-updates` (`fips-updates-invalidates-fips`).
> Both runs are in DIVERGENCE 5.282.
>
> **Ubuntu Pro's certified `fips` kernel exists only for Ubuntu 20.04 and
> older.** On 2026-11-23 Pro's certified pocket listed `xenial`, `bionic` and
> `focal`. 22.04 and 24.04 have FIPS only through `fips-updates`. A fresh
> Ubuntu 20.04 amd64 host was attached with `pro enable fips`. main
> (`0f61199d`) was built with `make fips`, installed with `make install
> install-fips`, and run as a hub, a node and an API **before** the reboot
> into FIPS mode. On the stock kernel `fips_mode` was False, and each `doctor`
> warned. After the reboot into kernel 5.4.0-1021-aws-fips, every unit came
> back on its `-fips` binary with no action. `fips_mode` read True, and each
> `doctor` passed. Jobs and the API worked. On that host's systemd 245, the
> hub's and the API's units logged that `ProtectProc=` is unknown and ignored
> it.
>
> **There is no certified kernel for arm64.** The certified pocket publishes
> i386, amd64, s390x and (for 16.04) ppc64el. On arm64, FIPS comes only
> through `fips-updates` or the uncertified `fips-preview`. A fresh Ubuntu
> 22.04 arm64 host was therefore given `pro enable fips-updates`. main
> (`17c55da3`) was built and installed the same way, and it ran on the stock
> 6.8 kernel. Then the host was rebooted into 5.15.0-1121-aws-fips. The
> results matched the amd64 host. The units came back with no action, and
> `fips_mode` changed from False to True. Each `doctor` changed from warn to
> pass.
>
> On a third arm64 host, already in FIPS mode, a node from the v0.2.0 archive
> was **upgraded by a state** to main. The node was under a main hub. A
> `file.managed` of `/usr/local/bin/halite-node-fips` from the hub's tree and
> a `systemd-run --on-active=30` restart (as in [chapter 36](36-upgrades.md))
> returned `changed=2` at once. The agent restarted on the new binary 69
> seconds later, and `manage.versions` showed nothing mismatched. Then the
> host was **moved out of FIPS mode** with halite running (`fips=0`, a
> reboot). The units came back on their `-fips` binaries, which still report
> their own FIPS mode. `fips_mode` changed from True to False, and each
> `doctor` changed from pass to warn. `pro status` reported `fips-updates` as
> disabled too. The questions these runs left for a person are issue 840.

## Secrets: where they are and are not

- **In pillar, encrypted or fetched** ([chapter 9](09-pillar.md)): on the hub,
  in memory for one compilation, never in the pillar cache in plaintext.
- **Masked on output.** A value-based redactor is seeded with every
  decrypted pillar value, every token and every configured secret, and
  scrubs them from logs, events and error messages *at the sink*, so a value
  cannot escape through a path that forgot to redact.
- **Not in command lines or child environments.** Passwords are read from
  standard input or files, never arguments; children get a clean environment.
- **Not in the process table, not on disk by accident.** The file mode of
  everything halite writes is explicit and never taken from the umask;
  private keys are `0600`.

## A checklist

Before a deployment is trusted with production:

**Identity**
- [ ] `hub_fingerprint` is set on every node by a route independent of the network.
- [ ] The CA key (`ca.key`) is backed up encrypted and readable only by the hub's account.
- [ ] You know the CA's expiry date, and have a calendar entry for it.
- [ ] Enrolment is `manual`, or `token` with short TTLs and scopes.
- [ ] Decommissioning includes `keys revoke`.

**Authorization**
- [ ] The starting `--admin` policy has been replaced.
- [ ] Every human role is narrower than "everything"; `cmd.run` is named only where needed.
- [ ] Every reactor has its own principal and narrow role.
- [ ] A `policy test` script is in version control and run on every change.
- [ ] The API's role is no wider than its operators' union.

**Data**
- [ ] Secrets are in encrypted pillar or a secrets manager, none in states or `files/`.
- [ ] `pillar_trusted_grains` is the default or a reviewed list.
- [ ] The tree is served from git with `gitfs_verify_signatures: true`.

**Containment**
- [ ] Hub and API run as unprivileged accounts; the hub's `state_dir` is not under a file root.
- [ ] `require_job_signature` is set for `arbitrary_code` and `state` on sensitive nodes, with signing keys off the hub.
- [ ] Extensions are signed and pinned ([chapter 29](29-extensions.md)).
- [ ] `require_state_signature: true` on sensitive nodes, with the tree signed in CI by a key off the hub, re-signed well inside `state_manifest_max_age`, and an alert on `halite_node_tree_revision_issued_timestamp_seconds`.

**Observation**
- [ ] `halite-hub event listen --tag 'halite/key/**,halite/reactor/**,halite/auth*'` (or an event returner) feeds your alerting.
- [ ] `doctor` runs from monitoring on every host.
- [ ] Metrics are scraped and the alerts of [chapter 34](34-monitoring.md) exist.
- [ ] `verify-evidence --anchors` is part of your investigation procedure.

## Key ideas

- Think in **compromises**: of a node, of the hub, of the tree, of an
  insider. Each has an answer, and each answer states what it does *not*
  cover.
- **Signed jobs** make hub compromise insufficient for fleet compromise;
  keep signing keys off the hub, require signatures for `arbitrary_code` and
  `state`.
- Untrusted text is parsed in an **unprivileged sandbox**; the hub's
  template environment is deliberately small.
- **Sign the tree** (gitfs) and keep `cmd.run` a separate permission.
- **Signed state content** makes a node refuse a tree its hub altered,
  added to or rolled back. The signer is off the hub, the keys are the
  node's, and **pillar is outside it**.
- The **evidence chain** is a node-local record anchored at the hub:
  `verify-evidence`, then `--anchors`.
- Secrets are **masked at the sink**, kept out of command lines and
  environments, and given explicit file modes.

Next: [extensions](29-extensions.md).
