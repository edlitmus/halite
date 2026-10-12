# Runbook: incident playbooks

<!-- abstract: What to do, in order, when a node, a credential, a rollout, a reactor or the hub itself goes wrong: contain first, then investigate, then recover. -->
<!-- kind: runbook -->
<!-- index: incident; incident response; compromised node; compromised credential; leaked token; leaked secret; bad rollout; runaway reactor; hub down; hub compromised; CA compromise; malicious commit; disk full; keys revoke; jobs kill; token revoke; verify-evidence; containment; postmortem -->

**Use this runbook when** something has gone wrong and you are the person who
has to act. It is organised by what you are looking at. Each playbook follows
the same order, on purpose:

1. **Contain**: stop it getting worse, even before you understand it. Prefer
   actions that are fast, safe and reversible.
2. **Investigate**: what happened, to what, and since when. halite keeps
   records for exactly this moment.
3. **Recover**: return to a known good state.
4. **Afterwards**: close the hole and write it down.

**You will end with** the incident contained, an account of it from the
system's own records, and a list of what to change so it does not recur.

## Before you start

Have these in place *before* the incident, because there is no time to set
them up during one:

- Event shipping to a place the attacker cannot reach
  ([chapter 23](23-scheduler-and-returners.md): `event_return`), so the record
  of an incident outlives the host it happened on.
- A tested backup of `pki/` and `state/` ([chapter 35](35-backup-and-recovery.md)).
- A known-good copy of the tree in version control, with signed commits.
- A **break-glass** operator certificate, kept offline, bound to a role that
  can do what an incident needs (including named `cmd.run`).
- The operator's cheat sheet: `keys revoke`, `jobs kill`, `jobs active`,
  `event listen --from earliest`, `jobs show`, `policy show`.
- Someone who is not the author of the change reading along.

Throughout, **time is evidence**: note the time you started, and use the JID
(it contains the time) and the event offsets (they are positions in an
append-only log) to anchor what you write down.

## Procedure

Pick the playbook that matches the alarm.

### A. A node you no longer trust

*A machine was compromised, was stolen, was cloned, behaves oddly, or is being
decommissioned.*

**Contain (seconds).** Revoke its enrolment. A revoked node is refused at every
handshake and on every request over an existing connection, and a connected agent
stops itself:

```sh
halite-hub keys revoke web2.lab --reason "suspected compromise, ticket 4411"
```
```
revoked web2.lab
a running hub applies this within a few seconds; the record on disk is the decision
```

On the node, the agent logs `this node's enrollment has been revoked` and exits.
If the node is hostile it will not exit politely; it simply cannot connect, so
nothing it sends reaches the hub and nothing the hub sends reaches it.

Then limit what *it already knew*. It had its own pillar, so **assume those
secrets are exposed** and rotate them (playbook E). It did **not** have other
nodes' pillar, other nodes' certificates, the CA key, or the job targets of
other nodes; targeting is evaluated on the hub, so a node only ever saw jobs
that matched it.

**Investigate.**

```sh
# everything it did and was told, from the hub's side
halite-hub jobs list --limit 200 | grep web2.lab
halite-hub event listen --from earliest --once --tag 'halite/node/web2.lab/**'

# what it claims to have done, from its own side, if you can get at the disk
halite-node verify-evidence                       # on the node, or a copy of its state
halite-hub evidence anchors web2.lab > anchors.json
halite-node verify-evidence --anchors anchors.json
```

`verify-evidence` tells you whether the node's local record was **edited**
(a break) and, with the hub's anchors, whether it was **rewritten**. What it
cannot tell you is anything about activity after the last head the hub accepted.
Read `halite/node/web2.lab/evidence/conflict` events: a conflict is a node whose
chain was rewritten, reset or rolled back since it last reported.

Preserve the disk before you wipe it, if you may need it later.

**Recover.** Rebuild from the tree ([chapter 31](31-install-nodes.md)): fresh
install, `keys delete web2.lab` for the old record, a new enrolment from a new
key. Never re-enrol a compromised machine in place.

