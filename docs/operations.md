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


## The event bus

Everything the hub does lands on a durable log — `halite/job/<jid>/new`,
`halite/job/<jid>/ret/<node>`, `halite/node/<node>/start`,
`halite/key/<node>/<action>`, and the rest. `halite-hub event tags`
prints the namespace.

```sh
halite-hub event listen                                  # follow everything
halite-hub event listen --tag 'halite/job/**'            # one class
halite-hub event listen --from earliest --once           # replay the log
halite-hub event listen --from '00000003:81920'          # resume from an offset
```

The offset is on every record, so a consumer stores it and resumes
exactly where it stopped. Salt's bus is in-memory and lossy by
construction; the events an estate wants during an incident are the ones
it dropped.

A node puts its own events on the bus:

```sh
halite-node event send deploy/finished version=1.2
halite-node event send deploy/finished '{"version":"1.2","host":"web1"}'
```

What lands is `halite/node/<that node>/deploy/finished` — a node writes
under its own prefix and nowhere else, whatever tag it asks for. Salt's
reactor runs with the control plane's full privilege, so a node that can
fire the right event can cause fleet-wide execution.

Retention is `event_retention` and `event_max_size`, whichever binds
first, enforced by the hub. Security-relevant tags — `halite/auth*` and
`halite/key/*` — are written durably before the append returns; the rest
are synced on an interval.

`event_tag_compat: true` additionally emits each event under its
`salt/...` spelling, for a consumer that cannot be changed at the same
time as the estate.

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

