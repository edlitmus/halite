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
| `salt-run fileserver.file_list` | `halite-hub run '*' cp.list_master` | phase 2 |
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
| `salt-api` event stream | SSE and WebSocket at `/v1/events` | phase 4 |

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
| `ext_pillar` | not built; the setting warns that it does nothing | phase 2 |

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
| `salt-cp '*' file /tmp/file` | `halite-hub files push` | phase 3 |
| `salt-run jobs.kill <jid>` | `halite-hub jobs kill <jid>` | works |
| no equivalent | `halite-hub jobs export <jid>` | works |
| `salt '*' --queue state.apply` | `halite-hub run '*' state.apply --offline queue` | works |
| `salt '*' saltutil.sync_grains` | pushed automatically on `grains_refresh_interval` | works |
| `salt-ssh '*' test.ping` | `halite-hub ssh '*' test.ping` | phase 5 |

| `salt-run state.orchestrate` | `halite-hub orch run <sls>` | works |
| `salt-api` | `halite-api serve` | phase 4 |

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
| `salt-run mine.get` | `halite-hub runner mine.get` | phase 3 |
| `salt-run queue.process_queue` | `halite-hub runner queue.process_queue` | phase 3 |
| `salt-run net.find` | `halite-hub runner net.find` | phase 3 |
| `salt-run fileserver.update` | `halite-hub runner fileserver.update` | phase 5 |
| `salt-run manage.bootstrap` | `halite-hub runner manage.bootstrap` | phase 5 |

Salt separates `manage.present`, `manage.alived`, and `manage.up`
because its transport cannot tell a live connection from a dead one
without asking. Here a node holds a stream to the hub or it does not, so
the three names answer one fact. They are all kept, so existing
orchestration reads unchanged.

A runner that ran and failed exits 1 and prints its error; only a
refusal, an unknown name, or a malformed call is a transport failure.

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
| `beacons.add`, `modify`, `delete`, `enable`, `disable`, `save`, `reset` | same | not built |
| `inotify`, `fanotify` | `filechanges` polls instead | needs `golang.org/x/sys` |
| `watchdirs`, `eventlog` | same | phase 5, Windows |
| `fsevents` | same | phase 5, macOS |
| `swapusage`, `cpuusage`, `network_info`, `proc`, `ps`, `log`, `wtmp`, `btmp` | same | not built |

A beacon that this build does not have, or that is declared and not
built, stops the node rather than being skipped: a watcher that is
configured and does not run is indistinguishable from a quiet machine.

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
| `salt.parallel`, `parallel` per step | refused by name | phase 3 |
| `queue` per step | refused by name | phase 3 |
| `salt-run state.pause` / `state.resume` | hold a running orchestration | phase 3 |

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
