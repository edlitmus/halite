# Plan: driving simfleet at scale on AWS

**Status: step 0 has run twice (2026-11-08).** The first run, on
`1bd9a6c8`, stopped the plan: at 5,000 nodes the hub did not survive one
`test.ping`. The two defects it found were fixed (#689, #688). The
re-run on `dbbf053d` met both SPEC 30 rows at 5,000, and the hub peaked
at 1.6 GB (§11.1). §10 has the order of work from here. The next steps
that bill, 1 to 3, still need approval.

The region (us-east-1), the use of two existing test hosts (host B and
host C, §2), and a `DryRun` launch were approved on 2026-11-08. No AWS
resource has been created. The launches were dry runs only. Host C was
rebooted once to recover it, after the first run (§11). The AWS facts
come from read-only calls made on 2026-11-08 in a test account. The
claims about halite come from the code at `bde5719b` and `1bd9a6c8`.
Where this page states what a run *will* show, it is a hypothesis, and
it is marked as one.

The purpose is the run that `plan.md` 3.1a and DIVERGENCE 5.279 say is
still owed. Before step 0, `tools/simfleet` had run once, at 200 nodes
on a workstation. None of SPEC 30's harness rows has been measured.

## 1. What simfleet is today

`tools/simfleet` (`make simfleet`, `bin/simfleet`) is one Go program
with two subcommands:

- `simfleet setup -root DIR -n N [-prefix P]` writes a throwaway hub
  configuration (`hub.yaml`, a `policy.yaml` that binds `cert:CN=simfleet`
  to `*` for functions and runners), an enrolment CA, an operator
  certificate, and N node identities. It issues the identities in process
  through `keystore.Authority` (Enroll, Accept, Enroll), against the
  key store under `DIR/state/keys`. **It does not use `/v1/enroll`.**
- `simfleet run -root DIR -hub ADDR -n N [-ping] [-apply]
  [-reuse-client | -client-per-return] [-ramp R] [-hub-pid PID] [-hold D]` loads N identities, opens one
  mutual-TLS subscribe stream per node through `transport.Client` at R
  connects per second, then optionally times one `test.ping` (SPEC 30:
  95% of returns in 10 s) and one `state.apply` dispatch (SPEC 30: full
  dispatch in 30 s) to `P-*`.

What is real: the hub (`halite-hub serve`), its key store, CA, policy,
dispatch, fleet and job cache; each node's TLS identity and stream. What
is simulated: the work. A node answers `test.ping` with `true` and
anything else with `{}`, at once.

### 1.1 Limits found by reading the code

These are read from the source, not measured. Each needs to be fixed or
worked around before the step that depends on it.

| # | Limit | Where | Affects |
|---|---|---|---|
| L1 | **Fixed (DIVERGENCE 5.640):** `run -no-submit`, `submit`, `merge`. **One process submits and measures its own nodes.** `run` submits to `P-*` and times only the sims in its own process. Several driver hosts give several separate jobs, not one job to the whole fleet. | `measure` | Every step above one driver |
| L2 | **Fixed (5.640):** times are kept per jid. **Only one measured job may be in flight.** Any job that arrives during the window is timed as the measured one (DIVERGENCE 5.279). | `answer`, `measure` | Return-volume step |
| L3 | **Fixed (5.643):** both use `internal/nodelink`'s pacer. **Reconnect pacing does not match the node.** A sim waits a fixed 1 s and retries. `halite-node` uses exponential backoff from 1 s to 60 s with ±20% jitter (`internal/nodelink/reconnect.go`, moved from `cmd/halite-node`; #597). A reconnect storm from simfleet is harsher and more synchronised than a real fleet. | `connect` | Reconnect storm |
| L4 | **Fixed (5.643):** both use `nodelink.PostReturn`. **Return retry does not match the node.** A sim counts a failed return and gives up. `postReturns` in `halite-node` tries 5 times with a growing delay. Under overload, a real fleet adds load and simfleet removes it. | `answer` | Every step near saturation |
| L5 | **No batch.** `run` sends no `Batch` field, although `transport.SubmitRequest` has `Batch`, `BatchWaitSeconds`, `BatchSafeLimit` and `BatchTimeoutSecs`. | `measure` | Batch step |
| L6 | **No enrolment over the wire.** `setup` bypasses `/v1/enroll`. That path is what an enrolment storm stresses, with its per-source limit (32 at once, then one every 10 s) and `DefaultMaxPending` = 1,024 (`internal/keystore/authority.go`). | `setupCmd` | Enrolment storm |
| L7 | **Hub memory only on the same host.** `-hub-pid` reads `ps -o rss=` locally. The hub registers no process or Go runtime metrics (no resident memory, goroutines or file descriptors in `/v1/metrics`), so a hub on another host must be sampled there. | `rss`, `internal/hub/metrics.go` | Every step |
| L8 | **Fixed (5.640):** `-connect-timeout`. **Connect wait is fixed at 5 minutes.** Below 1,000/s per driver it is too short for large fleets. | `runCmd` | Steps ≥ 20k |
| L9 | **Fixed (5.640):** `-json`, and the drivers' files. **Output is prose only.** No machine-readable result to archive or merge across drivers. | `measure` | Every step |
| L10 | **Fixed (5.641):** `halite-node` did the same, so the defect was the node's; both now keep one returns client per node. **One client per job, never closed.** Each job makes a new `transport.Client` (unless `-reuse-client`). The old one's idle connection stays open until `IdleConnTimeout` (90 s). Whether `halite-node`'s `hubClient` does the same has **not** been checked. That is a pair that must agree. | `connect` | Return volume, FDs |

### 1.2 Limits on the hub side, found by reading the code

- **SPEC 6.5's 25,000-connection cap is not enforced.** SPEC 6.5 says
  "Maximum node connections per hub: 25,000, configured", and handbook
  chapter 38 says "default max 25,000 per hub". `internal/transport/wire.go`
  says that the connection count and the concurrent-stream count are among
  the limits that "arrive with the endpoints that need them", and no
  code in `internal/hub` or `internal/transport` counts connections.
  The handbook claims a limit that does not exist. That is a defect in
  the prose (or a missing feature), separate from this plan. See §9.
- **File descriptors.** `contrib/systemd/halite-hub.service` sets
  `LimitNOFILE=65536`. With 25,000 streams, plus one connection per
  return that can stay idle for 90 s (L10), the hub could need more than
  50,000 descriptors during one `test.ping`. This is arithmetic, not a
  measurement. Since 5.641 a node keeps one returns connection, so the
  same arithmetic gives about 50,000 at most whatever the job rate,
  and a fleet of older nodes still gives the first figure.
- **Enrolment limits** (L6) are deliberate. An enrolment storm from a
  few driver addresses is *meant* to be refused. That test checks that
  the limiter holds and the hub stays healthy. It does not check
  throughput.

## 2. What exists in AWS (read-only, 2026-11-08)

The test account has three existing hosts, in us-east-1. This page
calls them by letter; their names, instance IDs and addresses are
specific to that account and are left out.

| Host | Type | Arch | OS |
|---|---|---|---|
| host A | c7g.2xlarge (8 vCPU, 16 GiB) | arm64 | Ubuntu 22.04 |
| host B | c7g.2xlarge (8 vCPU, 16 GiB) | arm64 | Ubuntu 24.04 |
| host C | c8g.2xlarge (8 vCPU, 16 GiB) | arm64 | Ubuntu 24.04 |

All three are SSM `Online`, in one VPC and one public subnet. They
carry an instance profile whose role has `AmazonSSMManagedInstanceCore`
and nothing for S3.

**Security groups (not to be changed):** the hosts' security group
allows SSH and ICMP from internal ranges, and one other TCP and UDP port
from the whole VPC. Egress is open.

So the only port open between hosts in the VPC, other than SSH, is that
one, called `<port>` on this page. The hub's default port, 4510, is
closed. **The plan runs the hub with `--listen <private-ip>:<port>`.**
No security group change is needed, and no new security group is
needed. The port belongs to another service that is not running on
these hosts. That is not checked on new instances, but they start from
a stock image that does not have it.

**Network:** six private /23 subnets (one per AZ, about 500 free
addresses each) route `0.0.0.0/0` through a NAT gateway per AZ. They
have gateway endpoints for S3 and DynamoDB. There are no interface
endpoints, so SSM works through the NAT. The /26 "public" subnets route
through an internet gateway.

**Quotas:** on-demand standard vCPU 26,356 and spot standard vCPU 1,152
in us-east-1. The largest layout below uses about 120 vCPU, so quota is
not a constraint.

**Prices** (Pricing API, us-east-1, Linux, on-demand, 2026-11-08):
c7g.large $0.0725/h, c7g.xlarge $0.145/h, c7g.2xlarge $0.29/h,
c7g.4xlarge $0.58/h, c7g.8xlarge $1.16/h, c6a.8xlarge $1.224/h,
c7i.8xlarge $1.428/h; gp3 $0.08 per GB-month.

## 3. The AWS layout

### 3.1 Placement

- **Region us-east-1, one AZ, one private /23 subnet** (about 500
  free addresses). One AZ keeps
  traffic free (same-AZ private traffic is not billed; cross-AZ traffic
  is $0.01/GB each way) and keeps latency out of the numbers. A private
  subnet means no public address, so the hub is reachable only inside
  the VPC.
- **A cluster placement group** for the hub and the drivers, so that the
  network between them is not the variable. This is one more resource
  to create and delete. It is optional at ≤ 10k.
- **The existing hosts' security group only**, attached as it is.
- **Access only through SSM** (Session Manager and Run Command). No SSH
  keys, no public addresses.
- **The existing instance profile**, so SSM works. Passing it needs
  `iam:PassRole` on that role. **It has.** `RunInstances` with
  `DryRun=true` answered "Request would have succeeded" (2026-11-08) for
  c7g.4xlarge and c7g.xlarge. Each call carried this instance profile,
  subnet and security group, no public address, IMDSv2 required,
  terminate-on-shutdown, a 50 GiB gp3 root, and the tags in §3.3 on the
  instance and the volume. `CreatePlacementGroup` (cluster) with
  `DryRun=true` answered the same.
- **AMI: Canonical Ubuntu 24.04 arm64**, resolved at apply time from the
  public SSM parameter
  `/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id`.
  Not the image the existing hosts run. Those hosts run a configuration
  management agent, and a copy of that image could enrol itself with the
  estate's configuration server. That risk is an assumption. Nobody has
  checked it, and a stock image avoids it.

### 3.2 Roles and sizes

| Role | Type | Why |
|---|---|---|
| **Hub** | **c7g.4xlarge** (16 vCPU, 32 GiB), $0.58/h | SPEC 30 names "one 16-core hub". A Graviton vCPU is a physical core, so 16 vCPU is 16 cores. On x86 a vCPU is a hyperthread, so the x86 match is a 32-vCPU size (c6a.8xlarge, $1.224/h). arm64 also matches all three existing hosts and host B's build. 32 GiB is 8 times the 4 GiB target, so memory pressure cannot hide a result. |
| **Driver** | **c7g.xlarge** (4 vCPU, 8 GiB), $0.145/h, **5,000 sims each** | See below. |
| **Observer and build host** | **host B**, as it is | Already running and billing. Builds the arm64 binaries (never host A, which is too small). Runs Prometheus and Grafana against the hub. Runs `simfleet submit` (H1). |

**Why 5,000 sims per driver (part arithmetic, part assumption):**

- *Ports (arithmetic).* All of a driver's connections go from one source
  address to one hub `ip:<port>`, so each needs its own ephemeral port.
  Linux's default range (32768-60999) has 28,232 ports. 5,000 streams,
  plus 5,000 new connections per job that stay idle for up to 90 s (L10),
  plus TIME_WAIT, leaves room for about two jobs a minute. At 10,000 per
  driver, two jobs inside 90 s would run out. Drivers widen the range to
  `1024 65535` anyway. Since 5.641 that is the `-client-per-return`
  case only: by default a driver uses two ports per node, 10,000 at
  5,000 nodes, whatever the job rate.
- *Memory and CPU (assumption).* A Go TLS client with an HTTP/2
  transport and its goroutines is guessed at 50-200 KiB. 5,000 sims
  would then use 0.25-1 GiB, well inside 8 GiB. The handshake burst of a
  connect ramp is the CPU risk. **Step 0 measures this**, and the size
  per driver changes if the measurement says so.

### 3.3 Raising and tearing down

A new OpenTofu module, **`contrib/tofu/aws-simfleet/`**, kept apart from
the Vultr lab (`contrib/tofu/`), which uses another provider, another
key and another network. It follows the lab's conventions: versions
pinned in `versions.tf`, lock file committed, state local and ignored
(`contrib/tofu/.gitignore` already ignores `*.tfstate` under it).

- Inputs: `region`, `subnet_id`, `security_group_id`,
  `instance_profile`, `hub_type`, `driver_type`, `driver_count`,
  `placement_group` (bool), `ttl_minutes` (default 240).
- Resources: one hub instance, `driver_count` driver instances, an
  optional placement group. **No security group, no IAM, no network
  resources.** Root volumes are gp3 with `delete_on_termination = true`:
  hub 50 GiB, drivers 20 GiB.
- Tags on everything: `Name=simfleet-<role>-<n>`, `Project=halite-simfleet`,
  `Environment=<environment>`, `Owner=<owner>`, `ExpiresAt=<RFC 3339>`.
  Whether an account's tag policy needs more tags depends on the
  account.
- **A dead man's switch:** `instance_initiated_shutdown_behavior = "terminate"`,
  and user-data that runs `shutdown -h +${ttl_minutes}` at boot. An
  instance that is forgotten terminates itself after 4 hours. This does
  not replace teardown (§7). It limits the cost of forgetting.
- Outputs: instance IDs and private IPs, as `lab.sh`-style JSON, so the
  run script can address hosts by role.

The module now exists, with H6's sampler in `kit/`; its README says
what has and has not been run. Nothing has been raised with it. `tofu plan` reads AWS and creates nothing. `tofu apply` is
the step that bills, and it needs the user's approval every time.

## 4. The scale steps

Each step runs only if the step before it passed. Each step keeps all of
its raw output: simfleet stdout written to a file with `tee` (never piped
through `tail`), the Prometheus data directory, and the hub-host samplers.

| Step | Sims | Where | Proves |
|---|---|---|---|
| **0. Smoke on existing hosts** | 1,000 then 5,000 | Hub on **host C**, one driver on **host B** | The mechanism across hosts on `<port>`. Per-sim cost on the driver (sizes §3.2). The owed break-on-purpose of `setup`'s re-run fix (5.279): revert the skip, grow 1,000 to 5,000, see the refusal, restore it, see it pass. Hub resident memory read on the hub host (fixes 5.279's wrong `-hub-pid`). The FreeBSD-shaped `ps -o rss=` call on Linux only. **$0 added**, but it uses shared hosts (§8). |
| **1. Dedicated hub, one driver** | 5,000 | New hub + 1 driver | The new layout works. A baseline for hub CPU and memory per 1,000 connections. |
| **2. SPEC 30's dispatch rows** | 10,000 | Hub + 2 drivers | `test.ping` to 10,000: 95% of returns ≤ 10 s. `state.apply` dispatch to 10,000: all received ≤ 30 s. Needs H1. |
| **3. Nodes per hub, hub memory** | 20,000 | Hub + 4 drivers (+1 spare) | 20,000 connected and held for 30 min. Hub RSS < 4 GiB. Both dispatch rows again at 20k (beyond SPEC, as information). Reconnect storm. Batch. Return volume. |
| **4. The hard limit** | 25,000, then 26,000 | Hub + 6 drivers | SPEC 6.5's 25,000 cap. **Expected to fail today**, because nothing enforces it (§1.2). The test records what happens instead: whether the 25,001st node connects, and at what FD and memory cost. |
| **5. 50,000 through relays** (optional) | 50,000 | Hub + 2 relays (c7g.4xlarge) + 10 drivers | SPEC 30's "100,000 per estate with relays" at half scale, two relays of 25,000 each. One hub cannot take 50,000 inside SPEC, so a 50,000 run against one hub only finds where it breaks. That is a different question, and it is worth asking only after step 4. Needs `setup` against each relay's key store. Relay identity handling has **not** been checked against simfleet. |
| **3F. FreeBSD hub** | 20,000 | Step 3 with a FreeBSD 14 arm64 hub | FreeBSD is SPEC 27.1 tier 1 and about 80% of production. **No SPEC 30 row should be called met for this estate on a Linux hub alone.** Whether this account can launch the official FreeBSD AMIs has **not** been checked. FreeBSD tuning differs (`kern.maxfiles`, `kern.ipc.somaxconn`, `net.inet.ip.portrange.*`). |

