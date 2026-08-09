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
  webserver.sls
  base.sls
  files/
    nginx-freebsd.conf
    nginx-linux.conf
```

`- source:` paths resolve relative to the SLS file, so `source:
files/nginx-freebsd.conf` works from anywhere. Add a `top.sls` at the
tree root and `halite apply -root states/` (or set `HALITE_ROOT`) gives
you a full highstate — see docs/writing-states.md for top-file targeting
and `include:`.

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

## Migrating from Salt

1. `halite grains` — check the facts you template on exist (most common
   ones do; see docs/salt-parity.md).
2. Convert Jinja to text/template (cheat sheet in docs/writing-states.md).
3. Replace unsupported modules with `cmd.run` + `unless` as a bridge.
4. Run everything with `-test` before the first real apply.
