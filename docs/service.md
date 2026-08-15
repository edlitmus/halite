# Running the daemons as services

`halite master` and `halite agent` run in the foreground and log to
stderr, which is what an init system wants. This page is the config files
they read and the FreeBSD rc.d scripts that start them.

## Config files

A daemon reads its settings from a file so that a command line does not
have to live in `rc.conf`:

| Daemon | File |
|---|---|
| `halite master` | `/usr/local/etc/halite/master.conf` (FreeBSD), `/etc/halite/master.conf` (Linux) |
| `halite agent` | `/usr/local/etc/halite/agent.conf`, `/etc/halite/agent.conf` |

`-config FILE` names another. **A missing file is not an error** — running
entirely on flags keeps working, which is what the examples in
[fleet.md](fleet.md) do.

Every setting is a flag without its dash, so `halite master -h` and
`halite agent -h` are the reference:

```yaml
# /usr/local/etc/halite/master.conf
addr: ":4506"
root: /usr/local/etc/halite/states
pillar-root: /usr/local/etc/halite/pillar
pki: /usr/local/etc/halite/pki

# A repeatable flag is a list.
returner:
  - file:/var/log/halite/results.ndjson
  - webhook:https://example.com/halite

reactor: /usr/local/etc/halite/reactor.sls
```

```yaml
# /usr/local/etc/halite/agent.conf
master: master1.example.com,master2.example.com
pki: /usr/local/etc/halite/pki
cache: /var/cache/halite
beacons: /usr/local/etc/halite/beacons.sls
schedule: /usr/local/etc/halite/schedule.sls
```

Working copies of both are in [examples/](../examples): `master.conf` and
`agent.conf`.

### What wins

**A flag, then the environment, then the file, then the platform
default** — the most specific thing somebody typed wins.

The environment only outranks the file for the four settings that have a
variable at all: `$HALITE_ROOT`, `$HALITE_PILLAR_ROOT`, `$HALITE_PKI`, and
`$HALITE_MASTER`. Everything else is flag, file, default.

A setting the daemon does not have is an **error**, not a warning:

```
/usr/local/etc/halite/master.conf: no such setting: adr
    (its name is the flag's, without the dash)
```

A config file that quietly did nothing would be worse than one that
refuses to start, because the failure would arrive as a fleet behaving
oddly rather than as a daemon saying why.

## FreeBSD rc.d

Two scripts are in [contrib/rc.d/](../contrib/rc.d). Install the one the
host needs, and the binary:

```sh
install -m 0755 dist/halite-freebsd-amd64 /usr/local/bin/halite
install -m 0755 contrib/rc.d/halite_master /usr/local/etc/rc.d/halite_master
```

Both wrap the daemon in `daemon(8)` with `-S`, so output goes to syslog
under the `daemon` facility, tagged with the service name.

### sysrc settings

```sh
sysrc halite_master_enable="YES"
service halite_master start
```

| Variable | Default | Meaning |
|---|---|---|
| `halite_master_enable` | `NO` | run the control plane at boot |
| `halite_master_config` | `/usr/local/etc/halite/master.conf` | settings file |
| `halite_master_program` | `/usr/local/bin/halite` | the binary |
| `halite_master_flags` | — | extra flags, appended after `-config` |
| `halite_master_user` | `root` | account to run as |
| `halite_master_pidfile` | `/var/run/halite_master.pid` | |
| `halite_master_daemon_args` | — | flags for `daemon(8)`; `-r` restarts the control plane if it exits |

```sh
sysrc halite_agent_enable="YES"
sysrc halite_agent_master="master.example.com"
service halite_agent start
```

| Variable | Default | Meaning |
|---|---|---|
| `halite_agent_enable` | `NO` | run the agent at boot |
| `halite_agent_config` | `/usr/local/etc/halite/agent.conf` | settings file |
| `halite_agent_master` | — | control plane host[:port], or several comma-separated. Overrides the file's `master` |
| `halite_agent_program` | `/usr/local/bin/halite` | the binary |
| `halite_agent_flags` | — | extra flags, appended after `-config` |
| `halite_agent_pidfile` | `/var/run/halite_agent.pid` | |
| `halite_agent_daemon_args` | — | flags for `daemon(8)`; `-r` restarts the agent if it exits |

`halite_agent_master` exists because it is the one setting a host usually
gets from an image or a provisioning script rather than from a file that
is the same everywhere.

### Which account

The **agent applies states**, so it runs as root. There is no knob for
that, because an agent that could not chown a file or start a service
would fail halfway through a highstate.

The **control plane does not**: it reads the PKI directory, the state
tree, and the pillar tree, and binds its port — none of which needs root.
`halite_master_user` is worth pointing at an account that owns exactly
those, and the pillar tree's mode is what keeps its secrets either way
(see [pillar-security.md](pillar-security.md)).

### Restarting on failure

Neither script supervises by default: FreeBSD's rc does not, and adding
it silently would surprise whoever reads the script. Ask for it:

```sh
sysrc halite_master_daemon_args="-r"
```

## Elsewhere

There is no systemd unit or launchd plist in the tree yet. Both daemons
are a foreground process that logs to stderr and stops on SIGINT or
SIGTERM, which is all either init system needs — the unit is four lines
and nobody has asked for one to be maintained here.
