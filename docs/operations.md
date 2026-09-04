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

There is no reload. Nothing handles `SIGHUP`, so the signal terminates
the process, and the units deliberately carry no `ExecReload` — the
directive was there once and turned `systemctl reload` into an outage.
Changing configuration is a restart:

```sh
sudo systemctl restart halite-hub
sudo service halite_hub restart
```

Two things a revocation does not need a restart for: `keys revoke` on a
running hub takes effect within a couple of seconds, and a node whose
certificate was revoked exits on its own.

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

A node needs one thing from the hub by a route an attacker cannot
tamper with: the fingerprint of its CA. The CA certificate itself is
public, and the node fetches it from the hub and checks it against that
fingerprint — so the fingerprint is the whole of the trust decision, and
enrolling without one is refused.

```sh
# on the hub
halite-hub keys fingerprint            # the CA digest, to deliver out of band

# on the node
halite-node enroll --hub hub.example --hub-fingerprint 'ab:cd:...'
# prints this node's fingerprint and exits 2: the request is pending

# on the hub, after comparing the two fingerprints by another route
halite-hub keys list
halite-hub keys accept web1.example

# on the node
halite-node enroll                     # collects the certificate
```

Exit 2 from `enroll` means pending, which is neither success nor
failure; `--wait` blocks until an operator decides instead.

Which is one line in `node.yaml` beside the hub, and all a provisioning
tool has to write:

```yaml
hub: hub.example
hub_fingerprint: 'ab:cd:...'
```

`hub_ca_file` still takes a CA you deliver yourself, and `--ca-file`
overrides it — but the fingerprint is required either way, because a CA
this node has not already pinned is one it is being asked to start
trusting. Only a CA already in `pki_dir` is exempt: it was checked when
it was written, which is why `connect` and `renew` on an enrolled node
need no fingerprint at all.

### Why fetching the CA is safe

The node dials the hub with certificate verification disabled, then does
something stricter than the default in its place: it looks through the
chain the hub presented for a certificate matching the pinned
fingerprint, and verifies the hub's own certificate against **that one
alone**. If either step fails, the handshake fails — so there is no
connection left over to accidentally keep using.

An attacker in the middle would have to present a certificate whose
SHA-256 matches the pinned fingerprint, which is a preimage. What they
cannot do instead is put the real CA in the chain beside their own
certificate: the CA is public, so they can send it, but their
certificate then does not chain to it and the handshake is refused.
There is a test for exactly that, and it fails if the verification step
is removed.

This is the only place in the build that disables certificate
verification, and it is why `hub_fingerprint` has no optional mode. The
guarantee is the fingerprint, so a missing one is not a weaker check but
no check at all.

A hub built before this served only its own certificate, so a newer node
enrolling against an older hub finds no CA in the chain to match. That is
reported as what it is rather than as a fingerprint mismatch — upgrade
the hub, or give the node the certificate directly with `hub_ca_file`.

### Enrolling from the service

On FreeBSD the rc.d script does the first two steps for you. `service
halite_node start` enrols if this node holds no certificate, and refuses
to start until it does — because `connect` cannot run without one, and
says so into the log rather than to whoever ran the command:

```
# service halite_node start
node        web1.example
fingerprint ab:cd:...
hub         https://hub.example:4510

the request is waiting for an operator to accept it.
on the hub: halite-hub keys accept web1.example
run this again, or pass --wait, once it has been accepted.
halite_node is not enrolled yet; accept it on the hub and start again
```

Accept it on the hub, run `service halite_node start` again, and it
collects the certificate and starts.

A node that already holds one skips straight through without touching
the network, so a reboot while the hub is down still starts the agent.
The knobs, all optional:

```sh
sysrc halite_node_ca_file=/var/db/halite/hub-ca.crt   # first enrollment only
sysrc halite_node_token='<secret>'                    # for token enrollment
sysrc halite_node_enroll=NO                           # provision certificates yourself
```

`halite_node_ca_file` is unnecessary once `hub_fingerprint` is set in
`node.yaml` and the CA is on disk. Enrollment runs as
`halite_node_user`, not as root, because it writes this node's private
key and key material owned by the wrong account is a node that cannot
read its own identity.

There is no equivalent in the systemd units. `ExecStartPre=` could carry
it, and has not been tried on a Linux host, so it is not shipped as
though it had been.


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

## FIPS builds

`make fips` produces a parallel set of binaries — `halite-hub-fips`,
`halite-node-fips`, `halite-api-fips` — built against the certified Go
Cryptographic Module (SPEC 27.4). `make fips-cross` is the release set,
built for Linux amd64 and arm64 only.

