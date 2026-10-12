# Runbook: migrating a Salt estate

<!-- abstract: The staged path from an existing Salt estate: audit the tree, stand up halite beside it, prove one machine, widen, switch off, and what is different on the way. -->
<!-- kind: runbook -->
<!-- index: migration; migrate; halite-hub migrate; audit; Salt compatibility; SLS tree; compatibility shim; salt://; grains; pillar; --fail-on; bridge skeleton; differential test; coexist; cutover; rollback; vocabulary; publisher_acl; custom modules -->

**Use this runbook to** move an estate from Salt to halite without a flag day.
It is written for someone who knows Salt well; if you do not, the rest of this
handbook is the better place to begin.

halite's first goal is that **existing state and pillar trees keep working**:
the YAML dialect (including its quirks), the Jinja dialect, SLS, requisites,
top files, grains and pillar. What changes is the machinery around the trees.
The migration is therefore mostly an *audit of differences*, and halite
ships the audit.

**You will end with** your trees running under halite on every machine, Salt
switched off, and a record of what you changed and why.

> **The honest caveat.** The migration tool has been run against two real
> trees (one of 129 state files and 64 pillar files, one smaller) and against
> synthetic ones. What has not been run is a Salt tree of *your* size and shape,
> which is the project's own stated exit criterion for the audit. Treat the
> first stage as finding out.

## Before you start

- **The two migration guides in this handbook are the detail.** This chapter is
  the plan; [Coming from Salt](../from-salt.md) is the step-by-step walkthrough
  with every command, and [Migrating from Salt](../migrating-from-salt.md) is
  the audit tool and the full list of deliberate differences. Read the second's
  "six differences that will actually bite" before you apply anything.
- **Vocabulary changes** (this is policy, not preference; the old role names are
  banned from halite's source, configuration and documentation, and accepted
  from your files by a compatibility shim that warns). The translation:

  | Salt | halite |
  |---|---|
  | master | hub | <!-- lexicon:allow -->
  | minion | node | <!-- lexicon:allow -->
  | minion key acceptance | enrolment | <!-- lexicon:allow -->
  | syndic | relay |
  | `salt-key` | `halite-hub keys` |
  | `salt` | `halite-hub run` |
  | `salt-call` | `halite-node call` (or `halite-node state apply --local`) |
  | `salt-run` | `halite-hub runner` and `halite-hub orch` |
  | `salt-ssh` | `halite-hub ssh` |
  | `salt-cp` | not built |
  | `saltenv` | `env` (the old name is accepted in templates and `salt://` URLs) |
  | `salt://` | unchanged; `halite://` is the same |
  | `publisher_acl`, `external_auth`, `peer`, `peer_run` | `policy.yaml` ([chapter 26](26-authorization.md)) |
  | blacklist, whitelist | denylist, allowlist | <!-- lexicon:allow -->

  The retained words are the accurate ones: **grains, pillar, mine, state, top
  file, SLS, highstate, beacon, reactor, orchestration, runner, returner,
  requisite**.
- A **place to run both**: they share a machine happily. halite's hub does not
  use Salt's ports; its configuration lives in `/etc/halite`
  (`/usr/local/etc/halite` on a BSD); and its tree can be the very directory
  Salt reads.
- A **representative sample** of machines, including the awkward ones.

## Procedure

### 1. Audit the tree (no machine involved)

```sh
halite-hub migrate /srv/salt --pillar-root /srv/pillar --salt-config /path/to/your/salt/server/config
```

It reads your trees and your Salt configuration, and prints a report. This one
is from a small deliberately-flawed tree:

```
Migration report for /…/srv/salt
  2 state files, 2 pillar files

Renderer inventory
  jinja|yaml               4 file(s)

Module usage
  cmd.run                          1 reference(s)
  file.managed                     1 reference(s)
  pkg.installed                    1 reference(s)
  service.running                  1 reference(s)
  user.present                     1 reference(s)

Findings

  BLOCKING (1)
    [yaml] web/init.sls:10
      "0644" has a leading zero and is read as octal 420 under YAML 1.1; quote it to keep the string
      -> Quote it. On a file mode this stops compilation. SPEC section 10.1.3.

  REVIEW (2)
    [semantics] web/init.sls:21
      `groups:` here means the groups this account must be in, and leaves any other membership alone; Salt's `remove_groups` defaults to true, …
      -> If this state relied on memberships being pruned, add `remove_groups: true`. …
    [yaml] web/init.sls:26
      "yes" resolves to the boolean true under YAML 1.1; quote it to keep the string
      -> Quote the value, or set yaml_bool_11 to false once the tree has been audited. …

Effort estimate
  semantics        1
  yaml             2
  TOTAL            3
  BLOCKING         1

1 cmd state(s) name a program with arguments in it. They were not reported, because cmd.run
runs through a shell by default: as it stands they work, and the day a node sets
cmd_default_shell: false they stop. …
```