halite has its own scheduler — `schedule:` in the node configuration,
managed at runtime with `halite-node call schedule.add`, and supporting
`cron`, `when`, `every`, `splay`, `maxrunning`, and `catchup`. [The
command reference](command-reference.md#the-scheduler) covers it.

The machine's own scheduler still does the job perfectly well, and there
is a real argument for leaving it there: it is one less thing in the
agent that can go wrong, and every operator already knows how to read a
crontab. Use the built-in one when you want the schedule to travel with
the node, to be changed without restarting it, or to catch up after an
outage.

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

`halite-hub serve`, `halite-node connect`, and `halite-api serve` all
run. Unit files and rc.d scripts for the three are in `contrib/`.

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

The agent runs the jobs that arrive on that stream, and compiles them
against the tree and the pillar the hub serves.

Set `file_roots` on the hub and the fleet follows it:

```yaml
# hub.yaml
file_roots:
  base:
    - /srv/halite/states
pillar_roots:
  base:
    - /srv/halite/pillar
```

Each node receives its own pillar and no other node's: the hub compiles
it against the grains that node reported, and the identity comes from
the certificate rather than from the request. A node cannot ask for
somebody else's.

A node fetches what it needs, verifies each file against the digest the
hub published before moving it into place, and caches it under
`cache_dir`. The next run asks conditionally, so a tree redeployed from
git — new timestamps, identical contents — costs a round trip and no
transfer.

`serve` refuses to start if a file root holds the hub's own `state_dir`,
`cache_dir`, or `pki_dir`. That arrangement serves the job cache, and so
every return in the estate, to every node that has enrolled.

## Driving the fleet

`halite-hub run` is the old `salt` command. It authenticates with an
operator certificate — there is no "trusted because it is running on
the hub" — and every submission is authorized against the policy, which
denies by default. Issue a certificate and, the first time, a policy:

```sh
halite-hub keys operator create ed --admin
```

`--admin` writes a bootstrap policy granting that one operator
everything, and only if there is no policy yet. It is a starting point
and not a destination: narrow it, and add roles for the people who do
not need all of it.

Without a policy file the hub starts, says so, and authorizes nothing.
That is deliberate: security that depends on a file existing is not
security.

```sh
halite-hub run '*' test.ping
halite-hub run '*' state.apply --test
halite-hub run -G 'os:FreeBSD' state.apply
halite-hub run -L 'web1,web2' service.restart nginx
halite-hub run '*' state.apply --async        # print the jid and return
```

Exit codes say which of the three things happened:

| Code | Meaning |
|---|---|
| 0 | every node answered and succeeded |
| 1 | a node answered and failed |
| 3 | a node was sent the job and did not answer |

The third is deliberately not the second. "It said no" and "it said
nothing" call for different actions, and Salt reports both as an absence
in the output.

### Applying to a lot of machines at once

Don't. `--batch` runs against a slice at a time and waits for it to
return before advancing:

```sh
halite-hub run '*' state.apply --batch 25% --batch-wait 30s
halite-hub run '*' state.apply --batch 10 --batch-safe-limit 3
halite-hub run '*' state.apply --subset 5     # a canary
```

The batch belongs to the hub, not to your terminal. In Salt `--batch`
is implemented in the CLI, so closing the laptop abandons the run with
half the estate updated and nothing recording where it stopped. Here:

```sh
halite-hub jobs active           # what is in flight, and how far it got
halite-hub jobs resume <jid>     # pick it up after a hub restart
```

`--batch-safe-limit` is the one worth setting. It stops the run once
that many nodes have failed, so the rest of the estate does not get the
same broken change. The job ends in state `aborted`, `run` exits 1, and
the nodes never reached are named.

`--subset` picks its nodes with `crypto/rand`: a canary set that can be
predicted is one that can be arranged to miss.

Every job and every return is recorded on the hub before delivery, so a
caller that disconnects loses nothing:

```sh
halite-hub jobs list
halite-hub jobs show <jid>
halite-hub jobs missing <jid>       # who was sent it and has not answered
halite-hub jobs prune               # retention runs hourly; this is now
halite-hub jobs kill <jid>          # stop what has not happened yet
halite-hub jobs export <jid>        # the job, every return, and who never answered
```

`jobs kill` stops a job reaching the nodes it has not reached, unspools
anything queued, and tells the nodes that have it. A node already
applying a state finishes it: a state run interrupted halfway leaves a
machine in neither the old state nor the new one, which is worse than
finishing something you changed your mind about. The command says so.

### A node that is switched off

```sh
halite-hub run '*' state.apply --offline queue    # wait for it to come back
halite-hub run '*' state.apply --offline require  # or refuse to run at all
```

The default reports it as unresponsive and sends it nothing. `queue`
spools the job for its next connection, bounded by the job's expiry —
an hour by default rather than fifteen minutes, because it is waiting
for a machine that is off. A node that comes back after the expiry does
**not** run a stale instruction, and the hub records that it did not,
because a node returning to find that nothing happened and no reason
why is the failure this bounds.

The cache is bounded by `job_cache_retention` and `job_cache_max_size`,
whichever binds first, and the hub enforces both. Salt's `local_cache` <!-- lexicon:allow -->
grows until the disk is full.

### Who may run what

The policy is one file with one grammar. A rule names a target **and**
the functions permitted against it, and a request must match one rule
entirely — Salt's `publisher_acl` and `external_auth` grant those
separately, with surprising precedence.

```yaml
roles:
  webops:
    - target: 'web*.prod'
      functions: ['state.apply', 'service.*', 'pkg.installed']
      args:
        'state.apply':
          allow_sls: ['webserver.*']
          deny_kwargs: ['pillar']
  readonly:
    - target: '*'
      functions: ['test.ping', 'grains.*', 'state.show_*']

bindings:
  - principal: 'cert:CN=alice'
    roles: ['webops', 'readonly']
```

`deny_kwargs: ['pillar']` matters more than it looks: passing pillar on
the command line is otherwise a trivial way round pillar-based
authorization.

A wildcard never grants a function that runs arbitrary code. Salt's
`.*` grants everything, and everybody's Salt ACL grants `.*`; here
`functions: ['*']` covers `pkg.installed` and not `cmd.run`, which has
to be named. `halite-hub policy show` prints the list for this build.

Test a policy before it is in production, and in CI:

```sh
halite-hub policy test 'cert:CN=alice' 'web1.prod' state.apply webserver.nginx
halite-hub policy test 'cert:CN=alice' 'db1.prod' state.apply    # exits 1
```

Every decision is logged with the rule that matched, or the reason for
the denial.

### What a node refuses

A job carries a nonce and an absolute expiry (SPEC 6.3). A node refuses
one it has already run, one whose nonce it has seen, and one past its
expiry — and returns the refusal rather than dropping it, so a job that
will not run says so instead of looking slow. `--ttl` sets the window;
the default is 15 minutes.

A node runs one job at a time. `job_queue_depth` bounds what waits, and
a full queue is refused out loud rather than held in memory.

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

## The event bus

Everything the hub does lands on a durable log — `halite/job/<jid>/new`,
`halite/job/<jid>/ret/<node>`, `halite/node/<node>/start`,
`halite/key/<node>/<action>`, and the rest. `halite-hub event tags`
prints the namespace.

```sh
halite-hub event listen                                  # follow everything
halite-hub event listen --tag 'halite/job/**'            # one class
halite-hub event listen --from earliest --once           # replay the log
halite-hub event listen --from '00000003:81920'          # resume from an offset
```

The offset is on every record, so a consumer stores it and resumes
exactly where it stopped. Salt's bus is in-memory and lossy by
construction; the events an estate wants during an incident are the ones
it dropped.

A node puts its own events on the bus:

```sh
halite-node event send deploy/finished version=1.2
halite-node event send deploy/finished '{"version":"1.2","host":"web1"}'
```

What lands is `halite/node/<that node>/deploy/finished` — a node writes
under its own prefix and nowhere else, whatever tag it asks for. Salt's
reactor runs with the control plane's full privilege, so a node that can
fire the right event can cause fleet-wide execution.

Retention is `event_retention` and `event_max_size`, whichever binds
first, enforced by the hub. Security-relevant tags — `halite/auth*` and
`halite/key/*` — are written durably before the append returns; the rest
are synced on an interval.

`event_tag_compat: true` additionally emits each event under its
`salt/...` spelling, for a consumer that cannot be changed at the same
time as the estate.

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

## Metrics

Every component records Prometheus metrics and exposes them at
`/v1/metrics`. On by default; `metrics: false` turns the recording off.

**Point the scraper at `halite-api`.** It is the only part of the
control plane a scraper can reach — the hub speaks its own ALPN protocol
over mutual TLS — and it answers with both expositions, its own and the
hub's, merged into one document:

```yaml
scrape_configs:
  - job_name: halite
    scheme: https
    authorization:
      credentials_file: /etc/prometheus/halite.token
    static_configs:
      - targets: ['api.example:4511']
```

The token needs `metrics.show` in its role's `runners:` list, and the
API's own certificate needs it at the hub, like every other request:

```yaml
roles:
  scraper:
    - runners: ['metrics.show']
bindings:
  - principal: 'local:prometheus'
    roles: ['scraper']
```

Issue it a token with a long life and no idle expiry, since a scraper
does not log in.

If the hub cannot be reached, the scrape still succeeds: the API's own
numbers come back and the reason appears as a comment, which the scraper
ignores. `halite_api_hub_scrape_failures_total` is the one to alert on —
it counts in the scrape that failed, not the one after.

To read the hub's own numbers directly, from the hub:

```sh
halite-hub metrics --as ed
halite-hub metrics --as ed --filter reactor
```

`--filter` keeps the `# HELP` and `# TYPE` lines, which is what `grep`
loses and what says whether a counter exists at all.

### What to alert on

| Metric | Why |
|---|---|
| `halite_events_dropped_total` | The bus lost something. It should be zero. |
| `halite_reactor_dropped_total` | The reactor's queue overflowed; reactions did not run. |
| `halite_reactor_queue_depth` | Rising means the workers are behind. |
| `halite_jobs_missing_returns` | Nodes a dispatched job has not heard from. |
| `halite_authz_decisions_total{result="denied"}` | A rate, not a total: a rise is either a misconfigured role or someone trying. |
| `halite_auth_attempts_total{result="refused"}` | The same, at the login. |
| `halite_pillar_failures_total` | A node is getting no pillar, which SPEC 12.7 prefers to a partial one. |
| `halite_api_hub_scrape_failures_total` | The API cannot reach the hub. |

Every bounded queue and every drop path has a counter — SPEC 26.2 makes
that a rule, because it is what turns a backpressure design into
something that can be audited. A family is declared in the exposition
before anything has been observed, so you can see the counter exists
before it fires.

A family holds at most 512 series. Past that, observations are counted
under `__overflow__` rather than dropped or allowed to grow without
bound: an estate with a thousand distinct functions would otherwise turn
`halite_jobs_dispatched_total` into a thousand series.

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
