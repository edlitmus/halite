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

`cert:CN=api` is whatever `api_operator` names in `api.yaml`. A role
with `runners: ['metrics.show']` and nothing else is enough — this was
checked by giving the API a role without it and watching the hub's half
of the exposition disappear.

### 2. The scraper needs an account and the same grant

```yaml
bindings:
  - principal: 'local:prometheus'
    roles: ['scraper']
```

Then an account for it in `accounts.yaml`, with a password generated
from bytes nobody keeps:

```sh
halite-api account hash < /dev/urandom_or_your_secret_store
```

### 3. Issue it a token

A scraper does not log in, so give the token a long life and no idle
expiry — `token_lifetime` and `token_idle` in `api.yaml` govern both.
Log in once and keep the token:

```sh
curl -s --cacert /etc/ssl/halite-api.crt \
  -X POST https://api.example:4511/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"prometheus","password":"…"}' |
  jq -r .token > /etc/prometheus/halite.token
chmod 600 /etc/prometheus/halite.token
```

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
      # the enrollment CA.
      ca_file: /etc/prometheus/halite-api-ca.crt
    static_configs:
      - targets: ['api.example:4511']
```

Confirm it by hand before trusting the scraper:

```sh
curl -s --cacert /etc/prometheus/halite-api-ca.crt \
  -H "Authorization: Bearer $(cat /etc/prometheus/halite.token)" \
  https://api.example:4511/v1/metrics | head
```

The Prometheus configuration above has not been run against a live
Prometheus; the endpoint, the token, and both grants have been, with
`curl` standing in for the scraper.

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

## Alerting

The rules below are illustrative — they have not been loaded into a live
Prometheus — but every metric in them exists in this build, which is
checked by a test.

```yaml
groups:
  - name: halite
    rules:
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
