# Salt commands and their halite equivalents

Every Salt command an operator types, and what to type instead. The
**Status** column says whether it works in this build; the phases are
SPEC section 32's, and [DIVERGENCE.md](DIVERGENCE.md) says what each
covers.

halite has no `--local` by default the way `salt-call` has none: a node
with no hub configured still needs to be told it is working from local
roots, and `--local` is that. Passing `--file-root` or `--pillar-root`
implies it.

## Running states on one machine

| Salt | halite | Status |
|---|---|---|
| `salt-call --local state.apply` | `halite-node state apply --local` | works |
| `salt-call --local state.highstate` | `halite-node state highstate --local` | works |
| `salt-call --local state.apply test=True` | `halite-node state apply --local --test` | works |
| `salt-call --local state.apply web` | `halite-node state apply web --local` | works |
| `salt-call --local state.sls web,db` | `halite-node state sls web db --local` | works |
| `salt-call --local state.show_top` | `halite-node state show_top --local` | works |
| `salt-call --local state.show_highstate` | `halite-node state show_highstate --local` | works |
| `salt-call --local state.show_lowstate` | `halite-node state show_lowstate --local` | works |
| `salt-call --local state.show_sls web` | `halite-node state show_sls web --local` | works |
| `salt-call --local state.show_states` | `halite-node state show_states --local` | works |

`state.apply` is also reachable as `halite-node call state.apply`, which
is the spelling muscle memory produces.

## Reading a node

| Salt | halite | Status |
|---|---|---|
| `salt-call --local grains.items` | `halite-node grains items --local` | works |
| `salt-call --local grains.get os` | `halite-node grains get os --local` | works |
| `salt-call --local grains.item os kernel` | `halite-node grains item os kernel --local` | works |
| `salt-call --local grains.ls` | `halite-node grains ls --local` | works |
| `salt-call --local pillar.items` | `halite-node pillar items --local` | works |
| `salt-call --local pillar.get a:b` | `halite-node pillar get a:b --local` | works |
| `salt-call --local test.ping` | `halite-node call test.ping --local` | works |
| `salt-call --local cmd.run 'uname -a'` | `halite-node call cmd.run /usr/bin/uname args='["-a"]' --local` | works |
| `salt-call --local pkg.list_pkgs` | `halite-node call pkg.list_pkgs --local` | works |
| `salt-call --local service.get_all` | `halite-node call service.get_all --local` | works |
| `salt-call --local sys.list_modules` | `halite-node call sys.list_modules --local` | works |
| `salt-call --local sys.doc file.managed` | `halite-node call sys.doc file.managed --local` | works |

`grains.get` takes one key and answers with the value; `grains.item`
takes any number and answers with a mapping. Salt's do the same, and
mixing them up is why `grains item a b c` used to answer about `a`.

## Checking a tree without running it

| Salt | halite | Status |
|---|---|---|
| no equivalent | `halite-hub migrate /srv/salt --pillar-root /srv/pillar` | works |
| no equivalent | `halite-hub migrate /srv/salt --salt-config /etc/salt/master` | works | <!-- lexicon:allow -->
| no equivalent | `halite-hub migrate /srv/salt --fail-on review` | works |
| no equivalent | `halite-hub migrate /srv/salt --cmd-default-shell` | works |
| no equivalent | `halite-hub migrate /srv/salt --bridge-skeleton ./bridges` | works |
| `salt-call --local state.show_sls web` (to find errors) | `halite-node lint /srv/salt/web.sls` | works |
| no equivalent | `halite-hub lint /srv/salt/web.sls` | works |

`migrate` is the one with no Salt counterpart and the one to run first.
It reads a tree and reports what will break, without applying anything
and without needing a node. See [Migrating from Salt](migrating-from-salt.md).

## Output

| Salt | halite | Status |
|---|---|---|
| `--out=json` | `--out json` | works |
| `--out=yaml` | `--out yaml` | works |
| `--out=quiet` | `--out quiet` | works |
| `--out=txt` | `--out txt` | works |
| default nested output | `--out nested`, the default | works |
| `--out-indent=2` | `--indent 2` | works |

## The file server

The hub serves the state tree, and a node compiles against it. Set
`file_roots` on the hub; a node with a `hub` configured uses it unless
`--local` says otherwise.

| Salt | halite | Status |
|---|---|---|
| `salt://web/nginx.conf` in a state | works unchanged | works |
| `salt-run fileserver.file_list` | `halite-hub runner fileserver.file_list` | works |
| the master serves `file_roots` | the hub serves `file_roots` | works | <!-- lexicon:allow -->
| `cp.cache_file` | happens automatically; the cache is under `cache_dir` | works |
| gitfs, s3fs | `fileserver_backend` accepts only `roots` | phase 5 |

`serve` refuses to start if a file root holds the hub's own `state_dir`,
`cache_dir`, or `pki_dir` — that arrangement serves the job cache, and
so every return in the estate, to every enrolled node.

## The event bus

The hub's bus is a durable append-only log, not Salt's in-memory one.
A subscriber resumes from an offset, so a reactor that restarts loses
nothing and an incident can be reconstructed afterwards.

