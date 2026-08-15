# halite

A Salt-inspired configuration management tool written in pure Go. Zero
runtime dependencies, zero external Go modules — everything is stdlib. One
static binary per platform.

halite targets the Salt 3008-era workflow (SLS state files, grains,
requisites, `test=True` dry runs, execution modules) without the Python
runtime, onedir/relenv packaging, or the deprecation treadmill.

**Status: v0.9.0 — P1–P5 complete, P6 (module breadth) in progress.** Three ways to run:

* **Masterless** — `halite apply` on the host. Highstate with top.sls
  targeting, custom grains, pillar, includes, the full requisite set with
  its `_in` forms and `names:` expansion, and 64 state functions across
  file/pkg/pkgrepo/pip/service/cmd/user/group/ssh_auth/host/jail/container/
  cron/sysctl/kmod/selinux/timezone/x509/archive/git/mount.
* **Fleet** — an mTLS HTTP/2 control plane, CSR-based enrollment, and
  targeted dispatch to agents.
* **Agentless** — `halite ssh` pushes the binary, runs, and cleans up.

Plus the event layer: a tagged event bus with a live stream, a reactor
turning events into jobs, agent-side beacons and a scheduler that keeps a
fleet converging on its own, durable returners, a mine of fleet-wide
facts, and ordered orchestration. What remains are the
deliberate non-goals and an honest list of known gaps against Salt
3008 — see [docs/salt-parity.md](docs/salt-parity.md).

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
| Windows | Chocolatey / winget | SCM (`sc`) | supported; scheduled tasks and registry unverified on real hardware |
| OpenBSD | — | — | compiles; backends TBD |

## Build

```sh
git clone git@github.com:edlitmus/halite.git
cd halite
make            # builds ./bin/halite for the host
make cross      # builds dist/halite-<os>-<arch> for all targets
make test       # go test ./...
make race       # the same under the race detector
make docs       # the documentation checks alone
make check      # vet, test, and race — what to run before calling it done
```

**Definition of done**: `make check` passes. That includes the checks in
`internal/docs`, which hold this repository's prose to its code — every
state function appears in the module reference, every command and flag is
documented, every internal link resolves, every example compiles, and the
counts quoted in the README and the parity map match the registry. A
change that leaves the documentation behind fails the build rather than
shipping.

Requires Go 1.25+ (the version go.mod declares). No other dependencies.

## Quickstart

```sh
# Show system facts (Salt: salt-call --local grains.items)
halite grains
halite grains -json

# Set the site's own facts, the ones targeting selects on (Salt: grains.setval)
halite grains set role=web datacenter=lax1

# Apply a state file (Salt: salt-call --local state.apply)
halite apply examples/webserver.sls

# Highstate from a state tree with top.sls (Salt: state.highstate)
halite apply -root /usr/local/etc/halite/states
halite apply -root ./states web.nginx        # dotted sls names work too

# Dry run (Salt: test=True)
halite apply -test examples/webserver.sls

# See the compiled plan without running it (Salt: state.show_sls)
halite show examples/webserver.sls
halite show -root ./states            # the whole highstate, in order

# Run one state function ad hoc (Salt: salt-call --local pkg.install nginx)
halite call pkg.installed name=nginx
halite call file.managed name=/tmp/x contents=hello mode=0644

# Show the pillar data this host resolves to (Salt: pillar.items)
halite pillar
halite pillar -pillar-root ./pillar -json

# Check an existing Salt tree before running anything against it
halite parse                                   # /srv/salt and /srv/pillar
halite parse -root ./states -pillar-root ./pillar
halite parse -errors -json
```

## Fleet mode

```sh
# On the control plane host
halite key init -cn "acme fleet ca"
halite key server master.example.com
halite key admin ed
halite master -root /usr/local/etc/halite/states

# On a managed host (ca.crt copied in out of band)
halite agent -master master.example.com -id web1

# Back on the control plane: accept the agent's request
halite key list && halite key accept web1

# Drive the fleet (Salt: salt '<target>' state.apply)
halite agents
halite run '*' state.highstate -test
halite run 'os_family:FreeBSD' state.apply web.nginx
halite run 'web*' call pkg.installed name=nginx
halite mine network.interfaces                  # facts agents published

# Watch what the fleet is doing, and run ordered work across it
halite events -tag 'halite/job/**'
halite orchestrate deploy

# Let each agent converge on its own clock (Salt: the minion scheduler)
halite agent -master master.example.com -schedule schedule.sls
```

mTLS 1.3 throughout, agent identity from the client certificate, and no
inbound connections to managed hosts. See [docs/fleet.md](docs/fleet.md).

## Agentless

No agent, no control plane — copy one binary, run it, clean up:

```sh
halite ssh web1,web2 state.highstate -test
halite ssh 'web*' state.apply web.nginx -roster hosts.txt
halite ssh '*' call disk.usage -roster hosts.txt
```

Pillar is rendered on your machine and only that host's data is shipped.
See [docs/agentless.md](docs/agentless.md).

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

## Coming from Salt

`halite parse` reads an existing state and pillar tree and reports, file by
file and line by line, what halite runs as written, what needs translating,
and what it does not support. It renders and compiles each file exactly as
`halite apply` would and prints where the pipeline stops — Jinja that has
to become `text/template`, block scalars and anchors the YAML subset does
not take, `salt://` sources, `require_in`, state modules that are not
implemented — with the translation for each. It exits non-zero when the
tree still holds something halite will not run, so it also works as a CI
gate while a conversion is in progress. See
[docs/migration.md](docs/migration.md).

## Documentation

* [docs/getting-started.md](docs/getting-started.md) — install, first state, workflow
* [docs/migration.md](docs/migration.md) — `halite parse`, and translating a Salt tree
* [docs/writing-states.md](docs/writing-states.md) — SLS format, templating, requisites
* [docs/states.md](docs/states.md) — state module reference (file, pkg, service, cmd)
* [docs/fleet.md](docs/fleet.md) — control plane, agents, targeting
* [docs/events.md](docs/events.md) — the event bus, reactor, returners, beacons, scheduler, mine
* [docs/orchestration.md](docs/orchestration.md) — ordered fleet-wide runs
* [docs/external-modules.md](docs/external-modules.md) — custom modules in any language
* [docs/agentless.md](docs/agentless.md) — `halite ssh`, rosters, bootstrapping
* [docs/pki.md](docs/pki.md) — keys, certificates, and agent enrollment
* [docs/pillar-security.md](docs/pillar-security.md) — protecting pillar data
* [docs/architecture.md](docs/architecture.md) — design decisions and internals
* [docs/salt-parity.md](docs/salt-parity.md) — Salt 3008 feature map and roadmap
* [examples/](examples/) — working state files, one per feature area ([index](examples/README.md))

## Non-goals

* Bug-for-bug Salt compatibility. SLS files port with small edits
  (Jinja → text/template), not verbatim — `halite parse` reports which.
* Full YAML. The parser handles the subset SLS actually uses; anchors,
  flow collections, and multi-line scalars are out (see writing-states.md).
* Python module ecosystem. Custom modules are Go, compiled in — or any
  language, as an external module (docs/external-modules.md).

## License

BSD 2-Clause (see LICENSE).
