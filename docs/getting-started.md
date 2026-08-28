# Getting started

halite is a configuration management system: it reads a tree of YAML
files describing how a machine should be, works out what is different,
and changes only that. This page takes you from nothing to a converged
machine.

Everything here works today. A node manages itself from a local tree,
which is the mode Salt calls masterless, and it is the mode worth
starting in either way: the tree is the same tree a hub would serve, and
you can watch it work.

A hub also works, and serves the tree: `halite-hub serve`,
`halite-node enroll`, and `halite-hub run '*' state.apply` are in
[Operations](operations.md#enrolling-a-node). An enrolled node compiles
against the hub's tree and pillar, so the copy this page puts on the
machine is the one you would later move to the hub.

## Install

halite is one static binary per program with no runtime dependencies.
Build it from source:

```sh
git clone https://github.com/edlitmus/halite
cd halite
make build
```

`bin/` now holds `halite-node`, `halite-hub`, and `halite-api`. Put
`halite-node` where the machine can find it:

```sh
sudo install -m 0755 bin/halite-node /usr/local/bin/
halite-node version
```

There is nothing else to install. No Python, no interpreter, no library:
`go list -m all` returns only this module, and the build asserts that on
every run.

### Installing

`make install`, as root, puts the binaries, the service files, and the
directories in place for the platform it is run on:

```sh
sudo make install
```

| | FreeBSD and the other BSDs | Linux |
|---|---|---|
| binaries | `/usr/local/bin` | `/usr/local/bin` |
| configuration | `/usr/local/etc/halite` | `/etc/halite` |
| durable state | `/var/db/halite` | `/var/lib/halite` |
| service files | `/usr/local/etc/rc.d` | `/etc/systemd/system` |

The binaries go to the same place on both because that is the path
written into the rc.d scripts and the systemd units.

It writes **no configuration** — a target that overwrote `hub.yaml`
would be one nobody could run twice — and starts nothing. Copy an
example from `contrib/examples/` and enable the service yourself.

The directories are created owned by the account the hub and the API run
as, which is the point of doing it here: a directory created by running
a command as root is one the service account cannot use afterwards, and
the symptoms name neither the directory nor the account. If the account
does not exist the target says so and names the command to create it,
rather than leaving root-owned directories behind quietly. If a `chown`
fails it stops, because `install` reports that on standard error and
still exits zero.

Every path is overridable, which is what makes it testable without root:

```sh
make install BINDIR=/tmp/stage/bin CONFDIR=/tmp/stage/etc/halite \
    STATEDIR=/tmp/stage/db CACHEDIR=/tmp/stage/cache \
    LOGDIR=/tmp/stage/log SERVICEDIR=/tmp/stage/rc.d
```

`make install-service` reinstalls only the rc.d scripts or the systemd
units, which is what to run after pulling a fix to them.
`make install-fips` adds the `-fips` artifacts of SPEC 27.4 beside the
ordinary ones; it does not install the systemd drop-ins, because those
change what a unit runs.

### Other platforms

`make cross` builds every target into `dist/`, named
`halite-node-<os>-<arch>`, and needs no toolchain for the target:

```sh
make cross
ls dist/halite-node-*
```

Where each platform keeps its files, which is where halite looks unless
a `--config` says otherwise:

| Platform | Configuration | State | Cache |
|---|---|---|---|
| Linux | `/etc/halite` | `/var/lib/halite` | `/var/cache/halite` |
| FreeBSD and the other BSDs | `/usr/local/etc/halite` | `/var/db/halite` | `/var/cache/halite` |
| macOS | `/etc/halite` | `/var/lib/halite` | `/var/cache/halite` |
| Windows | `%PROGRAMDATA%\Halite` | `%PROGRAMDATA%\Halite\lib` | `%PROGRAMDATA%\Halite\cache` |

macOS takes the Linux paths deliberately: Homebrew's prefix is not fixed,
so `/etc` is the honest default rather than a guess at one.

Read [DIVERGENCE 4](DIVERGENCE.md) before trusting a run on Windows or
macOS. The code cross-compiles for both and has not been run on either,
so the modules that matter there — `pkg`, `service`, the Windows event
log — are unexercised rather than known good. Linux and FreeBSD are the
platforms this build has been run on.

## The first tree

A tree has two halves. **States** describe how the machine should be.
**Pillar** holds the values that differ between machines.

```sh
sudo mkdir -p /srv/halite/states /srv/halite/pillar
```

The top file says which states apply to which machines:

```yaml
# /srv/halite/states/top.sls
base:
  '*':
    - motd
```

And one state:

```yaml
# /srv/halite/states/motd.sls
/etc/motd:
  file.managed:
    - contents: |
        This machine is managed by halite.
        Host: {{ grains['fqdn'] }}
        OS:   {{ grains['os'] }} {{ grains['osrelease'] }}
    - mode: '0644'
```

`{{ ... }}` is a template. `grains` are facts halite collects about the
machine — run `halite-node grains items --local` to see all of them.

## Look before you leap

Never apply a tree you have not read the plan for. `--test` runs every
state, changes nothing, and reports what it *would* do:

```sh
sudo halite-node state apply --local \
    --file-root /srv/halite/states \
    --pillar-root /srv/halite/pillar \
    --test
```

```
          ID: /etc/motd
    Function: file.managed
      Result: would change
     Comment: /etc/motd would be created: its contents, its mode.
     Changes:
              diff: |
                --- /etc/motd (current)
                +++ /etc/motd (managed)
                @@ -1,0 +1,3 @@
                +This machine is managed by halite.
```

That `would change` is a promise, not a guess. Every state module is held
to it by a shared conformance harness: in test mode it must make no
change, return no result, and say what it would have done. Salt has no
such harness, which is why `test=True` there is unreliable for a fair
number of its modules.

## Apply it

Drop `--test`:

```sh
sudo halite-node state apply --local \
    --file-root /srv/halite/states \
    --pillar-root /srv/halite/pillar
```

Run it again and nothing happens:

```
Succeeded: 1  Failed: 0  Total: 1
```

That is convergence, and it is the property everything else rests on. The
exit code tells a script which happened without parsing the output:

| Exit | Meaning |
|---|---|
| 0 | Something changed. |
| 2 | Already converged; nothing to do. |
| 1 or other | A state failed. |

A cron job that treats 2 as success will not page you for a machine that
had nothing to do.

## A configuration file

Passing roots on the command line gets old. Put them in a file:

```yaml
# /usr/local/etc/halite/node.yaml   (Linux: /etc/halite/node.yaml)
file_roots:
  base:
    - /srv/halite/states
pillar_roots:
  base:
    - /srv/halite/pillar
```

Then:

```sh
sudo halite-node state apply --local
```

Every setting is in the [configuration reference](configuration.md). A
key halite does not recognise is reported at startup rather than quietly
doing nothing:

```
configuration key "pillar_rots" is not recognised and was ignored
```

Fuller examples, one per shape of deployment, are in
[`contrib/examples/`](../contrib/examples/): a masterless node, a node
with a hub, the smallest file worth having, one each for the hub and the
API, a commented `policy.yaml` for the RBAC of SPEC 23.5, and an
`accounts.yaml` for the local accounts of SPEC 23.2. Each is loaded by a
test as the program it is written for — the policy and the accounts
through their own parsers, with the decisions their comments describe
asserted — so none of them can teach a setting, a grant, or an account
that does not exist.

The account example's password hashes cannot be logged into: each was
made from random bytes that were never recorded. A test proves it, so
the file cannot quietly acquire a working account.

## Pillar: what differs between machines

States describe the shape; pillar fills in the values.

```yaml
# /srv/halite/pillar/top.sls
base:
  '*':
    - common

# /srv/halite/pillar/common.sls
motd_owner: the platform team
```

```yaml
# in a state
    - contents: |
        Owned by {{ pillar['motd_owner'] }}
```

Pillar is compiled per machine and can be targeted the same way states
are, so `web*` gets different values from `db*`. What it may **not** be
targeted on is a machine's own grains, unless you allow specific ones:
grains come from the machine, so a machine that could target pillar on
its own grains could ask for another machine's secrets. See
`pillar_trusted_grains` in the configuration reference.

## Ordering

States run in the order they are written, and requisites change that
where it matters:

```yaml
nginx:
  pkg.installed: []

/etc/nginx/nginx.conf:
  file.managed:
    - source: salt://nginx/nginx.conf
    - require:
      - pkg: nginx

nginx_service:
  service.running:
    - name: nginx
    - watch:
      - file: /etc/nginx/nginx.conf
```

`require` means "after, and only if it succeeded". `watch` means that
plus "and restart me if it changed". `halite-node state show_lowstate
--local` prints the whole ordered run before any of it happens.

## Where to go next

- [Writing states](states.md) — the declaration form, requisites, and
  the test-mode contract.
- [Operations](operations.md) — running halite on a schedule, service
  files, logging, and what the exit codes mean.
- [Migrating from Salt](migrating-from-salt.md) — the audit tool, and
  what is deliberately different.
- [Module reference](modules.md) — every function this build ships.
- [Command reference](command-reference.md) — every Salt command and its halite equivalent.
- [DIVERGENCE.md](DIVERGENCE.md) — what is not built yet, in detail.
