# Runbook: upgrading and rolling back

<!-- abstract: Moving a hub, an API and a fleet of nodes to a new release: verify, read, back up, canary, roll, confirm, and how to go back. -->
<!-- kind: runbook -->
<!-- index: upgrade; downgrade; rollback; version skew; manage.versions; release verification; SHA256SUMS; attestation; CHANGELOG; canary upgrade; halite-node version; foreign schema; halite.job/1; ALPN; restart; zero downtime -->

**Use this runbook to** move the estate to a new release without taking the
fleet out of service, and to go back if the new release misbehaves.

**You will end with** every program on the new version
(`halite-hub runner manage.versions` shows one version and `mismatched: []`),
a record of what changed, and a rehearsed way back.

## Before you start

- **Read what changed.** The release's section of `CHANGELOG.md` (operator-visible
  changes), the [divergence ledger](../DIVERGENCE.md) entries it cites, and
  `EVIDENCE.md` in the archive (what has and has not been demonstrated for each
  module, so you know which modules in your tree changed status). A change that
  alters what an operator sees is in the changelog; a change to a module's
  behaviour is in the ledger.
- **Know the skew rules**, because they decide how careful the order must be.
  halite is pre-1.0 and changes, but the project has tested what happens
  across a version boundary and written it down:

  | Situation | Behaviour |
  |---|---|
  | A **newer hub** and **older nodes** | A message type an old node does not know reaches a branch that logs it and carries on; the stream stays open. |
  | A **newer node** and an **older hub** | Nothing rejects unknown fields; a request carrying fields the hub never heard of is accepted and the fields ignored. |
  | **The transport identifier** (ALPN `halite/1`) | The one place skew is fatal. A change to it breaks both directions at once. Expect it to be called out in the release notes as a breaking change. |
  | A **hub rolled back** over data a newer hub wrote | Records stamped with a schema the old hub does not know are **readable but not writable**: `jobs list` still works, and writes are refused (`ErrForeignRecord`) rather than truncating the record. |

  So **neither upgrade order is forced**, and a rolled-back hub does not
  silently lose fields from the job cache. The tolerance is a tested guarantee,
  but the *safe order* is still hub, then API, then nodes (below).
- **A backup** of `pki/` and `state/` ([chapter 35](35-backup-and-recovery.md))
  from before you start. A rollback is a binary swap; a backup is for when the
  new version has altered the data.
- A **maintenance window** for the hub's restart: the fleet keeps converging by
  its own schedule meanwhile, but dispatch, enrolment and reactions pause for
  the seconds it takes.
- The **new archive**, verified.

## Procedure

### 1. Verify the release

```sh
v=0.2.0; p=linux-amd64
sha256sum --ignore-missing -c SHA256SUMS
gh attestation verify halite-$v-$p.tar.gz --repo edlitmus/halite \
    --signer-workflow edlitmus/halite/.github/workflows/release.yml \
    --source-ref refs/tags/v$v
openssl dgst -sha256 -verify halite-release.pub -signature SHA256SUMS.sig SHA256SUMS
```

The first checks the digest, the second the build provenance, the third the
maintainers' signature over the whole manifest (`halite-release.pub`, whose
fingerprint is in `docs/releasing.md`). An SBOM
(`halite-$v-$p.cdx.json`, CycloneDX) lists exactly what was linked. A release
is built twice on different runners and the digests must agree before it is
attested, so a mismatch anywhere means do not install.

### 2. Rehearse on something you can lose

Install the new binaries on a spare node and a spare hub (the lab), run the
tree against it with `--test`, and read `EVIDENCE.md` for any module you use
that changed level. Compile the tree with the new `halite-node`
([chapter 16](16-fleet-operations.md) `check-tree.sh`): a state function whose
arguments changed fails at compile, not at 3 a.m.

### 3. Upgrade the hub

```sh
# on the hub
sudo install -m 0755 halite-$v-$p/bin/halite-hub /usr/local/bin/halite-hub.new
sudo mv /usr/local/bin/halite-hub /usr/local/bin/halite-hub.$(date +%F)    # keep the old one
sudo mv /usr/local/bin/halite-hub.new /usr/local/bin/halite-hub
halite-hub version
sudo systemctl restart halite-hub          # FreeBSD: sudo service halite_hub restart
```

`install` to a temporary name and `mv` is atomic and keeps the old binary
beside the new, which is your rollback. Also install the **service files** if
the release changed them (`make install-service`, or copy from `service/`), and
`systemctl daemon-reload`. Nodes reconnect on their own, typically within
seconds.

Check at once:

```sh
halite-hub doctor
halite-hub runner manage.status
halite-hub runner manage.versions
```

```
hub: 0.1.0-7-g8ad761ce275b-dirty+8ad761ce275b
nodes:
    0.0.9+abc123abc123:
        - web2.lab
    0.1.0-7-g8ad761ce275b-dirty+8ad761ce275b:
        - db1.lab
        - web1.lab
mismatched:
    - web2.lab
```

That is real output from a lab with one node deliberately left on an older
build (version `0.0.9`): the hub's own version, then each version nodes last
reported with the nodes on it, then `mismatched`, the nodes that differ from
the hub. It is your progress report for the rest of the procedure.

### 4. Upgrade the API

```sh
sudo install -m 0755 halite-$v-$p/bin/halite-api /usr/local/bin/halite-api.new
sudo mv /usr/local/bin/halite-api.new /usr/local/bin/halite-api
sudo systemctl restart halite-api
halite-api doctor
```