Ask a binary what it is rather than trusting its filename:

```sh
halite-node-fips version
# halite-node v1.0.0+abc123def456 (fips v1.0.0)
# fips mode on, module v1.0.0, self-tests passed
```

A binary that does not print a module version is not a FIPS artifact,
whatever it is called. `make fips` checks this itself and refuses to
finish otherwise.

Deploy with the mode stated by the service rather than inherited from
the environment. On systemd, the drop-ins in `contrib/systemd/fips/`
override the two lines that differ:

```sh
install -d /etc/systemd/system/halite-node.service.d
install -m 0644 contrib/systemd/fips/halite-node.service.d/fips.conf \
    /etc/systemd/system/halite-node.service.d/
systemctl daemon-reload && systemctl restart halite-node
```

On FreeBSD, set the matching knob in `rc.conf` — `halite_hub_fips`,
`halite_node_fips`, `halite_api_fips`, or `halite_highstate_fips`. Each
runs the `-fips` artifact with `GODEBUG=fips140=on` stated rather than
inherited, so a `GODEBUG` already in the environment cannot turn it off.

Three things change in FIPS mode, and each is refused rather than
silently substituted:

| Behaviour | What to do instead |
|---|---|
| `x509.create_private_key` refuses `algorithm: ed25519` | Use `ec` with `p256` or `p384` |
| TLS key exchange is P-256 or P-384; X25519 is refused | Nothing — both ends of a halite connection agree |
| TOTP cannot be checked, so accounts with a second factor cannot log in | `halite-api` names them at startup; remove the `totp` field or run those operators on a non-FIPS API |

That last one is the one that surprises people. RFC 6238 is defined on
HMAC-SHA-1, so a FIPS build has no way to check a code. It fails closed:
the account still declares that it needs a second factor and every code
is refused, rather than the password alone being accepted. Read the
startup log before cutting an API over.

Check what a running estate actually has:

```sh
halite-hub run '*' grains.item fips_mode fips_build fips_module
```

`fips_mode` is the host kernel's state and `fips_build` is the binary's
own, so a `True`/`False` pair either way is a mismatch worth chasing.
Both are grains — a node's account of itself — so treat them as
inventory rather than as evidence.

## Relays

A relay is a hub that serves its own nodes and presents itself upstream
as a single client (SPEC 5.3). Use one for a segment that cannot reach
the main hub directly, or one whose returns must survive the link
between them going down.

The relay enrols with its upstream as an ordinary node, so set it up in
that order:

```sh
# On the upstream hub: accept relays, and grant this one the right.
accept_relays: true          # in the hub configuration

# In the policy, for the relay's node certificate:
#   - principals: ['node:relay1.example']
#     runners: ['relay.proxy']

# On the relay: enrol with the upstream, then run as a relay.
halite-node enroll --config /usr/local/etc/halite/relay-upstream.yaml \
    --ca-file /var/db/halite/upstream-ca.crt
halite-hub keys accept relay1.example    # on the upstream
```

The relay's own hub configuration then names the upstream:

```yaml
node_id: relay1.example
relay: true
relay_upstream: hub.example
relay_pki_dir: /var/db/halite/relay-pki      # what it enrolled with
relay_spool_dir: /var/db/halite/relay-spool  # returns during an outage
relay_event_tags:
    - halite/job/**                          # empty forwards nothing
```

Nodes behind the relay enrol with the relay, not with the upstream, and
their keys are accepted there. The upstream never holds a key for them —
`keys list` upstream shows the relay alone — but `manage.up` and
targeting see them, and a job submitted upstream reaches them through
the relay's stream.

Two things are worth knowing before deploying one. A job submitted to
the relay's own command line stays local and does not appear in the
upstream's job cache; run it upstream if it should. And the upstream
trusts the relay's word about which nodes it proxies for, bounded by the
`relay.proxy` grant — see DIVERGENCE 1.8 and 1.9 for what that does and
does not buy.

While the upstream is unreachable the relay keeps serving its segment
and spools returns to `relay_spool_dir`, draining them oldest-first when
the link comes back. Watch it with:

```sh
halite-hub metrics | grep relay      # on the relay
```

A spool that is not shrinking after the upstream returns means the
returns are being refused rather than lost; the relay's log says which
jid and why.

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

## The API's serving certificate

`halite-api serve` will not start without one:

```
this service needs a serving certificate; set `tls_cert` and `tls_key`,
or pass --tls-cert and --tls-key
```

