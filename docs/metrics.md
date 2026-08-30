# Metrics

Prometheus text exposition, written directly rather than through a
client library. SPEC section 26.2.

This covers where the metrics are, how to point an external Prometheus
at them, what each family means, and what is worth alerting on.

## Where they are, and what can reach them

| Service | Port | Path | Authentication |
|---|---|---|---|
| `halite-api` | 4511 | `/v1/metrics` | a bearer token |
| `halite-hub` | 4510 | `/v1/metrics` | an operator certificate **and** the `metrics.show` grant |

Both follow the `listen` setting; there is no separate metrics port and
no way to move the path. `metrics: false` turns the recording off
entirely, on either service.

**Point the scraper at `halite-api`.** It answers with both expositions
— its own and the hub's — merged into one document, and it is the only
part of the control plane an ordinary client can reach.

The hub cannot be scraped directly, by Prometheus or by anything else
that does not speak halite's own ALPN identifier. It looks like this,
with or without a client certificate:

```
$ curl https://hub.example:4510/v1/metrics
curl: (35) TLS connect error: error:0A000438:SSL routines::tlsv1 alert internal error
```

That is the ALPN gate of [DIVERGENCE 1.7](DIVERGENCE.md), not a
certificate problem, and TLS 1.3 has no way to say so in the alert. A
node exposes nothing at all: it has no listener to expose it on, and
what it does appears on the hub as jobs, returns, and events.

## Setting up an external scraper

Three grants are involved, and missing any one of them fails quietly in
a different way.

### 1. The API's own certificate needs `metrics.show` at the hub

The API is a client of the hub like any other, so reading the hub's
exposition is a request the policy has to permit. In `policy.yaml`:

```yaml
roles:
  scraper:
    - runners: ['metrics.show']
bindings:
  - principal: 'cert:CN=api'
    roles: ['scraper']
```

`cert:CN=api` is whatever `api_operator` names in `api.yaml`, which
defaults to `api`. A role with `runners: ['metrics.show']` and nothing
else is enough — this was checked by giving the API a role without it
and watching the hub's half of the exposition disappear.

Check the binding before restarting anything. `policy test` reads the
file rather than asking the running hub, so it answers for an edit that
is not live yet:

```sh
halite-hub policy test 'cert:CN=api' '*' metrics.show --runner
```

```
allowed by role "scraper" rule 0
```

`--runner` is not optional here. `metrics.show` is a runner, and without
the flag the same command is evaluated as a job against a target and
comes back denied — a denial that says nothing about the binding you
meant to test.

A principal with no binding at all says so plainly, which is the shape
this mistake usually takes:

```
denied: cert:CN=api is bound to no role in /usr/local/etc/halite/policy.yaml
```

The hub reads the policy once at startup and has no SIGHUP handler, so
an edit needs `service halite_hub restart` before it takes effect.