### 4.1 The scenarios inside a step

- **Connect ramp:** ramp total connects/s across drivers (for example 500,
  then 2,000, then unthrottled). Measure time to all connected,
  `halite_hub_node_connect_total`, and hub CPU.
- **Dispatch and return:** `-ping` and `-apply`, one job at a time (L2),
  with at least 120 s between jobs, so that idle connections (L10) and
  TIME_WAIT from one job do not count against the next.
- **Return volume:** 10 pings in a row at 120 s intervals, then the same
  with `-reuse-client` and with `-client-per-return`. This puts a number
  on the per-return connection cost that 5.279 left open: the default is
  a node since 5.641, and `-client-per-return` is one built before it. Watch hub FDs, `ss -s`, and
  `halite_hub_request_duration_seconds{route="/v1/return"}`.
- **Batch:** `state.apply` with `batch: 10%` and then `batch: 500`
  (needs H4). Check that no more than one batch is in flight, and
  measure the time from batch to batch.
- **Reconnect storm:** with N connected, `kill -TERM` the hub, start it
  again, and measure time to N reconnected, the peak hub CPU, and the
  peak RSS. Run it twice: with the fixed 1 s pacing of today, and with
  the node's own backoff (needs H2). Only the second one says anything
  about a real fleet.
- **Enrolment storm:** 2,000 enrolments over the wire from the drivers
  (needs H5). First run in `manual` mode: the limiter refuses most of
  them, `halite_hub_enrollments_total{result="rate_limited"}` climbs,
  pending stays ≤ 1,024, and the hub's other routes keep their latency.
  Then run in `token` mode for throughput. SPEC 30 has no enrolment row,
  so these criteria are ours, and the record says so.