**Afterwards.** Rotate what it knew. Ask why the compromise was possible. Add the
alert you wished you had.

### B. A credential has leaked

*A password, an API token, an operator certificate/key, a webhook secret, a
signing key.*

| Credential | Contain | Then |
|---|---|---|
| **API token** | `halite-api token revoke <id>` or `--principal <name>` to withdraw everything an identity holds | Find what it did: `halite-api token show <id>`; the job cache's `on_behalf_of` field |
| **A person's API password** | Remove or lock the account (`accounts.yaml`), `halite-api token revoke --principal alice`, restart the API | Rotate the password; check for `halite_auth_attempts_total{result="accepted"}` from odd sources |
| **LDAP/OIDC identity** | Disable it in the directory or provider; `token revoke --principal` | Review role mappings |
| **An operator certificate** | Remove its binding from `policy.yaml` and restart hub **and** API: the certificate is still valid but is bound to nothing and can do nothing. There is no certificate revocation list for operators; it expires (30 days by default) | Issue a new one under a new name |
| **The API's operator certificate** | Re-issue it (`keys operator create api`), restart the API; narrow the `api-service` role | |
| **A webhook secret** | Change `secret_file`, restart the API | Replays are refused by nonce within the window; rotate and re-sign senders |
| **A job-signing key** | Remove its line from `job_signer_keys` on every node (a state does this) | Make a new signer; sign with it |
| **An extension signing key** | Remove it from `extension_trust_keys` on hub and nodes; re-sign extensions with a new key and update the pins | |
| **The CA key** | See playbook G | |
| **A bootstrap token** | `halite-hub keys token revoke <id>` (keeps the record of what it admitted) | `keys token list` shows whether it was used |

Investigate the same way for every row: **who used it, when, to do what.** The job
cache records the submitter, the target, the function and the arguments for
every job; the event log records enrolment, key and reactor activity; the
hub's `info` log records every authorization decision with the principal and the
rule. Work backward from the leak.

### C. A rollout is going wrong

*A change you applied is breaking machines.* This is the commonest incident.

**Contain.** If a batch is still in flight, stop it from reaching more:

```sh
halite-hub jobs active                  # what is still running
halite-hub jobs kill <jid>              # stop it reaching nodes it has not reached yet
```
```
killed 20261011T193850760475
  told: web2.lab
  a node already running the job finishes what it started
```

`jobs kill` stops a job reaching any node it has not yet reached, removes it from
queues for offline nodes, and tells the nodes that have it. A node **already
applying** a state finishes it, because a state run interrupted halfway leaves a
machine in neither the old condition nor the new; the command says so rather
than implying more. A `--batch-safe-limit` should already have stopped the run
at the Nth failure; the job record shows who was never reached.

For an orchestration, its `onfail` rollback runs by design; check `orch show
<jid>`.

**Investigate.** What changed is a commit; what it did is in the job record:

```sh
halite-hub jobs show <jid>                         # who failed, and who was never reached
halite-hub run '*' state.apply --test --out json | drift-report.sh   # what is now different
```

Run the broken state on one affected node with `--test --log-level debug`: it
reproduces the hub-driven run with full diagnostics
([chapter 39](39-troubleshooting.md)).

**Recover: revert by reverting.** The tree is in version control, and every state
is idempotent and declarative, so the previous version returns each machine to
the previous configuration:

```sh
git revert <the bad commit> && git push          # the hub serves the revert
halite-hub run -N canary state.apply --test      # does it do what you expect?
halite-hub run '<affected>' state.apply --batch 25% --batch-safe-limit 2
```

What a revert cannot undo are side effects in the world (a data migration, a
deleted record, a sent message). Those were the part to guard *before* the
change.

**Afterwards.** Why did the CI check pass? Did the canary exist? Was
`--batch-safe-limit` set? Add the missing test to the `check-tree.sh` of
[chapter 16](16-fleet-operations.md).

### D. A reactor or beacon is running away

*Events pile up on a tag; the hub is busy; `halite/reactor/overflow` or
`throttled` events appear; a node's file or service is being toggled.*