**A missing grant here does not fail the scrape.** Prometheus keeps
reporting `up == 1`, and the exposition simply arrives without any
`halite_hub_*` family in it — the API serves its own and drops the half
it could not read. `halite_api_hub_scrape_failures_total` is the signal,
and the [alerting rules](#alerting) below watch it for exactly this.

### 2. The scraper needs an account and the same grant

A scraper is a machine identity, which is what local accounts are for —
OIDC and LDAP are the operator path and there is nobody to log in here.
Generate a password, keep it where the token will be generated from, and
hash it:

```sh
head -c 32 /dev/urandom | base64 > /etc/prometheus/halite.password
chmod 600 /etc/prometheus/halite.password
halite-api account hash < /etc/prometheus/halite.password
```

`account hash` reads the password from standard input rather than from
an argument, which would reach the process table and the shell history.
It prints the verifier and never the password.

Put that in the account file — `<config root>/accounts.yaml` unless
`accounts` in `api.yaml` says otherwise:

```yaml
accounts:
  prometheus:
    hash: 'pbkdf2-sha512$600000$NjtK87YzEN8VZnUqJr0…'
    roles:
      - scraper
```

The `roles` here and a binding in `policy.yaml` are two routes to the
same thing, and an account gets the union of both. One or the other is
enough:

```yaml
# policy.yaml, if you would rather keep the grants in one file
bindings:
  - principal: 'local:prometheus'
    roles: ['scraper']
```

The file holds password verifiers, so give it mode 0600 and the account
`halite-api` runs as.
[`contrib/examples/accounts.yaml`](../contrib/examples/accounts.yaml)
is a worked one with every field, and
[configuration.md](configuration.md) has the `accounts` setting.

Check it before going further:

```sh
halite-api account list
```

### 3. Issue it a token

A scraper cannot log in again when its token runs out, so the defaults
are wrong for one. `token_lifetime` is 12h: a token minted in the
afternoon is dead by the next morning, and the scrape starts failing
overnight with nothing to say why. Set it in `api.yaml` before minting:

```yaml
# api.yaml — a scraper's token has to outlive a working day
token_lifetime: 8760h
```

`token_idle` is 4h and cannot be turned off: zero means the default
rather than "no idle expiry", and only a negative value disables it.
That is harmless for a scraper, which uses the token every scrape
interval and never goes idle, but it does mean a token parked for an
afternoon stops working.

Log in once and keep the token:

```sh
token=$(curl -sS --fail-with-body \
    --cacert /etc/prometheus/halite-api-ca.crt \
    -X POST https://api.example:4511/v1/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"prometheus","password":"…"}' | jq -r '.token // empty')

test -n "$token" || { echo "login failed" >&2; exit 1; }
( umask 077; printf '%s\n' "$token" > /etc/prometheus/halite.token )
```

Written in three steps on purpose. The obvious one-liner —
`curl -s … | jq -r .token > token` — writes an empty file when the
connection fails and the word `null` when the login is refused, and says
nothing either way: `-s` silences curl's error and the redirection has
already truncated the file by the time `jq` sees there is nothing to
extract. The form above leaves no file at all unless there is a token in
it.

The host in the URL has to be one the certificate covers. Connecting to
`https://localhost:4511` with a certificate whose names are
`api.example` and an address fails verification, and with `-s` that
failure is invisible.

### 4. Point Prometheus at it

```yaml
scrape_configs:
  - job_name: halite
    scheme: https
    scrape_interval: 30s
    metrics_path: /v1/metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/halite.token
    tls_config:
      # The certificate halite-api presents, which is its own and not
      # the enrollment CA — see "The API's serving certificate" in
      # operations.md for where it comes from. For a self-signed one,
      # this is that same file.
      ca_file: /etc/prometheus/halite-api-ca.crt
    static_configs:
      - targets: ['api.example:4511']
```

Both files have to be readable **by the account Prometheus runs as**,
which is not the one halite runs as. Do not point either setting inside
halite's own directories:

```
/usr/local/etc/halite/pki   drwxr--r--   halite:wheel
```

That directory has no execute bit for anyone but `halite`, so nothing
else can open a file inside it however permissive the file itself looks.
A `ca_file` under `pki/` fails for the scraper even though `root` and
the operator can both read it perfectly well. Give Prometheus its own
copies:

```sh
install -o root -g wheel -m 0644 \
    /usr/local/etc/halite/pki/api.crt \
    /usr/local/etc/prometheus/halite-api-ca.crt
install -o prometheus -g prometheus -m 0600 \
    /dev/null /usr/local/etc/prometheus/halite.token
```

and check it as that account rather than as yourself:

```sh
su -m prometheus -c 'cat /usr/local/etc/prometheus/halite.token'
```

The serving certificate is `api.crt`, not `ca.crt`. The enrollment CA
signs node identities and does not sign this, so pointing `ca_file` at
it fails verification even once the permissions are right.

**A permission failure here produces no target at all**, which is worse
than a failing one. Prometheus cannot build the scrape pool, so there is
nothing to be down: `up{job="halite"}` is empty rather than 0, the
target is missing from `/api/v1/targets`, and every alert in this
document is silent because they all match on a series that was never
created. The only evidence is a log line, every scrape interval:

```
level=error component="scrape manager" msg="error creating new scrape pool"
err="error creating HTTP client: unable to read CA cert: ...: permission denied"
scrape_pool=halite
```

So `absent(up{job="halite"})` belongs in the rules below alongside
anything watching `up` itself — see [Alerting](#alerting).

Validate the file before restarting; `promtool` checks that both files
exist and are readable, which catches this:

```sh
promtool check config /usr/local/etc/prometheus.yml
```

Then confirm the endpoint by hand:

```sh
curl -sS --fail-with-body --cacert /usr/local/etc/prometheus/halite-api-ca.crt \
  -H "Authorization: Bearer $(cat /usr/local/etc/prometheus/halite.token)" \
  https://api.example:4511/v1/metrics | head
```

The stanza above is accepted by `promtool check config`. The endpoint,
the token, and both grants have been exercised with `curl` standing in
for the scraper; the paths shown are FreeBSD's, and a Linux host puts
the same files under `/etc/prometheus/`.

## What is exposed

All families are prefixed `halite_`. The hub carries most; `halite_api_*`
are the API's own.

| Family | Type | Labels | What it says |
|---|---|---|---|
| `halite_build_info` | gauge | `component` `version` `commit` `go_version` `fips` | Always 1. What is deployed, per component. |
| `halite_hub_nodes_connected` | gauge | — | Nodes holding an open subscribe stream, relayed ones included. |
| `halite_hub_node_connect_total` | counter | — | Connections accepted. |
| `halite_hub_node_disconnect_total` | counter | `reason` | The other half, and why. |
| `halite_hub_keys_accepted` | gauge | — | Nodes holding an issued certificate. |
| `halite_hub_keys_pending` | gauge | — | Enrollment requests waiting for an operator. |
| `halite_jobs_dispatched_total` | counter | `fun` | Jobs sent, by function. |
| `halite_job_returns_total` | counter | `result` | Returns filed, by outcome. |
| `halite_job_duration_seconds` | histogram | `fun` | How long jobs take. |
| `halite_jobs_missing_returns` | gauge | — | Nodes a dispatched job has not heard from. |
| `halite_jobs_expired_total` | counter | — | Jobs whose TTL passed before every node answered. |
| `halite_state_states_total` | counter | `result` | Individual states applied, by outcome. |
| `halite_state_changes_total` | counter | — | States that changed something rather than converging. |
| `halite_pillar_compile_duration_seconds` | histogram | — | Time to compile one node's pillar. |
| `halite_pillar_failures_total` | counter | — | Compilations that failed, so a node got no pillar. |
| `halite_fileserver_requests_total` | counter | `backend` `code` | Tree fetches. |
| `halite_fileserver_bytes_total` | counter | — | Bytes served. |
| `halite_events_published_total` | counter | `tag_prefix` | Events reaching the bus. |
| `halite_events_dropped_total` | counter | `reason` | Events that did not. |
| `halite_reactor_duration_seconds` | histogram | `tag_prefix` | Render and dispatch time for one reaction. |
| `halite_reactor_dropped_total` | counter | — | Reactions the queue could not hold. |
| `halite_reactor_failures_total` | counter | `reason` | Reactions that failed. |
| `halite_beacon_events_total` | counter | `beacon` | Beacon events received from nodes. |
| `halite_authz_decisions_total` | counter | `result` | Every policy decision, allowed and denied. |
| `halite_auth_attempts_total` | counter | `method` `result` | Logins at the API, by backend. |
| `halite_api_requests_total` | counter | `route` `code` | API requests. |
| `halite_api_request_duration_seconds` | histogram | `route` | How long they take. |
| `halite_api_event_streams` | gauge | `transport` | Event streams open, SSE and WebSocket. |
| `halite_api_hook_deliveries_total` | counter | `path` `result` | Webhook deliveries received. |
| `halite_api_hub_scrape_failures_total` | counter | — | Times the API could not read the hub's exposition. |

Every family is declared before anything is observed, so its `# HELP`
and `# TYPE` are in the exposition from the start. What follows them
depends on whether the family carries labels:

- **Unlabelled**, and never fired: one series at zero —
  `halite_reactor_dropped_total 0`. An alert on it works from the first
  scrape.
- **Labelled**, and never fired: the declaration and no series at all,
  because there is no label value to name one. Prometheus has no data
  for it until something is observed, so `increase(...) > 0` cannot
  fire — which is the answer you want, but not because the counter said
  zero.

The distinction matters when a rule is not firing and you are deciding
whether that is good news. `halite-hub metrics --filter` keeps the
`# HELP` lines, which is how to tell a family that is quiet from one
this build never registers.

### Families that appear only when the feature runs

Registered by the subsystem that owns them, because a gauge reading a
queue that does not exist would read zero for ever:

| Family | Appears when |
|---|---|
| `halite_reactor_queue_depth` | `reactor:` has entries and the reactor is running |
| `halite_relay_subordinates`, `halite_relay_upstream_connected`, `halite_relay_spool_entries`, `halite_relay_spool_dropped_total`, `halite_relay_returns_forwarded_total`, `halite_relay_events_forwarded_total` | `relay: true` |

An alert against one of these on a hub that is not a relay never fires,
and not because nothing is wrong.

### What SPEC 26.2 names and this build does not have

Eleven of the specification's thirty-two families are not registered, so
an alert written from its table rather than from this one sits silent:
`halite_state_run_duration_seconds`,
`halite_state_compile_duration_seconds`,
`halite_pillar_cache_hits_total`, `halite_pillar_ext_failures_total`,
`halite_gitfs_fetch_duration_seconds`,
`halite_gitfs_signature_failures_total`,
`halite_event_subscriber_lag_seconds`, `halite_beacon_dropped_total`,
`halite_ext_invocations_total`, `halite_ext_duration_seconds`,
`halite_ext_timeouts_total`. [DIVERGENCE 5.23](DIVERGENCE.md) records
why.

## The series cap

A family holds at most 512 series. Past that, further label values are
counted together under `__overflow__` rather than dropped, so the total
stays right and the overflow is visible:

```
halite_jobs_dispatched_total{fun="__overflow__"} 1483
```

Seeing it means a label is taking unbounded values — most likely `fun`
on an estate running hundreds of distinct functions. The number is still
correct; the breakdown is not. There is no setting: an exposition that
can grow without limit is a way to run the scraper out of memory from
the estate being scraped.

## Monitoring

What is worth a dashboard, in rough order of how often it answers a
question:

- **`halite_hub_nodes_connected`** against `halite_hub_keys_accepted`.
  The gap is machines that should be talking and are not, and it is the
  single most useful number on the estate.
- **`rate(halite_jobs_dispatched_total[5m])` by `fun`.** What the estate
  is actually doing.
- **`halite_state_changes_total`.** On a converged estate this is flat
  between deployments. A step means something changed; a slope means
  something is not converging and is being re-applied every run.
- **`histogram_quantile(0.95, rate(halite_job_duration_seconds_bucket[5m]))`
  by `fun`.** Which functions are slow, and whether that is new.
- **`halite_hub_keys_pending`.** Enrollment requests nobody has decided
  on. On a manual-enrollment estate this should return to zero.

## A dashboard to start from

[`contrib/examples/grafana-dashboard.json`](../contrib/examples/grafana-dashboard.json)
is an importable Grafana dashboard over these families. In Grafana:
**Dashboards → New → Import → Upload JSON file**, then pick the
Prometheus that scrapes halite-api.

It asks for one thing on import, the data source. Two variables at the
top pick what you are looking at:

| Variable | What it is |
|---|---|
| `job` | the scrape job name in `prometheus.yml`, `halite` unless you renamed it |
| `instance` | which `halite-api` to read, when more than one is scraped |

The rows follow the sections above: fleet health, jobs, states, pillar,
events and reactions, the file server, authentication and policy, the
API's own service metrics, and a collapsed row for relays. Every panel
carries a description saying what a reading means — hover the title.

Two panels are worth knowing about before you need them:

- **Scrape** reads `up`, and is empty rather than 0 when the scrape pool
  failed to build. An empty stat there means the whole dashboard is
  showing nothing for a reason that has nothing to do with halite.
- **Hub scrape failures** is the one that says the dashboard is lying to
  you: while it climbs, every `halite_hub_*` panel is empty because the
  API could not read the hub's half, not because the fleet is idle.

The relay row is collapsed because those families exist only on a hub
running with `relay: true`. An empty panel there on an ordinary hub is
correct.

Every query in it names a family this build registers, which is checked
by a test — a panel written against a metric that does not exist draws
an empty graph rather than an error, which looks exactly like a fleet
with nothing happening in it. The thresholds and the panel choices have
not been tuned against a large estate; they are a starting point.

## Alerting

Every metric in the rules below exists in this build, which is checked
by a test, and the block is accepted by `promtool check rules`. They
have not been evaluated against live data, so the thresholds are a
starting point rather than tuned figures.

```yaml
groups:
  - name: halite
    rules:
      # First, that the scrape exists at all. A scrape pool that fails
      # to build — an unreadable ca_file or token — registers no target,
      # so `up` is absent rather than 0 and every other rule here goes
      # quiet without firing. This is the only rule that catches that.
      - alert: HaliteScrapeMissing
        expr: absent(up{job="halite"})
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "No halite target exists; check prometheus.yml and the log"

      - alert: HaliteScrapeDown
        expr: up{job="halite"} == 0
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "halite-api is not answering the scrape"

      # Nothing should ever be dropped. Both of these are bounded
      # queues whose overflow SPEC 26.2 requires be counted.
      - alert: HaliteEventsDropped
        expr: increase(halite_events_dropped_total[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "The event bus dropped {{ $value }} events"

      - alert: HaliteReactorDropped
        expr: increase(halite_reactor_dropped_total[10m]) > 0
        labels: {severity: warning}
        annotations:
          summary: "Reactions were discarded; the queue overflowed"

      # A node getting no pillar. SPEC 12.7 prefers that to a partial
      # one, which makes this the signal that something is wrong rather
      # than quietly half-configured.
      - alert: HalitePillarFailing
        expr: increase(halite_pillar_failures_total[15m]) > 0
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "The hub cannot compile pillar for some node"

      # Machines that hold a certificate and are not connected.
      - alert: HaliteNodesAbsent
        expr: halite_hub_keys_accepted - halite_hub_nodes_connected > 0
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "{{ $value }} enrolled nodes are not connected"

      # A rate rather than a total: a rise is either a role somebody
      # got wrong or somebody trying.
      - alert: HaliteAuthzDenials
        expr: rate(halite_authz_decisions_total{result="denied"}[5m]) > 0.2
        for: 10m
        labels: {severity: warning}

      - alert: HaliteLoginRefusals
        expr: rate(halite_auth_attempts_total{result="refused"}[5m]) > 0.2
        for: 10m
        labels: {severity: warning}

      # The API cannot read the hub. The scrape still succeeds and
      # returns the API's own numbers, so nothing else tells you.
      - alert: HaliteHubUnreadable
        expr: increase(halite_api_hub_scrape_failures_total[10m]) > 0
        labels: {severity: critical}
        annotations:
          summary: "The API cannot reach the hub's exposition"

      # Jobs dispatched that nobody answered.
      - alert: HaliteJobsUnanswered
        expr: halite_jobs_missing_returns > 0
        for: 15m
        labels: {severity: warning}

      # A label taking unbounded values. The totals stay right; the
      # breakdown stops being useful.
      - alert: HaliteSeriesOverflow
        expr: count({__name__=~"halite_.*", fun="__overflow__"}) > 0
        labels: {severity: info}
```

On a relay, add its spool — an outage that is not draining is the thing
you want to know about before the cap is reached:

```yaml
      - alert: HaliteRelaySpoolGrowing
        expr: halite_relay_spool_entries > 0 and halite_relay_upstream_connected == 1
        for: 15m
        annotations:
          summary: "The relay is connected upstream and its spool is not draining"
```

### What not to alert on

`halite_state_states_total{result="failed"}` looks tempting and is
usually noise: a state that fails once because a package mirror was
briefly down is not an incident. Alert on the rate over a window long
enough to survive one bad run, or on the estate not converging —
`halite_state_changes_total` still climbing hours after a deployment.

## When the hub cannot be reached

The scrape still succeeds. The API returns its own numbers, and the
reason the hub's are missing appears as a comment, which a scraper
ignores:

```
# the hub's metrics are absent: /v1/metrics: no rule in [apionly] permits metrics.show against ""
halite_api_hub_scrape_failures_total 1
```

The counter increments in the scrape that failed, not the one after —
checked by scraping twice against a hub the API was not permitted to
read and watching it go 1, 2. That is why `HaliteHubUnreadable` above
uses `increase(...) > 0` rather than waiting for a gap in the data:
there is no gap, and every hub metric simply stops being reported.

## Reading them without a scraper

```sh
halite-hub metrics --as ed
halite-hub metrics --as ed --filter reactor
```

`--filter` keeps the `# HELP` and `# TYPE` lines, which `grep` loses and
which say whether a counter exists at all — the difference between a
metric that is quiet and one that was never registered.
