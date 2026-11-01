# Runbook: monitoring and alerting

<!-- abstract: What to watch and how: doctor for health, Prometheus metrics from the API and nodes, the numbers that answer most questions, alert rules, logs, the event stream and a dashboard. -->
<!-- kind: runbook -->
<!-- index: monitoring; alerting; metrics; Prometheus; /v1/metrics; metrics.show; metrics_listen; halite_hub_nodes_connected; halite_hub_keys_accepted; halite_hub_ca_expiry_seconds; halite_state_changes_total; Grafana dashboard; promtool; doctor; logging; log level; log format; redaction; tracing; OpenTelemetry; SLO; on-call -->

**Use this runbook to** know, before your users tell you, that the control plane
is healthy, the fleet is connected, certificates are not about to lapse, and
changes are converging.

**You will end with** a Prometheus scraping the estate, alerts for the handful
of conditions that matter, a dashboard, and a place logs and events go.

## Before you start

- A **Prometheus** (or any scraper of the text exposition format) and,
  optionally, **Grafana**.
- The **API running** ([chapter 32](32-install-api.md)): it is the one place an
  ordinary client can reach the whole control plane's metrics.
- Decide what pages a person and what only draws a graph. The list below
  suggests which.

## What there is to watch

Four sources, each with a different job:

| Source | Answers | Cost |
|---|---|---|
| **`doctor`** on each program | "Is this one thing healthy right now, and what do I do about it?" | None; changes nothing |
| **Metrics** (Prometheus) | "How is the estate behaving over time?" | One scrape job |
| **The event bus** | "What just happened?" | A subscriber or an event returner ([chapter 23](23-scheduler-and-returners.md)) |
| **Logs** | "Why?" | Disk |

### Health: `doctor`

```sh
halite-hub doctor
halite-node doctor
halite-api doctor
```

Each runs the checks that apply to it, prints `pass`, `warn`, `fail` or `skip`
(with the reason), and gives a remediation line for anything not a pass. It
exits non-zero **only on a failure, not a warning**, so it is safe in a
monitoring probe and in a cron job. A node runs eleven checks (configuration,
certificate expiry, connectivity, clock skew against the hub, file server,
pillar compilation, disk space, extension signatures, module verification,
evidence chain, FIPS consistency); a hub eight; the API four. A warning is a
thing to do this month, not a reason to page anybody.

## Procedure

### 1. Point Prometheus at the API

The API's `/v1/metrics` answers with **both its own metrics and the hub's**,
merged. (The hub cannot be scraped directly by a general client; it speaks its
own ALPN identifier over mutual TLS.) Authenticate the scraper like any other
client:

1. Give the scraper an **operator certificate and a policy role** that includes
   the `metrics.show` runner, or an API token whose role does.
2. Issue a long-lived token (a scraper cannot notice its own credential
   expiring): `token_lifetime` for the account, or a service account.
3. In `prometheus.yml`:

   ```yaml
   scrape_configs:
     - job_name: halite
       scheme: https
       metrics_path: /v1/metrics
       authorization: { type: Bearer, credentials_file: /etc/prometheus/halite.token }
       tls_config: { ca_file: /etc/prometheus/halite-api-ca.crt }
       static_configs: [{ targets: ['api.example.com:4511'] }]
   ```

The full walkthrough, including the exact grants, is [Metrics](../metrics.md)
in this handbook. Check by reading the same data by hand:

```sh
halite-hub metrics --filter halite_hub_nodes
```
```
# HELP halite_hub_nodes_connected Nodes with a live subscribe stream.
# TYPE halite_hub_nodes_connected gauge
halite_hub_nodes_connected 3
```

### 2. Scrape the nodes, if you want node-side metrics