The API reads the policy at start, so this restart is also when it picks up any
policy change.

### 5. Upgrade nodes: canary, then batches

A node's binary is just a file. How you replace it depends on how you ship it:

- **A package or your own deployment tool**: use it.
- **halite itself**: publish the new binary in the tree and let a state place
  it, then restart the agent *after* the job ends so the restart does not kill
  the job doing it. The example tree (`contrib/examples/tree`, `halite.node`)
  does exactly this with a deferred restart (`daemon(8)` on FreeBSD,
  `systemd-run --on-active` on Linux). Its README says what has been compiled
  and what has been run. The restart command was run inside a real job on
  Ubuntu 26.04 and FreeBSD 15.1: the job finished and returned `True` first,
  and the node restarted afterwards and came back `up`. On Linux the restart
  came 52 seconds after the job, not 30, because a transient timer has
  systemd's default accuracy of one minute, so read `30` as "between 30 and 90
  seconds". The same restart written directly in the state (`systemctl
  restart` or `service restart`) was run as a control: the hub's `run` never
  returned, and on FreeBSD the node stayed **down**, the rc script saying `could
  not enrol (exit 141); not starting`. Not run: a whole fleet upgrade, and
  replacing the binary with the state.
- **By hand**, for a few nodes: the same three commands as the hub, with
  `halite-node` and `halite_node`.

Whatever the means, go in the order **one, a few, the rest**:

```sh
# the first node
sudo install -m 0755 halite-$v-$p/bin/halite-node /usr/local/bin/halite-node.new
sudo mv /usr/local/bin/halite-node.new /usr/local/bin/halite-node && sudo systemctl restart halite-node
halite-hub run web1 test.version
halite-hub run web1 state.apply --test       # does the new agent agree with the tree?
```

then check `manage.versions`, let it run through a full scheduled highstate
cycle, read `halite-node doctor`, and only then go wider, in batches with a safe
limit ([chapter 14](14-remote-execution.md)):

```sh
halite-hub run -N canary state.apply --test
```

A node restarts in seconds and reconnects; a restart does not need a window
unless your obligations make a node's own agent being down for those seconds
matter.

### 6. Confirm

```sh
halite-hub runner manage.versions
```

```
hub: 0.2.0
nodes:
    0.2.0:
        - db1.lab
        - web1.lab
        - web2.lab
mismatched: []
```

and the metric `halite_returns_foreign_schema_total` — returns whose schema the
hub does not know, meaning a node is *newer* than it — should be back to zero.
Finally run a fleet rehearsal ([chapter 16](16-fleet-operations.md)): the drift
report should be empty or explained. Write the old and new versions and the
date in your change record.

## Roll back

Rolling back is the same procedure in reverse, and it is cheap because the old
binary is still there:

```sh
sudo mv /usr/local/bin/halite-hub /usr/local/bin/halite-hub.failed
sudo mv /usr/local/bin/halite-hub.2026-10-12 /usr/local/bin/halite-hub    # the one you kept
sudo systemctl restart halite-hub
```

Order for a full rollback: nodes you already moved, then the API, then the
hub. What carries across, and what to expect:

- The **job cache**, event log and key store written by the newer hub are
  **readable** by the older one; any record stamped with a schema the older hub
  does not know is refused for **writing** (so a job record is never silently
  truncated) and reported by name. Looking at history keeps working.
- **Configuration** that the newer release accepted and the older does not
  read is reported as an unrecognised key at start. Restore the previous
  `hub.yaml` if you changed it for the new release.
- If the new release changed **data formats** in a way the release notes call
  out, restore `state/` from the pre-upgrade backup instead.

## Verify

You have upgraded when all of these hold:

- [ ] `halite-hub runner manage.versions` shows one version and `mismatched: []`.
- [ ] `halite-hub doctor`, `halite-api doctor` pass; a sample of `halite-node
      doctor`s pass.
- [ ] `halite_returns_foreign_schema_total` is not rising.
- [ ] A fleet `state.apply --test` is as clean as before the upgrade.
- [ ] You have the previous binaries and a backup, labelled, for a week.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| The hub does not start after the upgrade | A setting removed or renamed; a data directory unreadable | The log names the key; restore the previous `hub.yaml`/binary |
| Nodes do not reconnect | The transport identifier changed (a breaking release) or the hub's certificate names changed | Read the release notes; nodes must be upgraded too |
| Writes refused with a foreign-record message | You rolled back over data a newer hub wrote | Roll forward, or restore `state/` from backup |
| `mismatched` never empties | Nodes that are off or cannot restart | `manage.down`; fix or accept |
| `compile` errors that were not there before | A state function's arguments changed | The compile error names the file and line; the ledger names the change |
| A node's evidence chain breaks after an upgrade | It should not; this is a serious finding | `halite-node verify-evidence`; keep the node, escalate ([chapter 40](40-incidents.md)) |
| A restart killed a running job | The deferred restart was not deferred | Stagger; re-run the job (it is idempotent) |

## Key ideas

- **Verify, read, back up, rehearse.** Four steps before any production
  restart.
- No order is *forced* (tested), but the safe order is **hub, API, nodes**,
  one then few then the rest.
- Swap binaries with `install … .new` + `mv`, keeping the old file: it is
  your rollback.
- `manage.versions` is the progress report; `mismatched: []` is done.
- A rolled-back hub reads newer records but **refuses to write** them, so
  history is never silently truncated.

Next: [certificate operations](37-certificates.md).
