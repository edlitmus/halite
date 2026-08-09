# halite

A Salt-inspired configuration management tool written in pure Go. Zero
runtime dependencies, zero external Go modules — everything is stdlib. One
static binary per platform.

halite targets the Salt 3008-era workflow (SLS state files, grains,
requisites, `test=True` dry runs, execution modules) without the Python
runtime, onedir/relenv packaging, or the deprecation treadmill.

**Status: v0.4.0 — masterless mode complete (P1), P2 in progress.**
Highstate with top.sls targeting, pillar, includes, the full requisite set
(require, watch, onchanges, prereq), templated sources, and file/pkg/
service/cmd/user/group/cron/sysctl modules. The master/agent transport
layer is the rest of P2; see [docs/salt-parity.md](docs/salt-parity.md)
for the roadmap.

## Why

* Salt's Python runtime is the single largest source of packaging pain
  (relenv/onedir, FreeBSD ports lag, CVE surface in bundled deps).
* A Go binary cross-compiles to FreeBSD, Linux, macOS, Windows, and
  OpenBSD from any host in seconds, with no interpreter on the target.
* The useful core of Salt — declarative states, facts, requisites,
  idempotent modules — is much smaller than Salt itself.

## Supported platforms

| Platform | Packages | Services | Status |
|----------|----------|----------|--------|
| FreeBSD | pkg(8) | rc.d + sysrc | first-class |
| Linux (Debian/Ubuntu) | apt | systemd / sysvinit | supported |
| Linux (RHEL/Fedora) | dnf / yum | systemd | supported |
| Linux (SUSE, Arch, Alpine) | zypper / pacman / apk | systemd | supported |
| macOS | Homebrew | launchd (partial) | supported |
| Windows | Chocolatey / winget | SCM (`sc`) | supported |
| OpenBSD | — | — | compiles; backends TBD |

## Build

```sh
git clone git@github.com:edlitmus/halite.git
cd halite
make            # builds ./bin/halite for the host
make cross      # builds dist/halite-<os>-<arch> for all targets
make test
```

Requires Go 1.22+. No other dependencies.

## Quickstart

```sh
# Show system facts (Salt: salt-call --local grains.items)
halite grains
halite grains -json

# Apply a state file (Salt: salt-call --local state.apply)
halite apply examples/webserver.sls

# Highstate from a state tree with top.sls (Salt: state.highstate)
halite apply -root /usr/local/etc/halite/states
halite apply -root ./states web.nginx        # dotted sls names work too

# Dry run (Salt: test=True)
halite apply -test examples/webserver.sls

# Run one state function ad hoc (Salt: salt-call --local pkg.install nginx)
halite call pkg.installed name=nginx
halite call file.managed name=/tmp/x contents=hello mode=0644

# Show the pillar data this host resolves to (Salt: pillar.items)
halite pillar
halite pillar -pillar-root ./pillar -json
```

## A state file

```yaml
install_nginx:
  pkg.installed:
    - name: nginx

{{ if eq .Grains.os_family "FreeBSD" }}
nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx.conf
    - mode: "0644"
    - require:
      - pkg: install_nginx
{{ end }}

nginx_service:
  service.running:
    - name: nginx
    - enable: true
    - watch:
      - file: nginx_conf
```

Same shape as Salt SLS. Templating is Go `text/template` instead of Jinja —
grains are available as `{{ .Grains.os_family }}`. See
[docs/writing-states.md](docs/writing-states.md).

## Documentation

* [docs/getting-started.md](docs/getting-started.md) — install, first state, workflow
* [docs/writing-states.md](docs/writing-states.md) — SLS format, templating, requisites
* [docs/states.md](docs/states.md) — state module reference (file, pkg, service, cmd)
* [docs/pki.md](docs/pki.md) — keys, certificates, and agent enrollment
* [docs/architecture.md](docs/architecture.md) — design decisions and internals
* [docs/salt-parity.md](docs/salt-parity.md) — Salt 3008 feature map and roadmap
* [examples/](examples/) — working state files

## Non-goals

* Bug-for-bug Salt compatibility. SLS files port with small edits
  (Jinja → text/template), not verbatim.
* Full YAML. The parser handles the subset SLS actually uses; anchors,
  flow collections, and multi-line scalars are out (see writing-states.md).
* Python module ecosystem. Custom modules are Go, compiled in.

## License

BSD 2-Clause (see LICENSE).