### 4.2 Harness work needed first

Each item is code, gets its own PR, and gets tested on its own. They are
listed in the order the steps need them.

| # | Change | Needed by |
|---|---|---|
| H1 | **Done (5.640)**, except the chrony offsets, which belong to the run kit. Split submit from measure. `run -no-submit` records, for each sim, the jid and the arrival and return times, as JSON. A new `simfleet submit` sends one job to the whole fleet. A `simfleet merge` reads the drivers' files and computes p50, p95 and last against the submit time. Clocks: Amazon Time Sync through chrony on every host, with `chronyc tracking` offsets recorded before and after each run. Sub-millisecond offset is **assumed**, and that assumption is checked by the recorded offset. | 2+ |
| H2 | **Done (5.643):** `internal/nodelink`. Use the node's reconnect pacing. Move `reconnect` out of `cmd/halite-node` into a shared internal package, so that the two cannot drift. | Reconnect storm |
| H3 | **Done (5.643):** `nodelink.PostReturn`. Retry returns as `postReturns` does. Same reason. | 3+ |
| H4 | Pass through `-batch`, `-batch-wait`. | Batch |
| H5 | `simfleet enroll-storm`: real `/v1/enroll` calls, with optional token. | Enrolment storm |
| H6 | **Done:** `contrib/tofu/aws-simfleet/kit/sampler.sh`, run on host B. A hub-host sampler (a script in the run kit, not in the binary): every 5 s, write the `halite-hub` RSS (`ps -o rss=`), FD count (`/proc/PID/fd`), threads, `ss -s`, and the ENA counters (`ethtool -S`: `conntrack_allowance_available`, `conntrack_allowance_exceeded`, `pps_allowance_exceeded`, `bw_in_allowance_exceeded`, `bw_out_allowance_exceeded`). | All |
| H7 | **Done (5.640):** `-connect-timeout`. A flag for the connect deadline. | 3+ |
| H8 | **Done (5.640):** `-json`. `-json` output for each measurement. | All |
| H9 | **Done (5.641):** the node did the same, and was fixed with it. Check L10 against `halite-node`, and make simfleet do what the node does. | Return volume |