| Salt | halite | Status |
|---|---|---|
| `salt-run state.event` | `halite-hub event listen` | works |
| `salt-run state.event tagmatch='salt/job/*'` | `halite-hub event listen --tag 'halite/job/**'` | works |
| `salt-call event.send tag data` | `halite-node event send <tag> '{"k":"v"}'` | works |
| `event.send` from a state or a job | `halite-hub run '*' event.send tag=… data='{"k":"v"}'` | works |
| no equivalent | `halite-hub event listen --from earliest` (replay) | works |
| no equivalent | `halite-hub event tags` | works |
| no equivalent | `halite-hub metrics` (the hub's own exposition) | works |
| `salt-api` event stream | SSE and WebSocket at `/v1/events` | works |
| no equivalent | Prometheus metrics at `/v1/metrics` | works |

A node's events are namespaced under `halite/node/<node_id>/`
regardless of the tag it asks for. Salt's reactor runs with the control
plane's full privilege, so a node that can fire the right event can
cause fleet-wide execution; here it cannot write another node's tag or
the hub's.

## Pillar

The hub compiles each node's pillar and sends that node only its own.
Set `pillar_roots` on the hub.

| Salt | halite | Status |
|---|---|---|
| `salt-call pillar.items` | `halite-node pillar items` | works |
| `salt-call --local pillar.items` | `halite-node pillar items --local` | works |
| `salt '*' pillar.items` | `halite-hub run '*' pillar.items` | works |
| `#!yaml|gpg` in a pillar file | works, decrypted on the hub | works |
| `ext_pillar` | not built; the setting warns that it does nothing | not built |

An enrolled node's `pillar items`, `call`, and `state apply` go through
the hub unless `--local` says otherwise, which is what `salt-call` does. <!-- lexicon:allow -->
A hub that cannot be reached is a warning and a local compilation, not
a failure.

Targeting a pillar top file on a grain still needs the grain in
`pillar_trusted_grains`, and moving the compilation to the hub does not
change that: the grains still come from the node. An unvetted grain in
a pillar top is refused by name rather than quietly matching nothing.

## Enrollment and the key lifecycle

This works. A hub issues, an operator decides, and a node holds a
certificate it generated the key for. SPEC section 7.

| Salt | halite | Status |
|---|---|---|
| `salt-master` (the daemon) | `halite-hub serve` | works | <!-- lexicon:allow -->
| `salt-key -L` | `halite-hub keys list` | works |
| `salt-key -f web1` | `halite-hub keys fingerprint web1` | works |
| `salt-key -p web1` | `halite-hub keys show web1` | works |
| `salt-key -a web1` | `halite-hub keys accept web1` | works |
| `salt-key -A` | `halite-hub keys accept --all` | works |
| `salt-key -r web1` | `halite-hub keys reject web1 --reason "not ours"` | works |
| `salt-key -d web1` | `halite-hub keys delete web1` | works |
| no equivalent | `halite-hub keys revoke web1 --reason "decommissioned"` | works |
| no equivalent | `halite-hub keys export-crl` | works |
| `auto_accept: True` | `halite-hub keys token create --ttl 1h --nodes 'web*'` | works |
| no equivalent | `halite-hub keys token list` | works |
| no equivalent | `halite-hub keys token revoke <id>` | works |
| the minion generates a key on first start | `halite-node enroll` | works | <!-- lexicon:allow -->
| no equivalent | `halite-node renew` | works |
| `salt-minion` (the daemon) | `halite-node connect` | works | <!-- lexicon:allow -->

There is deliberately no `auto_accept`. A bootstrap token is the
automatic path, and unlike `auto_accept` it has a mandatory lifetime of
at most a day, a node-ID scope, a source CIDR, and a record of what it
admitted. SPEC section 7.3.

## Driving a fleet

This works. The hub resolves the target, records the job with its
expected respondents, and delivers it; each node validates it, runs it,
and posts a return.

`run` needs an operator certificate — there is no "trusted because it
is running on the hub" — and a policy that permits the request.
`halite-hub keys operator create <name> --admin` issues the certificate
and, if there is no policy yet, a bootstrap one. `run` finds the
certificate if there is exactly one in the key directory.

Without a policy file nothing is authorized, and the hub says so at
startup rather than treating the absence as permission.

| Salt | halite | Status |
|---|---|---|
| `salt '*' test.ping` | `halite-hub run '*' test.ping` | works |
| `salt -G 'os:FreeBSD' state.apply` | `halite-hub run -G 'os:FreeBSD' state.apply` | works |
| `salt '*' state.apply test=True` | `halite-hub run '*' state.apply --test` | works |
| `salt --async '*' state.apply` | `halite-hub run --async '*' state.apply` | works |
| `salt --timeout=60 '*' test.ping` | `halite-hub run --timeout 60s '*' test.ping` | works |
| `salt-run jobs.list_jobs` | `halite-hub jobs list` | works |
| `salt-run jobs.lookup_jid <jid>` | `halite-hub jobs lookup <jid>` | works |
| `salt-run jobs.print_job <jid>` | `halite-hub jobs show <jid>` | works |
| no equivalent | `halite-hub jobs missing <jid>` | works |
| no equivalent | `halite-hub jobs prune` | works |
| no equivalent | `halite-hub keys operator create <name>` | works |
| `publisher_acl`, `external_auth`, `client_acl` | one `policy.yaml`, SPEC 23.5 | works |
| no equivalent | `halite-hub policy show` | works |
| no equivalent | `halite-hub policy test <principal> <target> <fun>` | works |
| `salt --batch=25% '*' state.apply` | `halite-hub run --batch 25% '*' state.apply` | works |
| `salt --batch-wait=30 …` | `halite-hub run --batch-wait 30s …` | works |
| no equivalent | `halite-hub run --batch-safe-limit 3 …` | works |
| `salt --subset=5 '*' test.ping` | `halite-hub run --subset 5 '*' test.ping` | works |
| `salt --progress …` | `halite-hub run --progress …` | works |
| no equivalent | `halite-hub jobs active` | works |
| no equivalent | `halite-hub jobs resume <jid>` | works |
| `salt-run jobs.active` | `halite-hub jobs active` | works |
| `salt-run manage.up` | `halite-hub runner manage.up` | works |
| `salt-cp '*' file /tmp/file` | `halite-hub files push` | not built |
| `salt-run jobs.kill <jid>` | `halite-hub jobs kill <jid>` | works |
| no equivalent | `halite-hub jobs export <jid>` | works |
| `salt '*' --queue state.apply` | `halite-hub run '*' state.apply --offline queue` | works |
| `salt '*' saltutil.sync_grains` | pushed automatically on `grains_refresh_interval` | works |
| `salt-ssh '*' test.ping` | `halite-hub ssh '*' test.ping` | phase 5 |

| `salt-run state.orchestrate` | `halite-hub orch run <sls>` | works |
| `salt-api` | `halite-api serve` | works |

`run` exits 0 when every node succeeded, 1 when one failed, and 3 when a
node was sent the job and did not answer — because "it said no" and "it
said nothing" call for different things.

## Runners

A runner runs on the hub rather than on a node. `halite-hub runner
<module.function>` is the old `salt-run`, and it is a request to the hub
even when it is typed on the hub: an operator authenticates with a
certificate, and being logged in on the hub is not a credential.

A runner is authorized by the `runners:` list of the caller's role, not
by `functions:`. Permission to ask the hub a question and permission to
run a command on every node are different permissions, and Salt's
`external_auth` conflating them is how a `@runner` grant turns out to be
wider than it looked. A runner that then reaches the fleet —
`saltutil.refresh_pillar` does — is authorized a second time as the job
it dispatches, so a `runners:` grant alone cannot become fleet-wide
execution.

Every runner call gets a jid, is recorded in the job cache with the
principal that asked for it, and emits `halite/run/<jid>/new` and
`halite/run/<jid>/ret` on the event bus.

`halite-hub runner list` prints the whole SPEC 19.2 inventory, including
the runners that are declared and not built yet, each with the phase it
arrives in. `halite-hub runner doc <name>` prints one signature. Both
answer from the binary, so they work when the hub does not.

| Salt | halite | Status |
|---|---|---|
| `salt-run jobs.list_jobs` | `halite-hub runner jobs.list_jobs` | works |
| `salt-run jobs.lookup_jid <jid>` | `halite-hub runner jobs.lookup_jid <jid>` | works |
| `salt-run jobs.list_job <jid>` | `halite-hub runner jobs.list_job <jid>` | works |
| `salt-run jobs.print_job <jid>` | `halite-hub runner jobs.print_job <jid>` | works |
| `salt-run jobs.active` | `halite-hub runner jobs.active` | works |
| `salt-run jobs.missing <jid>` | `halite-hub runner jobs.missing <jid>` | works |
| `salt-run jobs.exit_success <jid>` | `halite-hub runner jobs.exit_success <jid>` | works |
| `salt-run manage.status` | `halite-hub runner manage.status` | works |
| `salt-run manage.up` | `halite-hub runner manage.up` | works |
| `salt-run manage.down` | `halite-hub runner manage.down` | works |
| `salt-run manage.versions` | `halite-hub runner manage.versions` | works |
| `salt-run manage.list_state` | `halite-hub runner manage.list_state up` | works |
| `salt-run key.list_all` | `halite-hub runner key.list` | works |
| `salt-run key.accept <node>` | `halite-hub runner key.accept <node>` | works |
| no equivalent | `halite-hub runner key.revoke <node>` | works |
| `salt-run nodegroups.list` | `halite-hub runner nodegroups.list` | works |
| no equivalent | `halite-hub runner nodegroups.expand <name>` | works |
| `salt-run pillar.show_pillar` | `halite-hub runner pillar.show_pillar <node>` | works |
| `salt-run cache.grains <node>` | `halite-hub runner cache.grains <node>` | works |
| `salt-run fileserver.file_list` | `halite-hub runner fileserver.file_list` | works |
| `salt-run event.send <tag>` | `halite-hub runner event.send <tag>` | works |
| `salt-run survey.hash <jid>` | `halite-hub runner survey.hash <jid>` | works |
| `salt-run saltutil.refresh_pillar` | `halite-hub runner saltutil.refresh_pillar` | works |
| `salt-run state.orchestrate <sls>` | `halite-hub orch run <sls>` | works |
| `salt-run mine.get` | `halite-hub runner mine.get` | works |
| `salt-run queue.process_queue` | `halite-hub runner queue.process_queue` | not built |
| `salt-run net.find` | `halite-hub runner net.find` | not built |
| `salt-run fileserver.update` | `halite-hub runner fileserver.update` | works |
| `salt-run manage.bootstrap` | `halite-hub runner manage.bootstrap` | phase 5 |

Salt separates `manage.present`, `manage.alived`, and `manage.up`
because its transport cannot tell a live connection from a dead one
without asking. Here a node holds a stream to the hub or it does not, so
the three names answer one fact. They are all kept, so existing
orchestration reads unchanged.

A runner that ran and failed exits 1 and prints its error; only a
refusal, an unknown name, or a malformed call is a transport failure.

## The mine

`mine_functions` on a node computes values and publishes them; a state
on another node reads them. That is how a load balancer's configuration
learns its backend list.

```yaml
mine_interval: 60
mine_functions:
  network.ip_addrs:
    - eth0
  backend:
    mine_function: grains.get
    key: roles
    allow_tgt: 'lb*'
```

The store is on the hub. A node publishes under the identity on its
certificate and no other, which is what makes the answer worth
believing, and it never talks to another node directly — SPEC 5.1 has
nodes connect outward only.

| Salt | halite | Status |
|---|---|---|
| `mine_functions`, `mine_interval` | same | works |
| `mine.send` | same | works |
| `mine.update` | same, and it replaces | works |
| `mine.get` | same, authorized as a `node:` principal | works |
| `mine.delete`, `mine.flush`, `mine.valid` | same | works |
| `allow_tgt` | same, decided by the publisher | works |
| `salt-run mine.get` | `halite-hub runner mine.get '<tgt>' <fun>` | works |
| `salt-run mine.update` | `halite-hub runner mine.update` | works |
| `salt-run mine.flush`, `mine.delete`, `mine.valid` | same | works |
| `salt-run cache.mine` | `halite-hub runner cache.mine <node>` | works |
| `peer`, `peer_run` in the master config | the RBAC policy, deny by default | works | <!-- lexicon:allow -->
| a node publishing on another node's behalf | refused; the certificate decides | by design |
| `publish.publish`, `publish.runner` | node-initiated execution on other nodes | not built |

Reading the mine is the peer interface, and it is deny-by-default in the
one policy rather than in a separate configuration dialect:

```yaml
roles:
  backends-may-be-read:
    - target: 'web*'
      functions: ['backend']
bindings:
  - principal: 'node:lb1.example'
    roles: ['backends-may-be-read']
```

That grant reads `backend` from `web*` and nothing else — not
`credentials` from `web*`, and not `backend` from `*`.

`allow_tgt` is the publisher's own restriction and a second gate rather
than the only one: a node publishing something sensitive decides who may
see it without trusting every reader's policy to be right. An operator
is not restricted by it, having already been through the policy.

A full publication replaces, so a function taken out of
`mine_functions` stops being served rather than lingering.

## The scheduler

`schedule:` on a node runs jobs on a clock, with no hub involved. That
is how a node keeps itself converged, and it matters most exactly when
the hub cannot be reached.

```yaml
schedule:
  nightly_highstate:
    function: state.apply
    cron: '17 3 * * *'
    splay: 900
    maxrunning: 1
    return_job: True
  collect_inventory:
    every: 15m
    function: grains.items
    return_job: False
    run_on_start: True
```

| Salt | halite | Status |
|---|---|---|
| `schedule:` in the minion config | `schedule:` in the node config | works | <!-- lexicon:allow -->
| `cron: '17 3 * * *'` | same | works |
| `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`, `@reboot` | same | works |
| `seconds`, `minutes`, `hours`, `days` | same, and they add up | works |
| `every: 15m` | same | works |
| `when`, `once`, `once_fmt` | same; `once_fmt` is Python's strftime | works |
| `after`, `until` | same | works |
| `range` with `invert` | same | works |
| `skip_during_range`, `skip_explicit` | same | works |
| `splay`, as an integer or a start/end range | same | works |
| `maxrunning`, `return_job`, `run_on_start`, `enabled`, `offset`, `metadata` | same | works |
| no equivalent | `catchup: true` runs a missed job once on start | works |
| `timezone: <IANA name>` | same, from Go's embedded database | works |
| `salt-call schedule.list` | `halite-node call schedule.list` | works |
| no equivalent | `halite-node call schedule.show_next_fire_time name=…` | works |
| `schedule.add`, `modify`, `delete` | same | works |
| `schedule.enable`, `disable` | same, holding the whole schedule | works |
| `schedule.enable_job`, `disable_job` | same, holding one | works |
| `schedule.run_job` | same, out of turn and without splay | works |
| `schedule.save`, `reload` | same | works |
| `/etc/halite/schedule.d/` | same | works |
| schedules through pillar | same | not built |
| `L`, `W`, `#`, `?`, a seconds field in cron | refused by name | by design |
| `jid_include` | accepted and means nothing here | by design |

Two cron behaviours are worth stating because they surprise people:

**Both day fields restricted means either.** `0 0 1 * mon` runs on the
first of the month *and* on every Monday. That is what every cron does,
and a crontab moved here has to keep meaning what it meant.

**Daylight saving.** Schedules evaluate on the wall clock. An hour that
repeats runs a job in it once, not twice. An hour that is skipped runs a
job in it once, at the transition. SPEC 20.1 specifies both because they
are where missed runs come from.

A change made at runtime lives only in memory until `save` writes it to
`schedule.d/99-runtime.yaml` — a file of the node's own, numbered last
so it beats the files it was made against, and never over what a package
manager put there. `reload` re-reads the directory and discards runtime
changes that were never saved. `beacons.save` does the same for
`beacons.d/`.

A fragment in either directory is a mapping of names to definitions with
no `beacons:` or `schedule:` above them, because the directory already
says what they are. One written in the shape of the main configuration
file is refused, with the fix in the message.

`schedule.run_job` runs arbitrary code, so a wildcard in the RBAC policy
never grants it: the role has to name it. SPEC 23.5.

A scheduled job's return goes to whatever `returner:` names, which
defaults to `local`. See below.

## Returners

`returner:` in the node configuration says where a return goes. SPEC
20.3 marks six as Full and this build has all six:

| Returner | halite | Status |
|---|---|---|
| `local` | append-only NDJSON at `<state_dir>/returns.ndjson` | works |
| `local_cache` | the hub's job cache | works |
| `file` | NDJSON at `returner_file`, with rotation | works |
| `syslog` | RFC 5424, local socket or TCP, optionally over TLS | works |
| `webhook` | HTTPS POST, signed, retried, spooled | works |
| `smtp` | one mail per return | works |
| `mysql`, `postgres`, `redis`, `elasticsearch`, `splunk`, `slack`, `kafka`, `sqs`, and the rest | extensions of kind `returner` | works |

The seventeen bridged destinations are extensions of kind `returner`.
`returner: postgres` finds the `postgres` extension by name, so you do
not have to know it is one. Each needs a database driver or a vendor
client, which is why they are not linked in.

A returner name this build does not have is **not** fatal at startup.
It becomes a returner that fails every return with the reason, said once
when the node starts. That is deliberate: a returner extension arrives
through `saltutil.sync_returners`, which needs a running node, so a node
that refused to start could never be sent the thing it is waiting for.
It does not fall back to `local` either — that would put the estate's
returns in a file nobody is watching while the configuration says they
are in a database.

A genuine misconfiguration — an `http://` webhook url, a missing secret
— is still fatal, because you can fix that by reading.

The webhook returner is the one worth reading about:

```yaml
returner: webhook
returner_webhook_url: https://ci.example/halite/returns
returner_webhook_secret_file: /usr/local/etc/halite/returner.secret
returner_webhook_ca_file: /usr/local/etc/halite/internal-ca.pem
returner_webhook_attempts: 5
returner_spool_max_size: 268435456
```

The signature is HMAC-SHA-256 over the timestamp and the raw body
together, in `X-Halite-Signature` with `X-Halite-Timestamp` — the same
construction the API's webhook ingress verifies, so an estate that has
written one verifier has written both.

A delivery that fails is retried with backoff and then spooled to disk.
The backlog goes out ahead of new returns, so the receiver sees them in
the order they happened. A 4xx is not retried: the receiver understood
and refused, and retrying forever fills a disk with a request nobody
will accept. A full spool refuses rather than making room, because a
spool that silently discards is the failure it exists to prevent.

The url must be `https://`. A return carries whatever the job printed,
and `returner_webhook_ca_file` is there so an internal CA works without
anyone reaching for a way to skip verification.

### Shipping the event stream

`event_return:` on the **hub** sends the whole bus to a returner, which
SPEC 20.3 calls the recommended path to a SIEM:

```yaml
event_return: syslog
event_return_tags: 'halite/job/**,halite/state/**'
returner_syslog_address: siem.example:6514
returner_syslog_tls: true
returner_syslog_ca_file: /usr/local/etc/halite/siem-ca.pem
```

It resumes from a bus offset, so a receiver that was unreachable for an
hour catches up rather than leaving an hour-shaped hole. A delivery
failure does not advance the offset. `event_return_from: earliest` ships
the backlog on a first run; the default is `latest`, because shipping a
month of history into a SIEM on first boot is a bill and an alert storm.

`local_cache` is refused here: it would be the bus writing into the
cache of the hub that owns the bus.

## Beacons

`beacons:` in the node configuration starts the watchers. What they see
goes to the hub's bus, where a reactor can act on it. The schema is
Salt's, including the list-of-single-key-mappings form.

```yaml
beacons:
  diskusage:
    - /: 85%
    - /var: 90%
    - interval: 60
  service:
    - services:
        nginx:
          onchangeonly: True
      disable_during_state_run: True
    - interval: 5
  filechanges:
    - files:
        - /etc/nginx/nginx.conf
    - interval: 10
    - onchangeonly: True
```

A beacon fires under `halite/node/<node_id>/<beacon>/<what>`, so a
reactor can watch one filesystem rather than all of them. A path becomes
the tag's tail with its leading slash removed; the root filesystem is
`root`, because a tag that ends at the beacon's own name cannot be
reached by `diskusage/**`.

| Salt | halite | Status |
|---|---|---|
| `beacons:` in the minion config | `beacons:` in the node config | works | <!-- lexicon:allow -->
| `diskusage` | same | works |
| `load` | same | works |
| `memusage` | same | works |
| `service` | same | works |
| `cert_info` | same | works |
| `status` | same | works |
| no portable file watcher | `filechanges`, polling on digest and metadata | works |
| `interval`, `onchangeonly`, `delay`, `emitatstartup` | same | works |
| `disable_during_state_run` | same | works |
| no equivalent | `rate_limit`, `coalesce_window`, `queue_depth` per beacon | works |
| `salt-call beacons.list` | `halite-node call beacons.list` | works |
| no equivalent | `halite-node call beacons.list available=True` | works |
| `beacons.add`, `modify`, `delete` | same | works |
| `beacons.enable`, `disable` | same, holding every beacon | works |
| `beacons.enable_beacon`, `disable_beacon` | same, holding one | works |
| `beacons.save`, `reset` | same | works |
| `/etc/halite/beacons.d/` | same | works |
| beacons through pillar | same | not built |
| `inotify`, `fanotify` | `filechanges` polls instead | needs `golang.org/x/sys` |
| `watchdirs`, `eventlog` | same | phase 5, Windows |
| `fsevents` | same | phase 5, macOS |
| `swapusage`, `cpuusage`, `network_info`, `proc`, `ps`, `log`, `wtmp`, `btmp` | same | not built |

A beacon that this build does not have, or that is declared and not
built, stops the node rather than being skipped: a watcher that is
configured and does not run is indistinguishable from a quiet machine.

A beacon key that is not an identifier — `1m`, or a path like `/var` —
cannot be typed as a keyword argument, so `beacons.add` takes the
configuration as JSON instead:

```
halite-hub run '*' beacons.add name=load beacon_data='{"1m": [">", 2.0], "interval": 5}'
```

Simple keys can be typed inline: `beacons.add name=diskusage interval=60`.

The controls exist because beacon events are the classic self-inflicted
denial of service — a file that changes in a loop fires a beacon that
fires a reactor that changes the file. Each instance has a token bucket,
identical events inside `coalesce_window` become one carrying a
`_count`, the queue is bounded and reports what it dropped, and
`disable_during_state_run` stops a state run from triggering itself. The
hub breaks a causality chain longer than `max_causality_depth`.

## Reactors

`reactor:` in the hub configuration maps an event tag glob to the
reaction SLS the hub runs when a matching event arrives. The SLS syntax
is Salt's, and so are the four reaction types.

```yaml
reactor:
  - 'halite/node/*/start':
      - /srv/reactor/start.sls
  - tag: 'halite/beacon/**'
    sls:
      - /srv/reactor/beacon.sls
    principal: 'reactor:beacons'
    debounce: 5s
    dedupe_window: 30s
    dedupe_key: 'path'
    rate_limit: 600/m
```

The first form is Salt's and an existing configuration uses it. The
second carries the SPEC 18.2 controls and the principal, which the first
has nowhere to put.

```yaml
# /srv/reactor/beacon.sls
{% set node = data['id'] %}

restart_nginx:
  local.service.restart:
    - tgt: {{ node }}
    - arg:
      - nginx

record_it:
  runner.event.send:
    - args:
        tag: halite/audit/nginx_restarted
        data:
          node: {{ node }}
```

| Salt | halite | Status |
|---|---|---|
| `reactor:` in the master config | `reactor:` in the hub config | works | <!-- lexicon:allow -->
| `local.<function>` | same | works |
| `runner.<function>` | same | works |
| `wheel.<function>` | same, against one hub-function namespace | works |
| `caller.<function>` | same, on the node that fired the event | works |
| `salt-run reactor.list` | `halite-hub runner reactor.list` | works |
| no equivalent | `halite-hub runner reactor.test --tag … --data …` | works |
| the reactor runs with full master privilege | each entry names a `principal`, denied by default | works | <!-- lexicon:allow -->
| single-threaded and serialized | worker pool, bounded queue, ordering by causality chain | works |
| no equivalent | `debounce`, `dedupe_window`, `rate_limit` per glob | works |
| a reaction that fails is silent | `halite/reactor/error` on the bus | works |
| no equivalent | `max_causality_depth` breaks a beacon-reactor loop | works |
| a lossy bus, so a restart loses events | the reactor records its offset and resumes | works |

The template context is `data`, `tag`, and `id`. `salt` is absent: SPEC
25.5 restricts the hub's dispatcher to a named safe set, and this build
gives a reaction none rather than one that has not been audited against
that list.

A reaction is authorized as its principal, so nothing runs until a
policy binds that principal and says what it may do. `reactor.test`
prints the decision alongside the plan, because "it renders" and "it is
permitted" are different questions and the second is the one that fails
at three in the morning.

## Orchestration

`halite-hub orch run <sls>` is the old `salt-run state.orchestrate`. The
SLS syntax is Salt's, and so is the meaning: an orchestration here is a
state run whose modules act on the fleet, compiled and executed by the
same compiler and runner a node uses. `require`, `onfail`, `prereq`, and
ordering mean exactly what they mean in a highstate.

```yaml
{% set version = pillar['version'] %}

drain_lb:
  salt.function:
    - name: lb.drain
    - tgt: 'lb*.prod'

deploy_web:
  salt.state:
    - tgt: 'web*.prod'
    - sls:
      - webserver.deploy
    - pillar:
        version: {{ version }}
    - batch: 20%
    - require:
      - salt: drain_lb

rollback:
  salt.state:
    - tgt: 'web*.prod'
    - sls:
      - webserver.rollback
    - onfail:
      - salt: deploy_web
```

An orchestration template sees the `pillar` the caller passed and
nothing else — `--pillar '{"version":"1.2"}'` — because the hub is not a
node and has no pillar of its own.

| Salt | halite | Status |
|---|---|---|
| `salt-run state.orchestrate <sls>` | `halite-hub orch run <sls>` | works |
| `salt-run state.orch <sls>` | `halite-hub orch run <sls>` | works |
| `salt-run state.orchestrate_show_sls <sls>` | `halite-hub orch lint <sls>` | works |
| `salt.state`, `salt.sls`, `salt.highstate` | same | works |
| `salt.function` | same | works |
| `salt.runner`, `salt.wheel` | same, against one hub-function namespace | works |
| `salt.wait_for_event` | same | works |
| `fail_minions` | `tolerate_failures`, old name accepted | works |
| `batch`, `subset`, `batch_safe_limit` per step | same | works |
| `timeout` per step | same, the per-state option of SPEC 11.7 | works |
| `require`, `onfail`, `onchanges`, `prereq` between steps | same | works |
| no equivalent | `halite-hub orch show <jid>` | works |
| no equivalent | `halite-hub orch list` | works |
| no equivalent | `halite-hub orch resume <jid> --from <step>` | works |
| `salt.parallel`, `parallel` per step | refused by name | not built |
| `queue` per step | refused by name | not built |
| `salt-run state.pause` / `state.resume` | hold a running orchestration | not built |

Every step is authorized twice: once as the orchestration, and again as
the job it dispatches. Permission to run an orchestration is not
permission to run whatever it happens to name.

`--test` sends state steps out with `test` set, because a state honours
test mode by contract (SPEC 11.6). It does not dispatch an execution
function at all: `salt.function` runs whatever it names, and finding out
what a test run would do by running it is how a test run becomes a
deployment.

`orch run` exits 0 when every step succeeded and 1 when one failed — and
prints the timeline either way, because the next command is
`orch resume --from <step>` and it needs the step's name.

## The HTTP API

`halite-api serve` is the old `salt-api`. It is a client of the hub, not
a component of it: it holds its own operator certificate, and its worst
case is bounded by the policy that certificate is bound to. In Salt the
API process loads the control plane's configuration and calls into its
internals, so a flaw in the API is a flaw in the control plane. <!-- lexicon:allow -->

```yaml
# api.yaml
listen: 127.0.0.1:4511
hub: hub.example
tls_cert: /etc/halite/pki/api.crt
tls_key: /etc/halite/pki/api.key
api_operator: api
accounts: /etc/halite/accounts.yaml
token_lifetime: 12h
token_idle: 4h
```

| Salt | halite | Status |
|---|---|---|
| `salt-api` | `halite-api serve` | works |
| `POST /login` | `POST /v1/login` | works |
| no equivalent | `POST /v1/logout`, revoking the presented token | works |
| `GET /token` | `GET /v1/token`, introspecting the presented token | works |
| no equivalent | `GET /v1/schema`, the module signatures | works |
| no equivalent | `GET /v1/healthz`, `GET /v1/readyz` | works |
| `eauth: pam` | local accounts, PBKDF2-HMAC-SHA-512 | works |
| no equivalent | TOTP second factor, RFC 6238 | works |
| `eauth: oidc` | `POST /v1/login/oidc`, Authorization Code with PKCE | works |
| no equivalent | `POST /v1/login/oidc/token`, a token you already hold | works |
| `eauth: ldap` | same, bind-only over LDAPS or StartTLS | works |
| `POST /run` | `POST /v1/run`, synchronous | works |
| `POST /minions` | `POST /v1/jobs`, asynchronous | works | <!-- lexicon:allow -->
| `GET /jobs`, `GET /jobs/{jid}` | same under `/v1/` | works |
| no equivalent | `DELETE /v1/jobs/{jid}`, killing it | works |
| `GET /minions` | `GET /v1/nodes`, with grains and connection state | works | <!-- lexicon:allow -->
| `GET /minions/{id}` | `GET /v1/nodes/{id}` | works | <!-- lexicon:allow -->
| `POST /minions` for one | `POST /v1/nodes/{id}/state` | works | <!-- lexicon:allow -->
| `GET /keys`, `POST /keys` | same under `/v1/`, subject to RBAC | works |
| no equivalent | `POST /v1/orch`, `GET /v1/orch/{jid}` | works |
| no equivalent | `GET /v1/pillar/{id}`, behind a named permission | works |
| `client: local`, `local_async`, `local_batch` | same | works |
| `client: runner`, `runner_async`, `wheel`, `wheel_async` | same, one hub namespace | works |
| `client: ssh` | same | phase 5 |
| `GET /events` | SSE, and a WebSocket at `/v1/ws/events` | works |
| `POST /hook/{path}` | webhook ingress, always authenticated | works |
| no equivalent | `GET /v1/metrics`, this service's and the hub's | works |

A token is 256 bits from `crypto/rand`, stored as a SHA-256 digest, with
an absolute expiry, an idle expiry, the roles frozen at issue, and an
optional source network. It is returned once, at login, and never
appears in a log, an event, an error, or a URL — so it goes in the
`Authorization: Bearer` header and nowhere else.

`halite-api token list|show|revoke|prune` is the operator's side of it.
`revoke --principal <name>` withdraws every token an identity holds,
which is what disabling an account means for the tokens already issued.

`halite-api account hash` produces the verifier to paste into the
account file. The password is read from standard input, never from an
argument — an argument reaches the process table and the shell history.

Every login failure gives one message however it failed. Which of the
three it was is in the log: the difference between "no such account" and
"wrong password" is the difference between a guess and a confirmed name.

Every request is authorized twice. The operator behind the token is
authorized at the API, against the same policy file the hub uses;
without that, logging in would hand out the service's whole authority.
The service then forwards under its own certificate and the hub
authorizes that. An estate that grants the API less than the sum of its
operators gets exactly that, which is a control rather than an accident:

```yaml
roles:
  api-service:
    - target: '*'
      functions: ['test.ping', 'state.apply']
    - runners: ['jobs.*', 'manage.*', 'pillar.show_pillar']
bindings:
  - principal: 'cert:CN=api'
    roles: ['api-service']
```

The roles a token was *issued* with are what decide, so a role granted
after the token was handed out does not widen it, and one taken away is
a reason to revoke rather than a change that applies mid-session.

A job submitted through the API records both identities: `submitter` is
the service's certificate, which is what the hub authorized, and
`on_behalf_of` is the operator, which is recorded and never trusted. The
hub reads identity from the connection and nothing from the body.

### Logging in through a directory

`eauth: ldap` on `/v1/login`, because unlike OIDC it is a username and a
password — the request shape Salt's clients already send.

```yaml
ldap_address: dir.example:636
ldap_tls: ldaps
ldap_ca_file: /usr/local/etc/halite/internal-ca.pem
ldap_bind_dn: cn=halite,ou=services,dc=example,dc=com
ldap_bind_password_file: /usr/local/etc/halite/ldap.secret
ldap_user_base_dn: ou=people,dc=example,dc=com
ldap_user_filter: '(uid=%s)'
ldap_member_of_attribute: memberOf
ldap_role_map:
  platform: [operator]
  sre-oncall: [operator, responder]
```

`%s` is replaced with the username, escaped per RFC 4515. It is never
put into a DN: the directory's DN syntax is not something a login form
should be able to write.

For a directory that does not publish `memberOf`, search for the groups
instead — or configure both, and the results are merged:

```yaml
ldap_group_base_dn: ou=groups,dc=example,dc=com
ldap_group_filter: '(member=%s)'
ldap_group_attribute: cn
ldap_nested_depth: 3
```

`ldap_nested_depth` follows a group's own memberships, which Active
Directory needs. Zero, the default, is one level. A membership cycle
terminates rather than hanging.

There is no plaintext mode. `ldap_tls` is `ldaps` or `starttls`, and a
StartTLS the directory refuses ends the login rather than continuing in
the clear. Anonymous bind is refused in both directions: the service
account is required, and an empty password is refused before the
directory is asked — RFC 4513 makes an empty password an anonymous bind,
which a directory answers success to.

Referrals are not chased. Following one means authenticating against a
server the estate did not configure.

The principal is `ldap:<username>`, or `ldap:<attribute>` when
`ldap_principal_attribute` names one:

```yaml
bindings:
  - principal: 'ldap:ed'
    roles: ['operator']
```

Every failure gives the operator one message. Which it was — invalid
credentials, no such user, a malformed request, the directory refusing,
the directory unreachable — is the `reason` field in the log. Alert on
`directory_unreachable`; a blank password field is `malformed_request`
and is not an outage.

### Logging in through an identity provider

SPEC 23.4 calls this the modern path. Configure it on the API:

```yaml
oidc_issuer: https://idp.example/realms/estate
oidc_client_id: halite
oidc_client_secret_file: /usr/local/etc/halite/oidc.secret
oidc_redirect_url: https://api.example:4511/v1/login/oidc/callback
oidc_ca_file: /usr/local/etc/halite/internal-ca.pem
oidc_groups_claim: groups
oidc_principal_claim: sub
oidc_role_map:
  platform-team: [operator]
  sre-oncall: [operator, responder]
```

`oidc_groups_claim` is a colon-delimited path because providers
disagree: `groups` for Okta and Entra, `resource_access:halite:roles`
for Keycloak. `oidc_principal_claim` defaults to `sub`, which is stable
and opaque; `email` and `preferred_username` are readable and can be
reassigned to somebody else, which is why they are not the default.

A group with no entry in `oidc_role_map` grants nothing. That is
deliberate — the provider's directory is not this estate's authorization
model — and an estate that sets an issuer with no map is refused at
startup rather than letting every operator discover it one at a time.

The interactive flow is three requests:

```sh
# 1. start; send the operator to the url that comes back
curl -sk -X POST https://api.example:4511/v1/login/oidc -d '{}'

# 2. they come back to the redirect with ?code=…&state=…
# 3. finish
curl -sk -X POST https://api.example:4511/v1/login/oidc/callback \
  -d '{"code":"...","state":"..."}'
```

A pending login lives ten minutes and a `state` is good once, so an
authorization response replayed a second time finds nothing waiting.

A CI job has no browser and presents a token it already holds:

```sh
curl -sk -X POST https://api.example:4511/v1/login/oidc/token \
  -d "{\"token\":\"$ID_TOKEN\"}"
```

Either way the answer is one of this service's own tokens, with the
mapped roles frozen into it. The session never outlives the assertion:
a provider that expires a token in ten minutes has said how long it
trusts the operator, and this service does not extend that.

The principal is `oidc:<claim>`, which the RBAC policy binds like any
other:

```yaml
bindings:
  - principal: 'oidc:8a7f-…'
    roles: ['operator']
```

Accepted signing algorithms are the nine SPEC 23.4 names — `RS*`, `PS*`,
`ES*`. `none` is refused and so is every `HS*`, which closes the
algorithm confusion attack. A token without an `exp` is refused.

The event stream is one stream with two transports. `GET /v1/events` is
SSE whose `id:` is the bus offset, so a client that reconnects with
`Last-Event-ID` resumes where it stopped rather than at "now" — the
difference between an audit trail and a sample of one.
`GET /v1/ws/events` carries the same events over a WebSocket for a
client that would rather have one; it is pinged every thirty seconds so
an intermediary does not close a quiet stream.

Both take repeated `tag=` globs, and `from=latest|earliest|<offset>`.
Both apply the same filter: a tag naming a node reaches only a caller
whose policy targets that node, an event about no node in particular
reaches any caller the policy grants something, and a principal bound to
nothing sees nothing. Watching the bus needs `event.listen` in the
role's `runners:` list.

```
curl -N -H "Authorization: Bearer $TOKEN" \
  'https://api.example:4511/v1/events?tag=halite/job/**&from=latest'
```

A webhook is configured, never improvised — there is no setting that
produces an unauthenticated hook, and one with no credential is refused
when the configuration loads rather than served:

```yaml
hooks:
  deploy:
    auth: hmac
    secret_file: /usr/local/etc/halite/hooks/deploy.secret
    principal: 'hook:deploy'
    content_types: ['application/json']
    replay_window: 5m
```

The signature is HMAC-SHA-256 over the timestamp and the raw bytes
together, sent as `X-Halite-Signature` with `X-Halite-Timestamp`. A
delivery outside the replay window is refused, and one whose nonce has
already been accepted is refused. `auth: token` and `auth: mtls` are the
other two modes.

A delivery becomes an event under `halite/hook/<path>` carrying
`_principal`, the identity the delivery authenticated as, so a reaction
authorizes on that and never on the payload — the payload is written by
whoever sent it. The `principal` a hook names is an ordinary RBAC
identity: it gets a binding in the policy file like any other, and a
hook that may cause a deployment says so there.

The nonce is recorded once the delivery has landed on the bus, not when
the signature verifies. A delivery that fails downstream is one the
sender will retry with the same signature, and refusing that as a replay
would turn a transient fault into the lost event a webhook exists to
prevent.

`GET /v1/pillar/{id}` needs the role to name `pillar.show_pillar`
literally. Reading one node's compiled pillar is reading its secrets,
and a role written to let someone restart a service must not carry it
because the list said `*`. SPEC 22.1 calls this a distinct
high-privilege permission and it is enforced as one.

## Extensions

Salt's extensibility is a Python file dropped in `_modules/` on the file
server, which the agent imports and runs in process, as root, with no
signature requirement. SPEC 24.1 calls that a code distribution channel.
An extension here is a separate signed executable.

| Salt | halite | Status |
|---|---|---|
| `_modules/`, `_states/` on the file server | a signed extension bundle | works |
| no equivalent | `migrate --bridge-skeleton` generates one from the Python | works |
| the agent imports it in process | a separate process, sandboxed | works |
| no signature requirement | Ed25519 over a Merkle root, verified every load | works |
| whatever the file server serves | pinned by version and digest | works |
| no equivalent | `sys.list_extensions` | works |
| `saltutil.sync_all` | fetch signed bundles from `_ext/` | works |

### Porting a formula that carries one

A community formula with `_modules/` or `_states/` is not portable
without conversion, and there is no way around that. What the migration
tool does is make the job bounded:

```sh
halite-hub migrate /srv/salt --bridge-skeleton ./bridges
```

It writes one Go command per Python module, with the function names,
parameters, and defaults filled in from the source. `__virtualname__` is
honoured — that is the name a state calls the module by, so it is the
name the bridge answers to — and `_private` functions and `__virtual__`
are skipped, as Salt's own loader does.

Every generated function returns an error until it is written. A bridge
that was generated and forgotten fails loudly rather than answering
nothing and looking as though it worked. A file that already exists is
never overwritten, so a second migration does not discard the work done
after the first.

`_utils/` and the other Python import targets get no skeleton: they are
not extension points, and whatever imported them has to carry what they
did.

### What an extension is

An executable speaking length-prefixed JSON over stdio. The host sends
`hello` and `call`; the extension answers `hello_ok` and `result`, and
may send `log`, `progress`, and `event` frames in between.

Concurrency is a process pool, so an extension never has to be
thread-safe. A hung one is killed — SIGTERM, then SIGKILL, then a fresh
process — so it cannot hang the agent. A protocol violation kills the
process rather than failing the call: an extension that sent something
unreadable has lost its place in the stream.

### Fetching one

Bundles are published on the file server under `_ext/<name>/<version>/`
and fetched with `saltutil.sync_all`, or one of the per-kind variants:
`sync_modules`, `sync_states`, `sync_grains`, `sync_beacons`,
`sync_returners`, `sync_renderers`.

```sh
halite-hub run '*' saltutil.sync_all
```

**Synchronizing fetches; it does not load.** That is the behavioural
difference SPEC 24.5 states plainly, and it is the point of the section:
in Salt, `saltutil.sync_all` means "the agent will now execute new code
from the file server". Here it means "the agent has fetched signed,
pinned bundles", and what is running does not change until the node
restarts. The answer says so when anything arrived.

A bundle is fetched into a staging directory and verified there, and
moved into the cache only if it verifies. One that fails is left out
entirely rather than replacing what is there — a node running a good
version must not lose it because somebody published a bad one.

A bundle published at one path and signed as another is refused: trusting
the path would let a bundle be installed under a name it was not signed
for. An extension pinned to a different version is not fetched at all.

### Trusting one

```yaml
extension_dir: /var/db/halite/ext
extension_trust_keys:
  - 'release AAAAC3NzaC1lZDI1NTE5AAAAI...'
extension_require_signature: true
extension_pins:
  vault:
    version: 2.1.0
    root: 2215bdf9125187586535be3e253c9ff4640f7f4a5e6e0b8ac8088b7a8f8332fc
```

Both halves of the pin matter. The version is a label the publisher
controls; the digest is not.

`extension_require_signature: false` permits an *absent* signature for
development and warns on every load. A signature that is present and
wrong is refused whatever the setting says — that is tampering or a key
rotation nobody finished, and neither is a development convenience.

Verification happens on every load, not once at fetch, and it runs in
both directions: a listed file whose digest is wrong is tampering, and
an unlisted file that is present is one nobody signed, sitting in a
directory the extension can load from.

### What confines one

```yaml
extension_user: _halite_ext
extension_group: _halite_ext
extension_timeout: 60s
extension_pool_size: 4
```

`sys.list_extensions` reports what is *actually* enforced on the machine
in front of you, rather than what SPEC 24.3's table hopes for across
five operating systems. On this build that is the process boundary, a
dropped identity, a process group so a kill takes the children too, and
resource limits — with Landlock, seccomp, `pledge`, `unveil`, and
Windows job objects named as not built.

The resource limits are carried in the environment and applied by the
child, because `setrlimit` applies to the calling process. They hold for
an extension built against this protocol and not for an arbitrary one,
and the description says so.

`RLIMIT_AS` is available and off by default. It bounds *virtual* address
space, and a garbage-collected runtime reserves far more of that than it
commits: a Go extension under a 512 MiB limit — which reads as generous
— dies with "out of memory" after about 160 MiB.

Nothing is granted that was not declared in the signed manifest. An
extension declaring `network` gets it; one declaring `root` is not
dropped to another account. A declaration this build does not understand
is refused rather than ignored.

An extension's functions are marked `arbitrary_code`, so a wildcard in
the RBAC policy never grants one — the role has to name it. A name that
collides with a built-in is refused rather than overriding it.

## Targeting

The compound grammar is the same. On the command line it belongs to the
hub, so it arrives with the transport; in a top file it works today.

| Salt | halite | Status |
|---|---|---|
| `-G 'os:FreeBSD'` | same | in a top file |
| `-E 'web.*'` | same | in a top file |
| `-L 'web1,web2'` | same | in a top file |
| `-C 'G@os:FreeBSD and web*'` | same | in a top file |
| `-N group` (nodegroup) | same | in a top file |
| `-I 'role:web'` (pillar) | same in a state top; refused in a pillar top | see below |
| `- match: grain` in a top file | same | works |
| `- ignore_missing: True` | same, and honoured in a pillar top as Salt honours it | works |

A pillar top file may not target on pillar, which does not exist yet
while pillar is being compiled, and may target on a grain only if the
grain is named in `pillar_trusted_grains`. SPEC section 12.4.

## Configuration

| Salt | halite |
|---|---|
| `/etc/salt/minion` | `<config root>/node.yaml` | <!-- lexicon:allow -->
| `/etc/salt/master` | `<config root>/hub.yaml` | <!-- lexicon:allow -->
| `/etc/salt/minion.d/` | `<config root>/node.d/` | <!-- lexicon:allow -->
| `master:` | `hub:` | <!-- lexicon:allow -->
| `id:` | `node_id:` |
| `gpg_keydir:` | `gpg_home:` |
| `cachedir:` | `cache_dir:` |
| `pki_dir:` | same |

`<config root>` is `/usr/local/etc/halite` on a BSD and `/etc/halite` on
Linux; [the configuration reference](configuration.md) has the table.
Salt's own spellings are accepted with a warning naming the halite one,
so an existing file works while it is converted. Example files are in
[`contrib/examples/`](../contrib/examples/).

## Exit codes

Salt's `salt-call` returns 0 whether or not anything changed, and
`--retcode-passthrough` changes that inconsistently. halite's are fixed
and mean one thing each. SPEC section 11.8.

| Code | Meaning |
|---|---|
| 0 | Something changed. |
| 2 | Nothing to do; the node was already as declared. |
| anything else | The run failed. |

A monitor that treats 2 as failure will alert on every healthy run. See
[Operations](operations.md).
