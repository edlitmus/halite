# Getting started

## Install

Build from source (Go 1.22+, no other deps):

```sh
git clone git@github.com:edlitmus/halite.git
cd halite && make
install -m 0755 bin/halite /usr/local/bin/
```

Or cross-compile for a fleet from any machine:

```sh
make cross
ls dist/
# halite-freebsd-amd64 halite-freebsd-arm64 halite-linux-amd64 ...
scp dist/halite-freebsd-amd64 host:/usr/local/bin/halite
```

There is nothing else to install on the target — no interpreter, no
site-packages, no onedir.

## First run

```sh
halite grains
```

You should see `os`, `os_family`, `osrelease`, `arch`, and friends. These
drive templating.

## First state

Create `motd.sls`:

```yaml
{{ if eq .Grains.os_family "FreeBSD" }}
motd:
  file.managed:
    - name: /etc/motd.template
    - contents: "{{ .Grains.host }} - FreeBSD {{ .Grains.osrelease }} - managed by halite"
{{ else }}
motd:
  file.managed:
    - name: /etc/motd
    - contents: "{{ .Grains.host }} - managed by halite"
{{ end }}
```

Dry-run it, then apply:

```sh
halite apply -test motd.sls
halite apply motd.sls
```

Re-apply and observe idempotency (`changed=0`).

## A realistic layout

```
states/
  top.sls
  webserver.sls
  base.sls
  files/
    nginx-freebsd.conf
    nginx-linux.conf
pillar/
  top.sls
  common.sls
```

`- source:` paths resolve relative to the SLS file, so `source:
files/nginx-freebsd.conf` works from anywhere. Add a `top.sls` at the
tree root and `halite apply -root states/` (or set `HALITE_ROOT`) gives
you a full highstate — see docs/writing-states.md for top-file targeting
and `include:`.

Host-specific values (ports, paths, credentials) go in the `pillar` tree
beside the states, and reach templates as `{{ .Pillar.x }}`. `halite
pillar` shows what the current host resolves to.

## Converging on a schedule

Masterless convergence with cron (FreeBSD):

```
# /etc/cron.d/halite or crontab -e
*/30 * * * * root /usr/local/bin/halite apply >> /var/log/halite.log 2>&1
```

Non-zero exit on failure makes it easy to alert from periodic(8) or your
monitoring agent.

## In jails and images

Because halite is one static binary, baking it into a Bastille template or
a Packer image is a `COPY` and a `RUN halite apply`. No bootstrap script,
no pip, no salt-bootstrap.

## Beyond one host

Everything above is masterless: halite on the host, converging itself.
That is the whole tool for image builds, jails, and cron. Two other ways
to run it share the same states, the same pillar, and the same engine:

* **Agentless** — [agentless.md](agentless.md). `halite ssh web1,web2
  state.highstate` copies the binary over ssh, runs it, and cleans up.
  Nothing is installed. This is also how you bootstrap agents onto hosts
  that do not have them.
* **Fleet** — [fleet.md](fleet.md). A control plane holds the trees and
  dispatches to long-running agents over mTLS: `halite run 'web*'
  state.apply web.nginx`. Adds [events](events.md) (a bus, a reactor,
  beacons, returners, the mine) and [orchestration](orchestration.md) for
  work that has to happen in an order across hosts.

A state file does not know or care which one is driving it. Start
masterless; move up when you have more hosts than patience.

## Migrating from Salt

1. `halite parse -root /srv/salt` — read the tree you have and get the
   list of what needs translating, file by file and line by line. Every
   finding names the change it needs ([migration.md](migration.md)).
2. `halite grains` — check the facts you template on exist (most common
   ones do; see docs/salt-parity.md).
3. Convert Jinja to text/template (cheat sheet in docs/writing-states.md).
4. Replace unsupported modules with `cmd.run` + `unless` as a bridge.
5. `halite parse -errors` until it is quiet, then run everything with
   `-test` before the first real apply.
6. Modules with no equivalent can be written as
   [external modules](external-modules.md) in any language, rather than
   left as `cmd.run`.

`salt-call --local` maps to `halite apply`, `salt '<tgt>' state.apply` to
`halite run '<tgt>' state.apply`, and `salt-ssh` to `halite ssh`. The
full map, including what is deliberately absent, is in
[salt-parity.md](salt-parity.md).