**Contain.** The surest stop is to remove the cause and restart; there is no
reload:

1. Remove (or comment out) the offending `reactor:` entry in `hub.yaml` and
   **restart the hub**. The loop ends with the process.
2. For a beacon, on the node: `halite-hub run <node> beacons.disable_beacon
   name=<beacon>` (it holds that one) or `beacons.disable` (all); changes are in
   memory until saved, so a node restart reverts them unless you also fix
   `node.yaml`.

The hub's causality limit **does not** stop a loop that goes through
`event.send`: in testing, a reaction that sent the tag it matched produced more
than two hundred events in seconds with no loop event
([chapter 20](20-reactors.md)). Do not wait for it.

**Investigate.**

```sh
halite-hub event listen --from earliest --once --tag 'halite/audit/**' | head
halite-hub runner reactor.list
halite-hub metrics --filter halite_reactor
```

Look for a reaction whose output tag matches a reactor pattern, a missing
`onchangeonly`, a state that rewrites a file a beacon watches without
`disable_during_state_run`.

**Recover.** Fix the entry; test it with `reactor.test`; add `dedupe_window` or
`debounce`; restore it. Check the job cache for the damage a runaway
`local.*` reaction did (`jobs list`, `jobs show`).

### E. A secret has been exposed

*A pillar value in a log, a password in git, a token in a ticket.*

1. **Rotate the secret at its source first** (the database, the service). The
   exposure is not undone by removing it from halite.
2. Update pillar (encrypted or in the secrets manager) and apply
   ([chapter 9](09-pillar.md)): `halite-hub run <targets> state.apply` or
   `saltutil.refresh_pillar`, in batches.
3. If it was in **git history**, treat the history as compromised: rotating is
   mandatory, and rewriting history is cosmetic.
4. Find where else it went: logs are redacted at the sink for secrets halite
   knew were secret, which means a secret that arrived any other way (a plain
   pillar value, a `cmd.run` argument, a template) is *not* redacted. Check the
   job cache (`jobs lookup`) and the event stream for the string, and
   `halite/job/<jid>/new` events, which carry arguments
   ([chapter 27](27-http-api.md) records that gap).
5. Afterwards: move the value to encrypted pillar or a secrets manager, and
   stop passing secrets as arguments.

### F. The hub is down or unreachable

*Operators cannot run anything; nodes log connection failures.*

**What still works.** The fleet keeps running whatever it was configured with.
Nodes retry with backoff forever; scheduled jobs in `node.yaml` run on their
clocks; beacons run; `halite-node state apply --local` always works. What
stops: dispatch, new enrolment, reactions, anything needing pillar from the hub
(unless `pillar_cache_disk` is on).

**Diagnose.**

```sh
systemctl status halite-hub          # FreeBSD: service halite_hub status
journalctl -u halite-hub -n 100      # the log names the reason
halite-hub doctor --config /etc/halite/hub.yaml
ss -ltn | grep 4510
df -h /var/lib/halite /var/cache/halite
```

The usual causes: out of disk (job cache, event log, tree), a configuration
error after an edit (`doctor` reports it), a policy or file-root problem, an
expired certificate, the host itself. Fix it, start it, and watch `manage.status`
as nodes return (within seconds). If the hub's data is damaged, restore
([chapter 35](35-backup-and-recovery.md)); do **not** start a fresh CA.

**Disk full.** `job_cache_retention` and `job_cache_max_size`,
`event_retention` and `event_max_size` bound the two big stores; `halite-hub
jobs prune` enforces retention immediately. Lower them if the disk is simply too
small, and look at what else shares the filesystem: the tree and `cache_dir`
must not.

### G. The hub or the CA is compromised

*Someone had the hub's account or its files.* Assume the worst case for
everything the hub could do or knew.

What the attacker could do with **hub access**: dispatch jobs to every node (so
check the job cache for jobs you did not send), read every node's pillar as it
compiled, read the file server, mint operator certificates, accept enrolments.
What they could not do if you had **signed jobs** on (`require_job_signature`
with keys not on the hub): get a node to run an unsigned job.