Also worth doing, but not this plan: process and Go runtime metrics on
the hub (`process_resident_memory_bytes`, `go_goroutines`,
`process_open_fds`). They would make L7 and H6 unnecessary, and any
operator would want them. They are a new metric family, and that is a
SPEC 26.2 conversation.

## 5. What to measure, and where

| Source | What | How |
|---|---|---|
| **simfleet** | Connected count and time; received and returned p50, p95 and last; return errors; SPEC 30 MET or NOT MET | stdout written with `tee` to `/root/simfleet/runs/<step>/<host>.txt`, and `-json` once H8 exists. Copied back over `<port>` or SSM. |
| **Hub `/v1/metrics`** | `halite_hub_nodes_connected`, `halite_hub_node_connect_total`, `halite_hub_node_disconnect_total{reason}`, `halite_jobs_dispatched_total`, `halite_job_returns_total`, `halite_jobs_missing_returns`, `halite_hub_request_duration_seconds{route}`, `halite_hub_requests_total{route,code}`, `halite_hub_enrollments_total{result}`, `halite_events_dropped_total`, `halite_event_subscriber_lag_seconds` | ~~Prometheus scrapes it directly.~~ **It cannot.** The hub refuses any TLS client that does not offer the `halite/1` ALPN (`transport.requireProtocol`), and step 0's curl was refused that way. Use `halite-hub metrics --hub <ip>:<port> --cert operator.crt --key operator.key --server-name <ip>` in a loop on the observer, writing exposition files. A Prometheus that needs live data would need a small ALPN-speaking proxy. That loop has **not** been run. |
| **Grafana** | The panels in `contrib/examples/grafana-dashboard.json`: nodes connected, connects and disconnects, missing returns, dispatched, returns, enrolments, request duration by route | Only if the scrape above feeds a Prometheus. The dashboard has **no** hub-process panel, because the hub exports no such metric (L7). |
| **Hub host** | RSS, FDs, threads, sockets, ENA allowance counters | H6 sampler, to `/root/simfleet/runs/<step>/hub-samples.tsv` |
| **CloudWatch** | `CPUUtilization`, `NetworkIn`, `NetworkOut`, `NetworkPacketsIn` for the hub and drivers | Basic monitoring (5-minute, free) as an independent check of CPU and of the network. Detailed monitoring (1-minute) costs a few cents a run, and is worth turning on for step 3. Memory needs the CloudWatch agent. The plan does not install it, and gets memory from H6 instead. |
| **Drivers** | CPU, RSS of simfleet, ephemeral ports used, `ss -s` | The H6 script, pointed at the simfleet PID |

