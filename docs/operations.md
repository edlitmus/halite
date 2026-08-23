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


## Logging

Structured JSON to stderr, one object per line, carrying `ts`, `level`,
`msg`, `component`, and `node_id`. SPEC section 26.1.

```yaml
log_level: info      # error, warn, info, debug, trace
log_format: json     # or console, for a person watching a terminal
log_file: ''         # a path also receives the records
```

`--log-level` and `--log-fmt` override both for one run, which is what
an interactive `halite-node call --log-fmt console` wants. Salt's
`garbage`, `quiet`, and `profile` are read on input, so a translated
configuration needs no edit.

A log file that cannot be opened is an error rather than a fall back to
stderr: an operator who asked for a file is relying on it.

### Secrets in the log

Every value the `gpg` renderer decrypts, and every setting whose name
says it holds a secret, is scrubbed from log records and error messages
before they are written. Redaction happens at the sink, so a diagnostic
added later cannot forget about it, and it covers every field of a
record rather than the message alone.

The line is between a diagnostic and requested data. `pillar items` is
not scrubbed: it was asked for the pillar, and answering with asterisks
would be a different program.

A value shorter than six characters is not scrubbed. It cannot be
removed from text without removing everything that resembles it — a
pillar value of `1` would turn every number in every message into
asterisks — and a one-character secret was never secret.

The state return is scrubbed too, in both output formats and in the
`state_|-id_|-name_|-fun` key, because a `cmd.run` that curls an API
with a token from pillar puts the token in its own name. Every
occurrence of one value becomes the same placeholder, so a dashboard
parsing that key still parses it.

What is *not* scrubbed is the run's own data structures, only its
output: `onchanges` and `prereq` compare changes, and two different
secrets both becoming asterisks would make two different states look
alike.

**Not yet:** per-component levels (`--log-level-component fileserver=debug`)
and the journal sink.

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

`halite-hub serve` and `halite-node connect` run. `halite-api serve`
needs phase 4 and exits saying so. Unit files and rc.d scripts for all
three are in `contrib/`.

```sh
sudo systemctl enable --now halite-hub      # the control plane
sudo systemctl enable --now halite-node     # the agent, once enrolled
```

```sh
sudo sysrc halite_hub_enable=YES && sudo service halite_hub start
sudo sysrc halite_node_enable=YES && sudo service halite_node start
```

The hub creates its enrollment CA under `pki_dir` on first run and logs
the fingerprint. That directory has to be writable by the account the
hub runs as; the systemd unit grants exactly it with `ReadWritePaths`
and nothing else under `/etc`.

`halite-node connect` reconnects on its own with backoff, so an
unreachable hub is not a unit failure. It exits 1 for the two things a
restart cannot fix — this node's enrollment was revoked, or `hub_tries`
was reached — and the units carry `RestartPreventExitStatus=1` so that
stops the unit and leaves the reason in the journal.

What the agent does **not** do yet is run a job that arrives from the
hub. That is the rest of phase 2. A job on the stream is logged and
refused, which is why `halite-highstate.service` and its timer are still
how a node is driven.

## Enrolling a node

The hub issues, an operator decides, and the node's private key never
leaves it. SPEC section 7.

```sh
# on the hub
halite-hub keys fingerprint            # the CA digest, to deliver out of band

# on the node
halite-node enroll --hub hub.example --ca-file /path/to/ca.crt \
    --hub-fingerprint 'ab:cd:...'
# prints this node's fingerprint and exits 2: the request is pending

# on the hub, after comparing the two fingerprints by another route
halite-hub keys list
halite-hub keys accept web1.example

# on the node
halite-node enroll                     # collects the certificate
```

Exit 2 from `enroll` means pending, which is neither success nor
failure; `--wait` blocks until an operator decides instead.

For an autoscaling group, a bootstrap token issues without an operator.
It has a mandatory lifetime of at most a day, a node-ID scope, a source
CIDR, and a record of what it admitted — none of which Salt's
`auto_accept` has, which is why there is no equivalent of that here.

```sh
halite-hub keys token create --ttl 1h --nodes 'web*.example' --cidr 10.0.0.0/8
halite-node enroll --token '<secret>' --ca-file ca.crt
```

The secret is printed once. The hub keeps only a SHA-256 digest of it.

### Renewal and revocation

A certificate lasts 90 days and is renewed at half of that, with a new
key, and no operator:

```sh
halite-node renew
```

Revocation takes effect at the next handshake and on every request over
a connection that is already open, so it does not wait for a CRL to
propagate:

```sh
halite-hub keys revoke web1.example --reason "decommissioned"
halite-hub keys export-crl --out /var/db/halite/halite.crl
```

`keys revoke` run beside a hub that is already serving reaches it within
a couple of seconds: the record on disk is the decision, and the running
hub follows it.

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
