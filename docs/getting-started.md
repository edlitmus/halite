# Getting started

halite is a configuration management system: it reads a tree of YAML
files describing how a machine should be, works out what is different,
and changes only that. This page takes you from nothing to a converged
machine.

Everything here works today. halite's hub — the part that drives a fleet
from one place — is phase 2 of the delivery plan in SPEC section 32, so
for now a node manages itself from a local tree, which is the mode Salt
calls masterless. It is the mode worth starting in either way: the tree
is the same tree a hub would serve, and you can watch it work.

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
key halite does not recognise is an error at startup, not a line that
quietly does nothing.

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
- [DIVERGENCE.md](DIVERGENCE.md) — what is not built yet, in detail.