**It does not come from the enrollment CA, and there is no command that
issues one.** That is deliberate. The API serves ordinary HTTPS clients
— browsers, `curl`, a Prometheus scraper — and those already trust some
set of certificate authorities. Issuing its certificate from the
enrollment CA would mean teaching every one of them to trust the
authority that also issues node identities, which is a much larger thing
to trust than a web server needs.

So it comes from wherever the rest of your HTTPS certificates come from:
an internal CA, ACME, or the estate's own tooling. For a lab, or a first
run, self-signed is enough:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout /usr/local/etc/halite/pki/api.key \
    -out /usr/local/etc/halite/pki/api.crt \
    -days 365 -subj "/CN=$(hostname)" \
    -addext "subjectAltName=DNS:$(hostname),DNS:localhost,IP:127.0.0.1"
```

The names in `subjectAltName` are what a client verifies, and **only**
those: the `CN` is ignored by every current client. Put every name and
address the API will be reached by in there. `localhost` and `127.0.0.1`
are in the command above because they are what you will reach it by
while testing, and a certificate that omits them fails verification with
no useful message — under `curl -s`, with no message at all.

Add the external name too, if a scraper reaches it by one:

```sh
-addext "subjectAltName=DNS:$(hostname),DNS:api.example,DNS:localhost,IP:127.0.0.1,IP:10.0.0.5"
```

```yaml
# api.yaml
tls_cert: /usr/local/etc/halite/pki/api.crt
tls_key: /usr/local/etc/halite/pki/api.key
```

Give the key mode 0600 and the account the API runs as. A scraper or any
other client then needs that certificate — or the CA that signed it — as
its `ca_file`; for a self-signed one they are the same file.

halite can manage this itself once it is running: `x509.create_certificate`
and `x509.private_key_managed` are in the [module
reference](modules.md), so the API's own certificate can be a state in
the tree like anything else, renewed on a schedule.

## Metrics

Every component records Prometheus metrics. On by default;
`metrics: false` turns the recording off.

The hub and the API expose them at `/v1/metrics`. Point the scraper at
`halite-api` on 4511, not at the hub: the API answers with both
expositions merged, and the hub speaks its own ALPN identifier that no
ordinary client can send.

A node has no listener and does not get one for this. It writes its
exposition to the file `metrics_textfile` names, which is the one
node_exporter's textfile collector reads, so the scraper that already
reaches every machine picks it up with no port opened anywhere:

```yaml
# node.yaml
metrics_textfile: /var/lib/node_exporter/textfile_collector/halite.prom
```

Unset, which is the default, a node records nothing. Reading the hub's
by hand:

```sh
halite-hub metrics --as ed
halite-hub metrics --as ed --filter reactor
```

[metrics.md](metrics.md) is the whole of it — the scraper configuration
end to end, every family with its labels and meaning, the alerting
rules, and what the specification names that this build does not have.

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

## Accounts and permissions

The hub and the API run as an unprivileged account. A node runs as root,
because it applies state to the whole machine.

Every failure this section exists to prevent has the same shape: a
directory created by running a command **by hand as root**, then used by
a service running as `halite`. Neither `mkdir -p` nor `[ -d ]` answers
"can this process use it" — they answer "does it exist" — so the service
starts and fails later, somewhere that does not name the directory.

### What each program needs

`<config root>` is `/usr/local/etc/halite` on a BSD, `/etc/halite` on
Linux, `%PROGRAMDATA%\Halite` on Windows. `<state dir>` is
`/var/db/halite` on a BSD and `/var/lib/halite` elsewhere.

**`halite-hub`**, as `halite`:

| Setting | Path | Needs |
|---|---|---|
| `pki_dir` | `<config root>/pki` | **read/write**, 0700 — it creates the enrollment CA here on first run, and its own serving certificate |
| `state_dir` | `<state dir>` | **read/write**, 0700 — keys, the job cache, the event bus, the mine, orchestration |
| `cache_dir` | `<cache dir>` | **read/write**, 0700 — cached node data, gitfs mirrors, s3 objects |
| `policy` | `<config root>/policy.yaml` | read |
| `file_roots`, `pillar_roots` | wherever the tree is | read |
| — | `<log dir>` | write, for the rc.d log file |

**`halite-node`**, as `root`:

| Setting | Path | Needs |
|---|---|---|
| `pki_dir` | `<config root>/pki` | **read/write**, 0700 — this node's private key lives here |
| `state_dir` | `<state dir>` | **read/write** — extensions, returner spools |
| `cache_dir` | `<cache dir>` | **read/write** — the tree it fetched from the hub |

**`halite-api`**, as `halite`:

| Setting | Path | Needs |
|---|---|---|
| `state_dir` | its own, **not** the hub's | **read/write**, 0700 — the token store |
| `pki_dir` | `<config root>/pki` | read — its own operator certificate and the CA |
| `accounts`, `policy` | `<config root>/` | read |
| `tls_cert`, `tls_key` | wherever they are | read |

Give the API a `state_dir` of its own. The systemd unit runs it with
`StateDirectory=halite-api` and `ProtectSystem=strict`, which makes
everything else read-only, so a service left on the built-in default
could not write a token at all.

### Setting it up

Worked configuration files for all three programs, plus a policy and an
account file, are in
[`contrib/examples/`](../contrib/examples/). Each is loaded by a test as
the program it is written for, so none of them can teach a setting that
does not exist.

Create the account, then let the build do the rest:

```sh
# FreeBSD
pw useradd halite -c "halite service account" -d /nonexistent -s /usr/sbin/nologin
# Linux
useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin halite