Its exit status is non-zero on a **blocking** finding (`--fail-on
blocking|review|note` chooses the threshold), so it can gate a CI job. The
findings have three levels:

| Level | Meaning |
|---|---|
| **Blocking** | Compilation or behaviour fails as written (an unquoted file mode; a Python renderer; an unsupported regular-expression construct). Fix before applying. |
| **Review** | Behaviour differs, and you must decide: `user.present` group semantics; a YAML 1.1 boolean. |
| **Note** | Informational. |

The report also tells you what to prepare for:

- **Renderers**: anything other than `jinja|yaml` (Python renderers, Mako) is
  blocking. `exec` and the native `crypt` renderer are not built; `gpg` works.
- **Regular expressions** with constructs RE2 lacks (lookahead, backreferences)
  are found and located. This is the single most likely source of migration
  work in a mature tree, and the audit makes it measurable before you start.
- **Custom modules** (`_modules/`, `_states/`, `_grains/`, `_pillar/`): each
  gets listed, and `--bridge-skeleton DIR` writes a Go skeleton per module whose
  every function fails loudly until written
  ([chapter 29](29-extensions.md)). Two are special: `_grains/metadata.py` is
  `cloud_grains: true`, and `_pillar/aws_secrets_manager.py` is the
  `aws_secrets_manager` source.
- **Pillar targeting** on untrusted grains: the report names each, because
  pillar top files can only target trusted grains
  ([chapter 9](09-pillar.md)).
- **`cmd.run` lines** that need a shell. They keep working, and
  `--no-cmd-default-shell` audits the tree as though you had taken the hardened
  setting.
- **Your Salt configuration**: translated, with every key it ignored and every
  key it translated listed. The shim keeps legacy ACL keys under `legacy_acl`,
  never consulted, for you to read, because an authorization policy is rewritten
  and not translated.

### 2. Know the differences that will actually bite

In rough order of how often they cost someone an afternoon (the full list, with
reasons, is in the migration guide):

1. **Undefined names are an error.** Salt is strict by default too, so a tree
   that rendered under Salt renders here; what differs is that the error names
   the file, the line and the identifier. `--permissive` (or `undefined:
   permissive`) is the transition setting: it logs every resolution.
2. **Unquoted file modes are refused**, where Salt silently writes the wrong
   permissions.
3. **`y`, `n`, `yes`, `no`, `on`, `off`** are booleans (YAML 1.1) with a warning
   on each use, until you audit and set `yaml_bool_11: false`.
4. **`user.present` `groups`** means "must be in these", not "exactly these";
   `remove_groups: true` restores pruning.
5. **`environ.setenv` persists**, and **`cmd.run` can take an argument vector**
   instead of a shell line (`cmd_default_shell: false`).
6. **Arguments are strings** unless their type says otherwise; a CLI argument is
   not parsed as YAML.
7. **`--test` is a contract** (a plus, but it will show you states that
   misreported in Salt).
8. **Authorization is rewritten.** `functions: ['*']` does not grant
   `cmd.run`.
9. **Events** live under `halite/…`; set `event_tag_compat: true` while a
   consumer still expects the old tags.
10. **Nodes dial the hub on one port (4510).** Nothing else needs opening.

### 3. Stand up a hub beside Salt

[Chapter 30](30-install-hub.md), with `file_roots` pointing at the tree Salt
already reads. Nothing is copied; both see the same files. Give the hub a
policy that can do what you intend, and nothing more.

### 4. Bring one node over, alongside the agent already on it

[Chapter 31](31-install-nodes.md). The node enrols to the hub as a new,
separate identity. Salt's agent keeps running. Choose a machine you can afford
to lose that is representative of a class.

### 5. Compare before you change

This is the step not to skip. Ask both systems what they would do, and compare:

```sh
salt-call --local state.apply test=True          # what Salt says
halite-node state apply --test                    # what halite says
```