A node dials the hub and is dialled by nothing; its metrics endpoint is the one
deliberate exception and is **off until you set `metrics_listen`**. There is no
plaintext mode: it needs a serving certificate, and optionally requires a
client certificate. The hub-side view already covers jobs, returns and events,
so the node endpoint adds what never reaches the hub: extension timeouts,
local queue drops, and so on. Skip it unless you need that; the detailed setup,
with the two certificates it needs, is in [Metrics](../metrics.md#a-nodes-metrics).

### 3. The numbers that answer most questions

In the order they usually help:

| Metric | What it says | Alert? |
|---|---|---|
| `halite_hub_nodes_connected` vs `halite_hub_keys_accepted` | The gap is machines that should be talking and are not: **the single most useful number** | Yes: gap > N for 10 minutes |
| `halite_hub_ca_expiry_seconds` | Seconds until the enrolment CA expires. Reaching zero takes the whole estate with it, and nothing renews it | Yes: < 90 days (page at 30) |
| `halite_hub_keys_expiring` / `_expired` / `halite_hub_soonest_certificate_expiry_seconds` | Node certificates near the end | Yes: any `expired`; `expiring` that does not go to 0 |
| `halite_hub_keys_pending` | Enrolment requests nobody decided | Ticket if > 0 for a day (manual mode) |
| `halite_state_changes_total` | States that changed something. Flat on a converged estate; a slope = something re-applied every run | Yes: sustained rate outside a deployment |
| `rate(halite_jobs_dispatched_total[5m])` by `fun` | What the estate is doing | Graph |
| `histogram_quantile(0.95, …halite_job_duration_seconds_bucket…)` | Slow functions, and whether it is new | Graph |
| `halite_jobs_missing_returns`, `halite_jobs_expired_total` | Jobs nodes did not answer | Yes if rising |
| `halite_hub_evidence_anchors_total{result="conflict"}` | A node whose chain was rewritten, reset or rolled back | **Yes: page** |
| `halite_pillar_failures_total`, `halite_pillar_ext_failures_total` | Nodes getting no pillar, or missing what a source held | Yes |
| `halite_reactor_dropped_total`, `halite_reactor_failures_total` | The reactor overflowed or failed | Yes |
| `halite_beacon_dropped_total` | A node's beacon queue overflowed | Ticket |
| `halite_authz_decisions_total{result="denied"}` | Denials: someone's role is wrong, or someone is trying | Graph; alert on a sudden rise |
| `halite_auth_attempts_total{result="throttled"}` | Sustained = somebody guessing at a name or spraying names from one address | Yes |
| `halite_returns_foreign_schema_total` | A node newer than the hub | Non-zero during an upgrade, zero after |
| `halite_api_hub_scrape_failures_total` | While this climbs, every `halite_hub_*` panel is empty because the API could not read the hub's half | Yes |
| `up` | The scrape itself | Yes |

Every bounded queue and every drop path in the design has a counter behind
it, which is why the list is long: the rule that makes backpressure auditable
and not aspirational.

A family holds at most 512 series; past that, further label values are
counted together under `__overflow__`, so the total stays right and the
overflow is visible. Seeing it means a label is taking unbounded values.

### 4. Install the alert rules

[Metrics](../metrics.md#alerting) carries a complete rule file (CA expiry,
certificate expiry, nodes missing, convergence, reactor, evidence conflict,
login throttling, scrape failure and more). Every metric it names exists in
this build, which a test checks, and `promtool check rules` accepts the block.
It has **not** been evaluated against live data, so treat the thresholds as a
starting point and tune them over your first month. Page on few things: the CA
expiring, an evidence conflict, the hub or API unreachable, a large gap in
connected nodes. Everything else is a ticket.

```sh
promtool check rules halite.rules.yml
```

### 5. Import the dashboard

`contrib/examples/grafana-dashboard.json` (in the archive as
`examples/grafana-dashboard.json`) is a Grafana dashboard over these families:
fleet health, certificates and enrolment, jobs, states and orchestration,
pillar, events and reactions, file server, authentication and policy, evidence
anchors, the hub's and API's own service metrics, collapsed rows for node
agents and relays, and the build. In Grafana: **Dashboards → New → Import →
Upload JSON file**, and pick the Prometheus that scrapes the API. Every panel
carries a description saying what a reading means. To pick up a new version,
re-import and choose Overwrite when the `uid` exists.

### 6. Logs

All three programs log structured JSON, one object per line, to standard
error (the journal under systemd, a file under rc.d). Fields on every record:
`ts` (RFC 3339), `level`, `msg`, `component`, `node_id` and, where they apply,
`jid`, `principal`, `correlation`, `sls`, `state_id`, `file`, `line`.

```sh
halite-node connect --log-level debug --log-fmt console       # for a person
halite-hub serve --log-level info --log-fmt json              # for a collector
```

Levels: `error`, `warn`, `info` (default), `debug`, `trace`. There is one
`log_level` per process (per-component levels are not built). **Secrets are
redacted at the sink**: the redactor is seeded with every decrypted pillar
value, token and configured secret, and scrubs them from logs, events and error
messages, so a value cannot escape through a path that forgot. A template that
fails shows its source line (`debug` shows each file as it renders).

Ship logs to your collector as you do everything else; make sure `halite/…`
authorization decisions (they are `info` lines: `job authorized fun=… principal=…
role=… rule=…`) are retained as long as your audit policy needs.

### 7. The event stream as a monitor

For everything the metrics do not say, read the bus. A small set of tags is
worth an alert in itself; send them through `event_return`
([chapter 23](23-scheduler-and-returners.md)):

| Tag | Means |
|---|---|
| `halite/reactor/error`, `halite/reactor/overflow`, `halite/reactor/loop` | A reaction failed, the queue overflowed, a causality chain was broken |
| `halite/node/*/evidence/conflict` | A node's evidence head contradicts the hub's |
| `halite/key/**` | Accepting, rejecting, revoking or deleting a key through the hub's runners (`key.accept` and the rest), and renewals: an audit trail of those. A `halite-hub keys` command run at a terminal is another process and publishes nothing |
| `halite/beacon/**` | Whatever your beacons were set to notice |
| `halite/error/**` | A structured error worth reacting to |

### 8. Tracing (optional)

W3C Trace Context is propagated with a span per job, state and file transfer,
exported as OTLP over HTTP/JSON. It is off by default and sampled when on; see
the reference for the settings. Turn it on when you are chasing latency across
the hub and nodes, not as routine.

## Verify

You are monitoring the estate when you can answer, in under a minute and from
your own tools:

1. **How many nodes are connected, and how many should be?**
   (`halite_hub_nodes_connected`, `halite_hub_keys_accepted`.)
2. **When does the CA expire, and when does the soonest node certificate?**
3. **Is anything not converging?** (`halite_state_changes_total`.)
4. **Did anyone do something unexpected in the last hour?** (the event stream,
   `halite/key/**`, the job cache.)
5. And you have **proved an alert fires**: stop a lab node and watch
   `halite_hub_nodes_connected` fall below `keys_accepted`; take a scrape
   target down and see `up` alert. An alert nobody has seen fire is a hope.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| The scrape is `401`/`403` | The token's role lacks `metrics.show`, or the token expired | Fix the role; use a long-lived token for the scraper |
| Every `halite_hub_*` panel is empty | The API cannot read the hub's metrics (`halite_api_hub_scrape_failures_total` climbs) | `halite-api doctor`; the operator certificate to the hub |
| `curl` to the hub's port fails with a TLS alert | Correct: the hub is not scrapeable directly | Scrape the API |
| No node metrics | `metrics_listen` is not set, or the serving certificate is wrong | The node logs "the metrics endpoint is not serving" once; read the log |
| `__overflow__` appears | An unbounded label (usually `fun`) | The total is correct; the breakdown is not; reduce distinct values |
| Counters reset on restart | Counters are per process | Use `rate()`/`increase()`, which handle resets |

## Key ideas

- **`doctor`** for point-in-time health, **metrics** for trends, the **event
  bus** for what just happened, **logs** for why.
- **Scrape the API**: it merges the hub's metrics with its own.
- Alert on few things and page on fewer: **nodes connected vs accepted**, **CA
  expiry**, **evidence conflict**, **hub/API down**.
- `halite_state_changes_total` should be flat on a converged estate.
- Prove an alert fires before you rely on it.

Next: [backup and recovery](35-backup-and-recovery.md).
