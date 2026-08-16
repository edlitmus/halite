# Running the daemons as services

`halite master` and `halite agent` run in the foreground and log to
stderr, which is what an init system wants. This page is the config files
they read, the logging they do, and the FreeBSD rc.d scripts and systemd
units that start them.

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
addr: ":5617"
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

## Logging

Both daemons write one line per event to stderr, where `daemon(8)` and
journald pick it up. Two settings change that, and like every other
setting they are flags or lines in the config file:

| Setting | Default | Meaning |
|---|---|---|
| `-log-level` | `info` | `error`, `warn`, `info`, or `debug` |
| `-log-file` | — | write to this file instead of stderr |

A line carries its level, so a log that is being grepped or shipped
somewhere reads the same as it always did:

```
halite-master 2026/08/16 10:18:22 INFO  hello from "web1" (halite 0.11.0)
halite-master 2026/08/16 10:18:23 WARN  throttling enrollment from 10.0.0.5
```

### What each level holds

| Level | What lands there |
|---|---|
| `error` | work that was lost: a result that could not be reported, a pillar or state tree an agent asked for and did not get, a beacon that panicked |
| `warn` | something was refused or retried and the daemon carried on: a denied request, a revoked identity, a throttled enroller, a returner dropping a record, a certificate near expiry, `-auto-accept` being on, and a caller whose certificate this fleet's CA did not sign |
| `info` | the fleet working: enrollments, connections, jobs dispatched and returned, orchestrations, scheduled runs |
| `debug` | one line per authenticated request the control plane serves, and what each agent poll came back with |

`error` is the quietest useful setting; nothing is ever silent, because a
daemon that says nothing when it drops a result is worse than a noisy one.

### Writing to a file

```yaml
# /usr/local/etc/halite/master.conf
log-level: info
log-file: /var/log/halite/master.log
```

The directory is created if it is not there, the file is opened for
appending, and its mode is `0640` — job results and agent identities are
not secrets, but they describe the fleet in enough detail to keep off a
world-readable path. Starting by hand prints one line to stderr saying
where the log went, so the output has not simply vanished.

### Rotation

**`SIGHUP` reopens the log file.** That is the handshake both rotators
expect: they rename the file, signal the daemon, and it starts writing to
the name again. Without it the daemon keeps appending to a file that is
no longer at the path anybody is looking at.

```
# /usr/local/etc/newsyslog.conf.d/halite.conf
# logfile               owner:group  mode count size when  flags pid_file                       sig
/var/log/halite/master.log  root:wheel  640  7     1000 *     BC    /var/run/halite/halite_master.pid 1
```

```
# /etc/logrotate.d/halite
/var/log/halite/master.log {
    weekly
    rotate 7
    missingok
    notifempty
    postrotate
        systemctl kill -s HUP halite-master.service
    endscript
}
```

A daemon left on stderr has nothing to reopen, so `SIGHUP` there does
nothing rather than failing. Under `daemon(8)` or journald, rotation is
already somebody else's job — `-log-file` is for the hosts that are not
running either.

Under the **master** unit in this tree, `/var/log/halite` is the one
place the daemon may write: `LogsDirectory=halite` creates it, and
`ProtectSystem=strict` refuses everywhere else. A `-log-file` pointing
somewhere else needs that path added to `ReadWritePaths=`. The agent unit
is deliberately unsandboxed — it applies states as root — so its
`-log-file` may go anywhere, and the directory is created if it is not
there.

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
| `halite_master_binary` | `/usr/local/bin/halite` | the binary. **Not** `_program`: rc.subr reserves that name and would run halite in place of `daemon(8)` |
| `halite_master_flags` | — | extra flags, appended after `-config` |
| `halite_master_user` | `root` | account to run as |
| `halite_master_pidfile` | `/var/run/halite/halite_master.pid` | its directory is created for `halite_master_user`; an existing one is left alone |
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
| `halite_agent_binary` | `/usr/local/bin/halite` | the binary (see the note on the master's) |
| `halite_agent_flags` | — | extra flags, appended after `-config` |
| `halite_agent_pidfile` | `/var/run/halite/halite_agent.pid` | its directory is created if missing |
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

## systemd

Two units are in [contrib/systemd/](../contrib/systemd). Install the one
the host needs, and the binary:

```sh
install -m 0755 dist/halite-linux-amd64 /usr/local/bin/halite
install -m 0644 contrib/systemd/halite-master.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now halite-master
```

Output goes to the journal (`journalctl -u halite-master -f`). Both units
restart on failure after a few seconds, which is the systemd norm and is
visible in the file — unlike the rc.d scripts, where supervision is
opt-in through `halite_*_daemon_args="-r"`.

Settings belong in `/etc/halite/{master,agent}.conf`, so the unit is the
same on every host. Change a unit with a drop-in rather than in place,
so a reinstall does not undo it:

```sh
systemctl edit halite-agent
```

### Environment files

Each unit reads an optional `EnvironmentFile`, which is where the four
settings that have a variable can come from:

| File | Used for |
|---|---|
| `/etc/halite/master.env` | `HALITE_ROOT`, `HALITE_PILLAR_ROOT`, `HALITE_PKI` |
| `/etc/halite/agent.env` | `HALITE_MASTER` |

`HALITE_MASTER` is the systemd counterpart of `halite_agent_master`: the
one setting a host usually gets from an image or a provisioning script
rather than from a file that is the same everywhere.

```sh
echo HALITE_MASTER=master.example.com > /etc/halite/agent.env
```

Remember that the environment outranks the config file, so a stale
`agent.env` beats a corrected `agent.conf`.

### Which account, and what is sandboxed

`halite-master.service` runs as **`halite`**, which the unit does not
create:

```sh
useradd --system --home-dir /etc/halite --shell /usr/sbin/nologin halite
```

It is sandboxed: a read-only filesystem, no capabilities, private `/tmp`
and `/dev`, and only inet and unix sockets. Two paths stay writable —
`/etc/halite/pki`, because signing an enrollment and recording an
accepted key are writes, and `/var/log/halite` from `LogsDirectory=`,
where the default file returner writes. **Point `-pki` or a file
returner somewhere else and that list has to grow**, or the daemon fails
with a read-only filesystem:

```sh
systemctl edit halite-master     # ReadWritePaths=/srv/halite/pki
```

`halite-agent.service` runs as **root with no sandboxing at all**, and
that is deliberate. The agent installs packages, writes files anywhere,
restarts services, and manages jails, containers, and filesystems; a
restriction here would surface as a highstate failing halfway through,
read as a broken state rather than as a unit file. It gets
`CacheDirectory=halite` for the fetched state tree and a five-minute
`TimeoutStopSec`, because interrupting a package upgrade is worse than
waiting for it.

The FreeBSD default differs: `halite_master_user` is `root` there,
because that is what `rc.subr` does without one. Both agree that the
control plane does not need root — systemd just makes it easy to say so
in the file everyone installs.

## Elsewhere

There is no launchd plist in the tree. Both daemons are a foreground
process that logs to stderr and stops on SIGINT or SIGTERM, which is all
launchd needs — the plist is a dozen lines and nobody has asked for one
to be maintained here.