make build
sudo make install
```

`make install` does not build, so the build never runs as root and
leaves no root-owned binaries in `bin/`. It creates every directory
below owned by that account, for
the platform it runs on, and says so rather than continuing quietly if
the account is missing or a `chown` does not take. [Getting
started](getting-started.md#installing) has the paths it uses and how to
override them.

By hand, on FreeBSD:

```sh
install -d -o halite -g halite -m 0700 /usr/local/etc/halite/pki
install -d -o halite -g halite -m 0700 /var/db/halite
install -d -o halite -g halite -m 0700 /var/cache/halite
install -d -o halite -g halite -m 0750 /var/log/halite
```

On Linux the state directory is `/var/lib/halite`, and the systemd units
create the state, cache, and log directories themselves with
`StateDirectory=`, `LogsDirectory=`, and the right owner — so only
`pki_dir` needs doing by hand there.

### Checking it

```sh
find /usr/local/etc/halite/pki /var/db/halite /var/cache/halite \
     /var/log/halite ! -user halite -print
```

Anything that prints is a directory the hub cannot use. The usual cause
is a `halite-hub` or `halite-node` run by hand as root before the
service was enabled, which creates whatever was missing owned by root.
Give the tree back to the account that runs the service — recursively,
because the files inside it are root's too.

### What it looks like when it is wrong

Two failures worth recognising, because neither names the directory:

| Symptom | Cause |
|---|---|
| `daemon: open: Permission denied`, and the service does not start | The log directory is not writable by the account. rc.subr drops to it before `daemon` runs. |
| `no node matched "*"` immediately after `keys list` shows the node accepted | The hub cannot read `<cache dir>/nodes`, so every node is skipped during targeting. |
| `/v1/enroll: reading the record for <node>: permission denied`, on a node whose key you just accepted | A key record written by root into a store owned by the service account. `keys accept` now hands the record to whoever owns the store, but a record written before that is still root's. |

The second is refused at startup now — a hub whose node cache it cannot
write says so and stops, rather than starting and matching nothing. The
first is handled by the rc.d script creating the log file itself. Both
are worth knowing anyway, because an estate that upgrades meets them on
the directories it already has.

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

### Setting the search path

`PATH` is the one a state most often needs changed. Left alone it is
whatever started the program, and rc.d, systemd, and an operator's shell
each hand over a different one — so a state that finds its binary when
you run `halite-node state apply` by hand fails under the service, and
the failure says only `executable file not found in $PATH`.

`exec_path` makes it explicit, which is what SPEC 25.4 asks for:

```yaml
# node.yaml
exec_path: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
```

It replaces rather than extends, so name the whole path. It applies to
the program as well as to what it spawns, so `cmd.run`, the package
providers, and the hub's `git`, `gpg`, and `ssh` all resolve binaries the
same way — the hub reads it too, for exactly those.

Check what a node will actually search:

```sh
halite-node call cmd.run printenv PATH
```

Left empty, the environment's `PATH` is used, and a built-in list when
there is none. That is the old behaviour and it still works; it is just
not reproducible between one way of starting the node and another.


## Backups

There are none yet. Salt's `backup:` option, which keeps a copy of a file
before overwriting it, has no equivalent here: `file.managed` overwrites
without keeping one. If a tree relies on that, say so before migrating
it. It is recorded in [DIVERGENCE.md](DIVERGENCE.md).
