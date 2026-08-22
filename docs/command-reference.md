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
| no equivalent | `halite-hub migrate /srv/salt --fail-on review` | works |
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

## Driving a fleet

None of this works yet: the transport is phase 2. Each command says so
rather than failing obscurely.

| Salt | halite | Status |
|---|---|---|
| `salt '*' test.ping` | `halite-hub run '*' test.ping` | phase 2 |
| `salt -G 'os:FreeBSD' state.apply` | `halite-hub run -G 'os:FreeBSD' state.apply` | phase 2 |
| `salt-run jobs.list_jobs` | `halite-hub jobs list` | phase 2 |
| `salt-run manage.up` | `halite-hub runner manage.up` | phase 2 |
| `salt-key -L` | `halite-hub keys list` | phase 2 |
| `salt-key -a web1` | `halite-hub keys accept web1` | phase 2 |
| `salt-cp '*' file /tmp/file` | `halite-hub files push` | phase 2 |
| `salt-ssh '*' test.ping` | `halite-hub ssh '*' test.ping` | phase 2 |
| `salt-call event.send` | `halite-node event send` | phase 2 |
| `salt-run state.orchestrate` | `halite-hub orch` | phase 3 |
| `salt-api` | `halite-api serve` | phase 4 |

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