### 5.1 Pass and fail

A row is MET only when every condition holds, on the hardware named, and
the record names the machine, OS, kernel, Go version, commit, driver
count, and what was not covered. A NOT MET is a result, and it is
recorded the same way.

| Step | Pass | Fail |
|---|---|---|
| 0 | All N connected; ping and apply reach N of N; zero return errors; RSS line printed; the `setup` revert fails and the fix passes | Anything else. Also records sims per driver: driver RSS ÷ N and peak CPU during the ramp. |
| 1 | 5,000 of 5,000 connected; zero disconnects while idle for 10 min; hub CPU idle < 10% | Disconnects while idle, or not all connected in the deadline |
| 2 | **SPEC 30:** ping p95 ≤ 10 s with 10,000 of 10,000 returned; apply last-received ≤ 30 s with 10,000 of 10,000; `halite_jobs_missing_returns` back to 0 | Either SPEC condition; any 5xx on `/v1/return`; any missing return that persists |
| 3 | **SPEC 30:** 20,000 connected and held 30 min with zero unexplained disconnects; **hub RSS < 4 GiB** at the highest sample; the step 2 rows at 20,000 (information only); reconnect storm (node pacing) back to 20,000 within 5 min, with hub RSS still < 4 GiB (**our criterion**, not SPEC's); batch never has more than one batch in flight; return volume shows no FD growth across 10 pings | Any SPEC condition; FDs reaching `LimitNOFILE`; any `conntrack_allowance_exceeded` greater than 0 (that is a property of AWS, recorded as such, not as a hub defect) |
| 4 | The hub refuses connection 25,001 with a clear error, and the 25,000 already connected stay healthy | **Expected today:** it accepts. Recorded as a defect against SPEC 6.5, with the FD and memory cost of 26,000. |
| 5 | 50,000 reached through two relays; a ping submitted upstream returns from 50,000 | Either |
| 3F | As step 3, on FreeBSD | As step 3 |

## 6. Cost per run

On-demand us-east-1 prices from §2. gp3 is under $0.05 a run at these
sizes and is left out. Same-AZ traffic is free. Binaries go from host B
to the new hosts inside the VPC, so NAT data is only package installs
(Prometheus and Grafana on host B, under 1 GB, about $0.05). Hours are
an estimate of wall time with the instances up, including setup.

| Run | Instances | $/h | Hours | Estimate |
|---|---|---|---|---|
| Step 0 | existing hosts | 0 added | 2 | **$0 added** |
| Steps 1-3 in one session | hub + 5 drivers | 0.58 + 5 × 0.145 = 1.305 | 4 | **≈ $5.20** |
| Step 4 | hub + 6 drivers | 1.45 | 2 | **≈ $2.90** |
| Step 5 | hub + 2 relays + 10 drivers | 0.58 + 1.16 + 1.45 = 3.19 | 3 | **≈ $9.60** |
| Step 3F | FreeBSD hub + 5 drivers | ≈ 1.305 | 4 | **≈ $5.20** |
| **All steps** | | | | **≈ $23**, under $50 if every run takes twice as long |

The dead man's switch caps a forgotten run at 4 hours: about $5 for
steps 1-3, about $13 for step 5.

## 7. Teardown, so nothing bills afterwards

In this order, in the same session that raised the instances:

1. Copy the run outputs back (simfleet files, H6 samples, a Prometheus
   snapshot from host B) to a scratch folder, and record them.
2. `tofu destroy` in `contrib/tofu/aws-simfleet/`.
3. **Check with read-only calls, not with tofu's word for it**, in
   us-east-1:
   - `DescribeInstances`, filter `tag:Project=halite-simfleet`, state
     not `terminated`: **must be empty**.
   - `DescribeVolumes`, filter `tag:Project=halite-simfleet`: **must be
     empty** (root volumes are delete-on-terminate. Check anyway).
   - `DescribePlacementGroups` by name: **must be empty**.
   - `DescribeNetworkInterfaces`, filter description or tag: **must be
     empty**.
4. On the existing hosts: stop Prometheus, Grafana, any `http.server`
   on `<port>`, and any hub or simfleet process. Remove `/root/simfleet`
   (`/tmp` is noexec there, so work lives under `/root`). Confirm that
   the hosts' own agent is still active (`systemctl is-active` on it)
   and that `dmesg` shows no OOM kill.
5. Write in the run record that teardown was checked, with the time.

## 8. Risks

- **Shared hosts.** Host B and host C are test hosts that other work
  uses (FIPS work, issue 306). Step 0 puts a hub and 5,000 connections
  on them. Ask before using them, and check for other running work
  first. Host A is not used, and is never a build host.
- **The configuration agent on the existing hosts.** Load on those
  hosts could starve it, as an earlier OOM did. Step 0 stays at 5,000.
- **`<port>` belongs to another service in this VPC's security group.**
  If anything else in the VPC speaks that service's protocol to these
  addresses, it reaches the hub's TLS listener and fails the
  handshake. That is harmless to both, but it adds noise to
  `halite_hub_requests_total`.
- **AWS connection tracking.** The security group's ingress rule is by
  CIDR, so every connection is tracked. The tracked-connection allowance
  of a c7g.4xlarge is not published. At 25,000 streams, plus returns,
  it could limit the run before the hub does. H6 records
  `conntrack_allowance_exceeded` so that this cannot be mistaken for a
  hub result.
- **simfleet is harsher than a fleet in some ways and gentler in
  others** (L3, L4, L10). A NOT MET from the harness before 5.641 and
  5.643 may be the harness: H9, H2 and H3 are done, so a sim now
  reconnects, retries and holds connections as a node does. A reconnect
  or saturation result from an older build should be run again.
- **One-AZ, one-placement-group numbers are best case.** Production
  nodes are on other networks, with more latency and loss.
- **Billing:** forgetting teardown. The dead man's switch limits it,
  and §7 checks it.

## 9. What is not known yet

- ~~Whether us-east-1 is the region meant.~~ Confirmed.
- ~~Whether the account's role can pass the instance profile, launch instances
  with these tags, and create a placement group.~~ It can. All three
  passed `DryRun` (§3.1). The `DryRun` also covered the tag policy for
  these tags.
- Whether the official FreeBSD AMIs can be launched in this account.
- ~~Per-sim memory and CPU on a driver.~~ About 200 KiB per sim with
  returns in flight (1.0 GB for 5,000 streams, 1.3 GB at peak), and
  little CPU (§11).
- ~~Whether the hub's TLS handshake rate or its per-return work is the
  first limit.~~ The per-return work, by a wide margin, on the evidence
  of §11. That is not yet proven by a profile.
- How long `simfleet setup` takes at 20,000. 5,000 took 283 s, and the
  cost per thousand rose in a straight line (§11). Extrapolating that
  line gives about an hour for 20,000. That is arithmetic, not a
  measurement.
- ~~Whether `halite-node` also leaves one idle connection per return
  (L10, H9).~~ It did, and no longer does (5.641).
- Whether relays can be fed by simfleet without changes (step 5).
- What the hub does at 25,000 connections (nothing stops it, §1.2).

Two findings from this reading are worth fixing on their own, outside
this plan:

1. **Handbook chapter 38 says the hub's default connection maximum is
   25,000. Nothing enforces it.** Either the limit is built (SPEC 6.5
   says "configured"), or the handbook says it is not enforced.
2. **simfleet's reconnect and return-retry pacing differ from
   `halite-node`'s** (L3, L4). These are two paths that must agree, and
   today nothing holds them together.

## 10. Order of work, and what needs approval

Revised after step 0. Items 1, 3 and 5 of the first order are done.

1. ~~Approve this plan, and confirm the region.~~ Done 2026-11-08.
2. ~~Step 0 on host C and host B.~~ Done 2026-11-08 (§11).
3. ~~`RunInstances` `DryRun` for permissions.~~ Done 2026-11-08 (§3.1).
4. ~~**Fix the hub's return path**~~ Done: #689, merged as
   `adcbcc89`. The keystore's per-name scan was fixed too: #688, merged
   as `c1a61e7e`.
5. ~~**Re-run step 0 at 5,000 with that fix**~~ Done 2026-11-08 on
   `dbbf053d` (§11.1). 5,000 of 5,000 returns, hub peak 1.61 GB:
   passed.
6. Harness PRs: ~~H8, H1, H7~~ done (5.640); ~~H6~~ done
   (`contrib/tofu/aws-simfleet/kit/sampler.sh`); ~~H9~~ done (5.641);
   ~~H2, H3~~ done (5.643); then H4, H5. *(code; no AWS)*
7. ~~`contrib/tofu/aws-simfleet/` PR~~ written, with a mocked
   `tofu test`; `tofu plan` against the test account ran on 2026-11-09:
   2 to add, nothing else. Nothing applied.
8. ~~Step 1~~ done 2026-11-09 (§11.2), applied and torn down in one
   session. Steps 2-3 next, one session, `tofu apply`, then teardown.
   *(ask first; it bills)*
9. Step 4. Step 3F. Step 5 only if 4 passed and the relay work is done.
   *(each asked for separately)*
10. Each result goes into DIVERGENCE 5.279 as a subsection, into
    `internal/perf`'s rows, into `plan.md` 3.1a, and into handbook
    chapter 38's table, in the same change as the run.

## 11. Step 0 results (2026-11-08)

Full numbers, setup and caveats are in DIVERGENCE 5.279. In short:

**Hosts.** Hub on host C (c8g.2xlarge, 8 vCPU, 15.7 GiB), fleet on
host B (c7g.2xlarge). Both are arm64 Ubuntu 24.04 on a FIPS kernel,
and both run other agents. Built from `1bd9a6c8` with Go 1.27.3. The hub
listened on `<private-ip>:<port>`.

| | 1,000 nodes | 5,000 nodes |
|---|---|---|
| Connected | 1,000 in 1.6 s | 5,000 in 6.8 s |
| `test.ping` received | p95 248 ms | last 361 ms |
| `test.ping` returned | 1,000 of 1,000, p95 4.07 s: **MET** at this size | **1,508 of 5,000**, p50 47 s, 3,492 errors: **NOT MET** |
| `state.apply` dispatch | last 217 ms: MET | `Submit` blocked 25.5 s, then the last node had it at 26.05 s. The harness printed MET, which is an artefact of its clock (L11 below). |
| Hub RSS | 118 MB idle, 928 MB peak | **14.2 GB within 60 s, 15.3 GB at the end** |
| Hub host | fine | thrashed with no free memory, SSM lost, rebooted after 16 min |

**Other results:**
- `setup`'s re-run fix was broken on purpose and held.
- setup's rate fell from 50 to 11 identities a second as the store grew
  (the keystore's `countPending` reads every record per new name).
- The metrics scrape plan was wrong (§5).
- A driver needs about 200 KiB per sim.

**L11 (new; addressed in 5.640: each result now also shows the last arrival after the hub accepted the job).** `measure`'s dispatch clock starts before `Submit`. When
`Submit` itself blocks, the time goes on the dispatch clock, and the
verdict reads MET on a hub that took 25 s to accept the job. The dispatch
row needs the time from `Submit` returning, or both times shown.

**Cleanup, checked.** On both hosts: no simfleet units, no processes, no
`/root/simfleet`, nothing listening on `<port>`, the configuration agent active, and
no OOM this boot. No AWS resource exists for this run.

### 11.1 Re-run after the fixes (2026-11-08)

The same hosts, harness and order of runs, built from `main` at
`dbbf053d`, which has #689 and #688 but not the connection cap. The hub
unit added `MemoryMax=12G` as a guard against another thrash. It was
never reached. The full numbers are in DIVERGENCE 5.279.

| | 1,000 nodes | 5,000 nodes | 5,000, `-reuse-client` |
|---|---|---|---|
| Connected | 1,000 in 1.65 s | 5,000 in 6.7 s | 5,000 in 6.6 s |
| `test.ping` returned | 1,000 of 1,000, p95 465 ms: **MET** | **5,000 of 5,000**, p95 1.87 s: **MET** | 5,000 of 5,000, p95 1.43 s: MET |
| `state.apply` | submit 321 ms, last received 290 ms: MET | submit 803 ms, last received 842 ms: **MET** | submit 273 ms, last 287 ms: MET |
| Return errors | 0 | 0 | 0 |
| Hub peak RSS | 353 MB | **1.61 GB** | 0.99 GB |

- `setup` issued 5,000 identities in 73.4 s, at about 15 s per thousand
  throughout. Before the fix it took 283 s and slowed as it went.
- The hub logged no TLS handshake errors. The ENA allowance counters
  stayed at 0.
- `halite-hub metrics`, run from the driver, worked every time.
  `halite_job_returns_total{result="succeeded"}` read 22,000, which
  matches the harness's count exactly.
- The driver peaked at 1.27 GB for 5,000 sims, the same as before.
- With the submit now taking under a second, L11 no longer changes a
  verdict at this size. It is still a harness defect, which H1 fixes.
- Cleanup was checked on both hosts as above.

**What this does not show.** Neither SPEC row at its own size (10,000
and 20,000), the 4 GiB row at 20,000, a 16-core hub, FreeBSD, or a
profile. 1.6 GB at 5,000 is encouraging for the 4 GiB row, but it does
not extrapolate to 20,000 without a run. That is step 1's job.

### 11.2 Step 1, a dedicated hub and one driver (2026-11-09)

Raised with this module (c7g.4xlarge hub, one c7g.xlarge driver,
stock Ubuntu 24.04 arm64), built from `main` at `0026d9fa`. The user
data was checked on both hosts first: the halt was scheduled, the
sysctls were set, and chrony read about 2 µs from Amazon Time Sync. The
full numbers are in DIVERGENCE 5.279.

| 5,000 nodes | Result |
|---|---|
| Connected | 5,000 in 7.7 s |
| `test.ping` returned | 5,000 of 5,000, p95 1.88 s: **MET** |
| `state.apply` | submit 300 ms, last received 300 ms: **MET** |
| 10 min hold | 0 disconnects |
| Hub idle CPU | 7.5% of one core (0.47% of 16): passes step 1's < 10% |
| Hub peak RSS | 1.50 GB (with 10,000 idle return connections open); 900 MB holding 5,000 streams |

- The ENA `conntrack_allowance_available` read about 1.23 million on
  the c7g.4xlarge, and every exceeded counter stayed at 0. That answers
  §8's question about this size's tracked-connection allowance.
- About 180 KB per idle stream would be about 3.6 GB at 20,000 before any
  job. That is arithmetic, not a measurement; step 3 measures it. Fixing
  L10 (H9) first would remove the 10,000 idle return connections from
  the peak. It is fixed (5.641), and how much of the peak that removes
  has not been measured.
- Teardown was checked with read-only calls: both instances
  `terminated`, nothing left with the project tag. About 25 minutes of
  instance time, under $0.50.

**Not covered:** 10,000 and 20,000, more than one driver, FreeBSD, and a
profile of the idle CPU.
