# Hardening: signing, the sandbox and evidence

<!-- abstract: The controls that limit what a compromised part can do: signed jobs, the render sandbox, unprivileged accounts, signed trees, the node's evidence chain, FIPS, and a checklist. -->
<!-- kind: guide -->
<!-- index: hardening; security model; threat model; job signing; require_job_signature; job_signer_keys; keys signer create; --sign-key; --sign-extension; render sandbox; evidence; verify-evidence; evidence anchors; hash chain; FIPS; gitfs_verify_signatures; least privilege; file modes; exec_path; clean environment; secrets in logs; redaction; checklist -->

Part V so far has been the model that keeps strangers out and keeps
authorized people inside their limits. This chapter is about the controls
for when something has already gone wrong: a node is compromised, the hub
is compromised, a tree is malicious. It states what each control
establishes and, just as importantly, what it does not.

## The threats, and what answers them

| Who or what has been compromised | What an attacker has | The answers |
|---|---|---|
| **A network attacker** | The ability to send bytes to the hub's port | TLS 1.3 mutual authentication terminated by the standard library; one port; no application parsing before authentication; a health check that returns a constant |
| **One node** | That node's key, its grains, its pillar | Hub-side targeting (it sees only its own jobs); trusted-grain allowlist for pillar; events confined to its own prefix; deny-by-default mine access; short-lived, revocable certificate; evidence chain anchored at the hub |
| **The hub** | The hub's process and files, including its CA key | Optional **detached job signing** with a key the hub does not hold; node-side expiry and replay caches; signed and pinned extensions; signed git refs; node-local evidence the hub cannot rewrite |
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

- **Orchestration steps are not signed.** The steps of an orchestration are
  jobs the hub itself dispatches, and the ledger records that this build does
  not sign them. On a node that requires signatures for a function class, try
  an orchestration that touches it on a spare node before you rely on it.
- **Agentless mode does not check a signature.** `halite-hub ssh` runs the
  binary over SSH, so the caller already has a shell there; a signature
  check would establish nothing.

## The render sandbox

All the YAML parsing and template rendering on a node, the largest and most
attacker-adjacent code in the system, happens in a **child process that
runs as an unprivileged account** with no network. The node runs as root
because package and service management need it; the code that reads
untrusted text does not. The sandbox returns *data*, never a callable and
never a command to run without validation: the compiled low state is checked
against the module signatures before anything executes.

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
status. The `fips_mode` grain is a pair, the host kernel's state *and* the
binary's, and a mismatch is a `doctor` warning. The FIPS set is Linux only
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
- The **evidence chain** is a node-local record anchored at the hub:
  `verify-evidence`, then `--anchors`.
- Secrets are **masked at the sink**, kept out of command lines and
  environments, and given explicit file modes.

Next: [extensions](29-extensions.md).