What they could do with the **CA key**: issue a certificate for any node ID,
impersonating it to the hub, and issue operator certificates.

**Contain.** Take the hub off the network (stop it, or firewall 4510). Nodes keep
running on what they have and retry.

**Investigate** from the nodes' side, which a compromised hub cannot rewrite:

```sh
halite-node verify-evidence            # on a sample of nodes
halite-node verify-evidence --anchors <a-copy-of-the-hub's-anchors-taken-earlier>
```

Each node's evidence chain records every job it accepted, with the principal
and result. Any job in it that you cannot account for is the investigation.

**Recover.** Rebuild the hub on a clean host. If only the host was exposed and
the CA key was not (and you can show it was not, for example because it is in a
KMS), restore state and rotate operator credentials and secrets. **If the CA key
may have been copied, the CA is burnt**: make a new one on a clean hub
(a different fingerprint), re-enrol every node against it
([chapter 35](35-backup-and-recovery.md), the lost-CA procedure, done on
purpose), re-issue every operator certificate, and rotate every secret in
pillar. There is no shortcut, which is why `ca.key` has its own backup and
protection.

### H. A malicious or mistaken commit in the tree

*Someone with write access to the tree pushed something harmful.*

**Contain.** Make the hub stop serving it: revert/reset the served branch, or
point `file_roots` at a known-good directory and restart. If gitfs signature
verification is on, a commit that is not signed by a trusted key is not served
at all; check the log for the refusal. Stop in-flight rollouts (playbook C).

**Investigate.** The history (`git log -p`) is the evidence of *what*; the job
cache is the evidence of *where it ran*: `jobs list` around the commit time,
and `halite-node verify-evidence` on affected nodes for exactly what each
accepted. A malicious state can run commands (`cmd.run`) as root on every node
it targets, so assume that and review the changes for arbitrary execution,
downloads and new users or keys.

**Recover.** Revert on all affected machines, then examine them: a revert
restores declared state and does nothing about a file the malicious change
created outside it. If code ran, treat affected nodes as playbook A.

**Afterwards.** Require signed commits and review for the served branch, and set
`gitfs_verify_signatures: true`.

## Verify

An incident is closed when:

- [ ] The cause is fixed, not only the symptom.
- [ ] Anything that may have been exposed has been rotated.
- [ ] `halite-hub doctor` and a fleet `state.apply --test` are clean.
- [ ] You can state, from records (job IDs, event offsets, commits), what
      happened and when. If you cannot, the record is the first improvement.
- [ ] A short, blameless written account exists: timeline, impact, what was
      detected and how, what helped, what did not, and the changes made. Each
      change names an owner.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| `keys revoke` ran but the node still seems connected | The hub applies it within seconds; the connected node stops itself | `manage.status`; if it persists, check you revoked the right ID |
| You cannot reach the hub to contain anything | The hub is the problem | The break-glass path: `halite-node state apply --local` on the nodes you can reach; or firewall the node's route to the hub |
| An operator certificate leaked and there is no revocation | Operator certificates have no revocation list | Remove its policy binding; restart hub **and** API; let it expire |
| You are unsure whether a token was used | Tokens are stored as digests | Revoke it and read the job cache for its principal's activity |
| Evidence verifies but you do not trust the node | It is intact up to its last anchor; later records are not covered | Rebuild it |

## Key ideas

- **Contain, investigate, recover, then improve.** Fast and reversible first.
- **`keys revoke`** ends a node's trust immediately; **`jobs kill`** stops a job
  reaching more nodes (and finishes none that have started); **removing a
  policy binding and restarting hub and API** ends an operator certificate.
- The job cache, event log and **evidence chain** are the investigation; ship
  events off the hub *before* you need them.
- **Revert by reverting.** The tree's history is your undo button.
- Rotate **at the source** first; a CA that may have been copied is replaced
  and the fleet re-enrolled.

Next: [migrating from Salt](41-migrating.md).
