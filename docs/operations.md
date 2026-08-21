# Operations

Running halite on a machine: scheduling it, the service files, what the
exit codes mean, and where the output goes.

## Exit codes

Every command follows the same convention, so a script never has to parse
output to know what happened.

| Exit | Meaning |
|---|---|
| 0 | The run succeeded and something changed. |
| 2 | The run succeeded and nothing needed changing. |
| 1 | A state failed, or the command could not run. |

**Treat 2 as success.** A monitor that does not will alert on every
machine that was already correct, which is nearly all of them nearly all
of the time, and the alerting will be ignored within a day.

```sh
halite-node state apply --local
case $? in
  0) echo "converged, with changes" ;;
  2) echo "already converged" ;;
  *) echo "failed" >&2; exit 1 ;;
esac
```

## Running it on a schedule

halite's own scheduler is phase 3 of SPEC section 32. Until it lands, the
machine's own scheduler does the job, and there is an argument for
leaving it there permanently: it is one less thing in the agent that can
go wrong, and every operator already knows how to read it.

### systemd

`contrib/systemd/` holds the units. The timer is the one that works
today:

```sh
sudo install -m 0644 contrib/systemd/halite-highstate.service \
                     contrib/systemd/halite-highstate.timer \
                     /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now halite-highstate.timer
```

```sh
systemctl list-timers halite-highstate.timer
journalctl -u halite-highstate.service -n 50
```

Two details in that unit are worth understanding rather than copying.

`SuccessExitStatus=0 2` is what stops every converged run being recorded
as a failed unit.

`RandomizedDelaySec=10min` spreads a fleet out. Without it every machine
converges on the same second and hits the file server together; the
window should be a decent fraction of the interval.

### FreeBSD

`contrib/rc.d/` holds the scripts.

```sh
sudo install -m 0555 contrib/rc.d/halite_highstate /usr/local/etc/rc.d/
sudo sysrc halite_highstate_enable=YES
```

Run it by hand at any time:

```sh
sudo service halite_highstate onestart
```

It translates exit 2 into "already converged" and returns 0, so a boot
run does not report a failure for a machine that had nothing to do.

For a periodic run, cron is the honest answer on FreeBSD:

```
# /etc/cron.d/halite  — spread the fleet with a random delay
*/30 * * * * root sleep $((RANDOM \% 600)); /usr/local/bin/halite-node state apply --local >> /var/log/halite/highstate.log 2>&1
```

### The daemons

`halite-node serve`, `halite-hub serve`, and `halite-api serve` need the
transport, which is phase 2 of SPEC section 32 for the first two and
phase 4 for the third. Their unit files and rc.d scripts are in
`contrib/`, written for when the phases land.

Enabling one today starts a process that exits saying which phase it
needs. The systemd units carry `RestartPreventExitStatus=1` so that this
stops the unit and leaves the message in the journal, rather than a
restart loop that fills the log and says nothing.

## Logging

halite writes to standard output and standard error. Under systemd that
is the journal; the units set `SyslogIdentifier` so `journalctl -t
halite-node` finds it. Under the rc.d scripts it goes to
`/var/log/halite/`.

`--out` chooses the rendering. `nested` is the human one and the default;
`json` is the frozen, versioned schema for anything downstream:

```sh
halite-node state apply --local --out json | jq '.[] | select(.result == false)'
```

The JSON shape is SPEC section 11.8 and does not change without a
version bump, so a dashboard built on it keeps working.

## Before you apply anything

```sh
halite-node state show_lowstate --local     # the ordered run
halite-node state apply --local --test      # what it would change
halite-node lint /srv/halite/states/web.sls # render and parse, no run
```

`lint` is the cheapest of the three: it renders the template and parses
the YAML without touching the machine, and it reports the things that are
easy to get wrong and hard to see — a regular expression RE2 cannot
express, a YAML 1.1 boolean coercion, a duplicate key, a `salt://`
reference that does not resolve.

## Reading the tree without applying it

```sh
halite-node grains items --local              # every fact about this machine
halite-node grains get os_family --local
halite-node pillar items --local              # this machine's compiled pillar
halite-node state show_top --local            # which SLS files match here
halite-node call sys.list_modules --local     # what this build ships
halite-node call sys.doc name=file.managed --local
```

`sys.doc` is the same information as the [module
reference](modules.md), read from the binary rather than from a file, so
it cannot be out of date with the build you are running.

## The file modes halite writes

A private key is written 0600, by an atomic write that sets the mode
before the rename — the key is never briefly visible with a temporary
file's default. A script fetched by `cmd.script` is 0700 and removed
after the run, because many carry a credential and the temporary
directory is world-readable.

Everything else takes the mode the state asked for, and refuses an
unquoted one.

## What a node sends to a child process

A command run by `cmd.run` gets a clean environment: `PATH`, `LC_ALL`,
`LANG`, and `HALITE=1`. Not halite's own environment, and not the
pillar. A state that needs a variable says so with `env`.

`runas` uses setuid and setgid with the target account's full
supplementary group set, rather than `su -c`, which would start a shell
and read that account's profile — changing the environment out from under
the command.

## Backups

There are none yet. Salt's `backup:` option, which keeps a copy of a file
before overwriting it, has no equivalent here: `file.managed` overwrites
without keeping one. If a tree relies on that, say so before migrating
it. It is recorded in [DIVERGENCE.md](DIVERGENCE.md).