A difference is a finding: either a deliberate difference in the guide, or a
bug to report. The project's own test suite compiles ten trees with both
systems and compares the low state, the pillar and (optionally) each state's
test-mode prediction against three Salt releases; it found four defects on its
first run. What that does *not* do is compare what actually changes on a real
machine, or run against *your* trees, which is why you are doing it here.

### 6. Apply for real, on that one machine

Then run it again and require `exit 2` (converged): the idempotency test of
[chapter 4](04-idempotency.md). Leave the node running both agents for a few
days. Compare the machine's behaviour, not just the reports.

### 7. Move the schedule

Replace Salt's schedule entries with halite's (`schedule:` in `node.yaml`,
[chapter 23](23-scheduler-and-returners.md)) or a systemd timer / cron line,
with `splay`. Re-create beacons and reactors. Reactions run as principals now,
so each needs a policy role ([chapter 20](20-reactors.md)).

### 8. Widen

By class, using canaries and batches
([chapter 16](16-fleet-operations.md)). Watch `manage.versions` and the drift
report. Keep Salt running on the machines not yet moved.

### 9. Turn Salt off

When a node has run clean under halite for a defined period: stop and disable
its Salt agent, remove its key on the old Salt server, and remove the old packages
at your leisure. Keep the old Salt server read-only for a while: it holds the old
job history. Remember `salt-cp`-style file pushing is not built; and
`publish.publish`, a node asking for execution on other nodes, is not built.

### Rolling back

At every stage, the way back is the previous stage: halite's agent is a
separate program with its own state, so removing it leaves Salt untouched, and
a machine can be returned to Salt by stopping `halite-node`. Do not remove
Salt's agent from a machine until you are certain you are not going back.

## Verify

- [ ] `halite-hub migrate` exits `0` at your chosen threshold, in CI.
- [ ] For each class of machine, `halite-node state apply --test` agrees with
      Salt, or each difference is explained in your record.
- [ ] A second apply exits `2` on every migrated class.
- [ ] Reactions, beacons, schedules and returners have been rebuilt and
      exercised, not assumed.
- [ ] Authorization has been rewritten and tested with `policy test`.
- [ ] Nothing in your tooling depends on Salt's event tags, return
      destinations or API shape that you have not accounted for (`event_tag_compat`;
      the frozen JSON output schema; `/v1/…` endpoints).
- [ ] You know the features you used that are **not built** (below).

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| `must be a quoted string, found the integer 420` | An unquoted mode | Quote it |
| `x is undefined` where Salt rendered an empty string | The tree depended on permissive rendering | Run permissive with logging, fix each, return to strict |
| A pillar value is missing for a node | Pillar targets an untrusted grain | The refusal names the grain: trust it deliberately or target by ID |
| `#!py` / `#!mako` file | Unsupported renderer | Port to Jinja/YAML or an extension |
| A custom module has no equivalent | Python modules do not load | `--bridge-skeleton` |
| `cmd.run` lines break after `cmd_default_shell: false` | They were shell lines | Convert to `name` plus `args` |
| Your regex has lookahead or a backreference | RE2 | Rewrite; the audit lists each |
| Targeting by a range or an old matcher | Not supported | Nodegroups |
| An API consumer breaks | Different routes and token flow | `/v1/…`; the client types are kept |

## What is not there

For planning, the larger absences today (the ledger is the authority):

- most **external pillar** sources (one ships; others are extensions you write);
- **`halite-hub files`** (the `salt-cp` direction);
- **most Windows modules**, and the cloud / virtualization / network-device
  families;
- **`salt.parallel`** and the **`queue`** orchestration option, the durable
  work queue behind them;
- **`cp.*`** and `publish.*` functions;
- **node-side events as privileged tags** (a node can only speak under its
  own prefix, by design);
- **schedules and beacons through pillar**;
- the **native `crypt` renderer** (use GPG or a secrets manager).

## Key ideas

- The migration is an **audit of differences**, run by `halite-hub migrate`
  against your trees, with a gating exit status.
- **Run both**, side by side; prove **one** machine; compare `--test` against
  Salt; require **exit 2** on the second apply; then widen by class.
- The differences that bite: **strict undefined, quoted modes, YAML booleans,
  group semantics, argument types, authorization**.
- Custom Python becomes **extensions**; Salt ACLs are **rewritten**, not
  translated.
- Know what is **not built** before you promise a date.

Next, Part VII: the reference.
[Command reference](gen-cli.md).
