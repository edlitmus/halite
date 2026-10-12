# Runbook: backup, restore and disaster recovery

<!-- abstract: What to back up on a hub and a node, how to restore a hub without re-enrolling anyone, and what losing each piece actually costs, with the failure shown. -->
<!-- kind: runbook -->
<!-- index: backup; restore; disaster recovery; ca.key; pki_dir; state_dir; hub replacement; lost CA; re-enroll; evidence; job cache; recovery time; high availability; hot standby; file backup; backup: node; file.list_backups; file.restore_backup -->

**Use this runbook to** decide what to back up, back it up, and bring a hub
(or a node) back from nothing.

**You will end with** a backup you have restored from at least once, a written
recovery order, and an understanding of what each loss costs.

## Before you start

The question that decides everything is: **what is lost if I lose X?** In
halite there are four kinds of state, and they differ enormously:

| State | Where | If lost |
|---|---|---|
| **The CA** (`ca.crt`, `ca.key`) and the hub's other keys | hub `pki_dir` | **Every node must enrol again.** Their certificates cannot be verified against any CA you can produce. This is the one that matters. |
| **The key store** (each node's record) | hub `state_dir/keys` | Nodes' certificates still verify (the CA is intact) but the hub does not know them; each has to be re-accepted. |
| **History**: jobs, events, orchestration runs, mine data, evidence anchors | hub `state_dir/{jobs,events,orch,mine,evidence-anchors}` | You lose the audit trail and the reactor's place; the fleet carries on. The mine refills within one `mine_interval`. |
| **Cache** | hub `cache_dir`, node `cache_dir` | Nothing; it is rebuilt. |
| **Configuration and the tree** | `hub.yaml`, `policy.yaml`, states, pillar | Whatever they were; this is why they are in version control. |
| A node's **key and certificate** | node `pki_dir` | That node enrols again; nothing else is affected. |
| A node's **evidence chain** | node `state_dir/evidence` | That node's local audit record. The hub's anchors still show what it reported. |

So the plan is short: **protect the CA and the key store above all**,
back up history to the extent your audit policy requires, and keep
everything else in git.

Know your **recovery objectives**. A hub outage does not stop the fleet doing
what it already does ([chapter 12](12-hub-and-nodes.md)): nodes keep running,
retry the connection, and a scheduled highstate in `node.yaml` keeps working
. What stops is dispatch,
new enrolment and reactions. How long that may last is your decision.

## Procedure

### 1. Back up the hub

What to include, and the file-level view of it as the lab produced it:

```
pki/ca.crt  pki/ca.key  pki/hub.crt  pki/hub.key  pki/operator-*.crt  pki/operator-*.key
state/keys  state/events  state/jobs  state/orch  state/mine  state/evidence-anchors
state/event-return.offset  state/event-return-spool  state/reactor.offset
```

plus `hub.yaml` (and `hub.d/`), `policy.yaml`, `api.yaml`, `accounts.yaml`, the
GPG key ring if you use `#!yaml|gpg` pillar (`gpg_home`), the signing keys for
extensions (these should not be on the hub, but wherever they are), and the
tree if it is not in git. Do not include `cache_dir`.

A minimal, consistent backup:

```sh
# on the hub, as root or the hub's account
umask 077
systemctl stop halite-hub        # FreeBSD: service halite_hub stop    (see "A live backup" below)
tar -C /var/lib/halite -czf /backup/halite-state-$(date +%F).tar.gz .
tar -C /etc/halite     -czf /backup/halite-etc-$(date +%F).tar.gz .
systemctl start halite-hub
```

**The CA key needs more care than the rest.** The backup of `ca.key` is a copy
of the ability to issue any identity in the estate. Encrypt the archive
(`age`, `gpg --symmetric`, your vault), store it apart from the others, and
restrict who can read it. Consider keeping the CA key in a KMS through the
signer interface so that no file copy exists.

**A live backup.** Stopping the hub for a snapshot interrupts dispatch, not the
fleet, and takes seconds. If you cannot, snapshot the filesystem (ZFS, LVM)
and back up the snapshot: the key store, job cache and event log are
append-oriented and a point-in-time copy is consistent enough to restore from,
though the last few records may be missing.

**Frequency.** `pki/` changes only when you issue an operator certificate, so
back it up when that happens and daily thereafter. `state/keys` changes on
every enrolment and revocation. History changes constantly; a daily copy is
the usual compromise. Send copies off the host.

### 2. Back up a node

A node needs very little:

- `pki_dir` (the key and certificate) *if re-enrolling is costly for you*. The
  usual answer is **do not back it up**: a rebuilt node enrols again in a
  minute, and a restored copy of a key is a second copy of a secret.
- `state_dir/evidence`, if your obligations include the node's own audit
  record.
- Custom grains (`grains`, `grains.d/`), if they are not written by a state.
- Anything your states do not rebuild.

Most nodes need no backup, because the point of declaring a machine in a tree
is that the tree rebuilds it.

#### File backups taken by states

`file.managed` can keep the previous contents when it overwrites:

```yaml
/etc/app/app.conf:
  file.managed:
    - source: salt://app/app.conf.jinja
    - template: jinja
    - backup: node
```

`backup: node` writes a timestamped copy under
`<cache_dir>/file_backup/<the file's absolute path>/` before changing the file
(any other value is a suffix, giving `<path><suffix>` beside it). A node with
no `cache_dir` keeps no backups and says so.

```sh
halite-hub run web1 file.list_backups /etc/app/app.conf
halite-hub run web1 file.restore_backup /etc/app/app.conf <timestamp>
```

This is a safety net for a state that wrote something wrong. It is not a
substitute for the tree.

### 3. Rehearse the restore (do this once, before you need it)

Every step below was run. In the lab: take a backup of `pki/` and `state/`,
stop the hub, **destroy** its data, restore from the archive into an empty
directory, and start it.

```sh
tar -C hub -czf hub-backup.tar.gz pki state          # the backup
kill <the hub>; mv hub hub.destroyed                  # the disaster
mkdir -p hub/cache && tar -C hub -xzf hub-backup.tar.gz   # the restore
# start the hub
```

Result, within about ten seconds of starting it, with **no action on any
node**:

```
$ halite-hub keys list
accepted (3)
  db1.lab    61:34:9e:a7:…
  web1.lab   cd:6e:55:75:…
  web2.lab   91:59:a7:7b:…

$ halite-hub run '*' test.ping
web1.lab: True
web2.lab: True
db1.lab: refused: this node requires a signature …      # (a lab node that requires signed jobs)

$ halite-hub jobs list --limit 2
20261011T193100153670  test.ping   *     complete
20261011T193047353856  event.send  hub   complete
```

The nodes reconnected on their own because the restored CA was the one they had
pinned and the restored key store knew them; job history, the mine and the
reactor's position came back too. That is the standard, easy recovery, and it
is only easy if `pki/` and `state/keys` were in the backup.

### 4. Recover the hub: the order

**A. The host survives, the data does not** (a disk failure, a bad upgrade):

1. Reinstall the programs ([chapter 30](30-install-hub.md) steps 1 to 3).
2. Restore `/etc/halite` and the state directory from backup, preserving modes
   and ownership (the hub's account owns them, `pki/` is `0700`).
3. Start the hub. Do not create a new CA.
4. Verify as in step 3 above.

**B. A new host** (the old one is gone): the same, plus:

1. Give the new host the **same name** (DNS) that nodes dial, or the same
   address. Nodes verify the certificate against the name they dialled, and the
   restored serving certificate names the old host's names. If the names change,
   start the hub with `--names` including every name nodes use; it issues a
   fresh serving certificate and nodes accept it because it chains to the same
   CA. If the CA was restored, **no node needs to change**.
2. Restore, start, verify.

**C. The CA key is lost and there is no backup.** Not a recovery, a rebuild:

```
$ halite-hub keys fingerprint        # on a hub started with a new pki_dir
46:9b:91:15:57:ec:…                  # a different CA, a different fingerprint
```

and every node's log says, repeatedly:

```
warn: the stream ended error=Post "https://…/v1/subscribe": tls: failed to verify
certificate: x509: certificate signed by unknown authority …
warn: compiling pillar for a beacon poll error=…/v1/pillar: … certificate signed by unknown authority
```

(This was run: a hub was started on an empty `pki_dir` while the nodes held
certificates from the old CA.) The nodes keep retrying, harmlessly, and
recovered the moment the original data was put back. With no backup, each node
must be re-enrolled against the new CA: give it the new `hub_fingerprint`,
remove its old `pki_dir` contents, run `halite-node enroll`, and accept it. With
a token mode and a provisioning system this is a script; with manual
enrolment it is an afternoon per hundred nodes. This is the cost that makes
the CA key worth a dedicated backup.

**D. Operator certificates and the API.** Restored `pki/` brings back
operator certificates, but they expire in 30 days; re-issue any that are
close to expiry ([chapter 37](37-certificates.md)). Restart the API after the
hub is up.

### 5. Recover a node

Rebuild the machine from the tree: install the program, write `node.yaml`
(fingerprint and ID), enrol with a token or manual approval **under the same
node ID**. If the old record is still on the hub, `halite-hub keys revoke <id>`
then `keys delete <id>` first, or the new request collides with it. The first
highstate restores its configuration. Its old evidence chain is gone with the
old disk; the hub still holds the anchors of what it reported.

### 6. Do not have a single hub if you cannot afford one outage

There is no active/active hub. Options, in increasing effort:

- **Accept** the outage window. A fleet keeps converging by its own schedule.
- **A warm standby**: a second host with a periodically restored copy of the
  state and the same name behind a failover address. Remember that the key
  store and CA must be current, so restore frequently or share the data with
  replication.
- **Relays** for network reachability and returns that survive an upstream
  outage ([chapter 38](38-scaling.md)); they do not replace the hub.

## Verify

You have a recovery plan when you can answer yes to all of these:

- [ ] A backup of `pki/` and `state/` exists, off the hub, newer than your last
      enrolment or revocation.
- [ ] The archive holding `ca.key` is encrypted and its key is not on the hub.
- [ ] You have restored it somewhere and seen `keys list`, `test.ping` and
      `jobs list` return as they were.
- [ ] You know which names nodes dial and could give a replacement host the
      same ones.
- [ ] The tree, `hub.yaml`, `policy.yaml` and the GPG key ring are in
      version control or in the backup.
- [ ] The recovery order above is on a card someone can find during the
      outage.
- [ ] Someone other than the author has done it.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| After a restore nodes log `unknown authority` | You restored the wrong `pki/`, or started a fresh one | Stop the hub; restore the right `pki/`; start it |
| After a restore `keys list` is empty | `state/keys` was not in the backup | Nodes hold valid certificates but the hub has no record; re-accept each, or re-enrol |
| The hub refuses to start after a restore | Modes or ownership wrong (`pki/` must be `0700` and the hub's account's) | `chown -R halite`, `chmod 0700 pki` |
| The hub starts but nodes cannot verify it | The new host's names are not on the serving certificate | Restart with `--names` listing every name nodes use |
| Operators cannot log in after a restore | Operator certificates expired in the meantime | `keys operator create` again |
| `serve` refuses to start | The restored tree overlaps `state_dir`/`pki_dir` | Keep them apart |

## Key ideas

- **The CA is the crown jewel**: losing `ca.key` with no backup means every
  node enrols again. Back up `pki/` and `state/keys` first, encrypt the CA key
  archive, keep it off the hub.
- History is backed up to the extent your audit requires; cache is never.
- A restore of `pki/` + `state/` brings the whole fleet back **without
  touching a node**, which was demonstrated.
- A node is rebuilt from the tree; back up only what states cannot recreate.
- There is one hub: decide how long its outage may last and plan for exactly
  that.

Next: [upgrading](36-upgrades.md).
