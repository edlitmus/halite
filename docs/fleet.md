# Fleet mode: control plane and agents

Masterless halite converges one host. Fleet mode adds a control plane that
holds the state and pillar trees, keeps track of which hosts exist, and
dispatches work to them. It is the same binary in a different mode.

An agent does not execute anything the control plane sends it as code. It
fetches the state tree as an archive and its pillar as JSON, then runs the
same loader and engine `halite apply` runs locally. A converged host looks
identical whether an operator triggered it or cron did.

## Roles

| Role | Certificate | May |
|---|---|---|
| control plane | `master.crt` | serve |
| agent | `agent.crt` | say hello, poll for work, fetch pillar and the state tree, return results |
| operator | `admin.crt` | everything an agent may, plus dispatch work and list the fleet |

The role travels inside the certificate as an organizational unit, stamped
by the CA at signing time. Nothing a caller sends can change its own role,
and an agent certificate cannot dispatch work — a compromised agent that
could dispatch would own the fleet.

## Setting up

On the control plane host:

```sh
halite key init -cn "acme fleet ca"
halite key server master.example.com -san 10.0.0.1
halite key admin ed                      # your operator certificate

halite master -root /usr/local/etc/halite/states
```

On each managed host, install `ca.crt` into the PKI directory and start the
agent:

```sh
install -m 0644 ca.crt /usr/local/etc/halite/pki/ca.crt
halite agent -master master.example.com -id web1
```

The agent generates its own key, prints its fingerprint, and waits. Back on
the control plane:

```sh
halite key list
# pending    web1    6a:12:39:24:ab:...
halite key accept web1
```

The agent picks up its certificate on its next attempt and connects. See
[pki.md](pki.md) for the enrollment rules.

`-auto-accept` signs requests without asking. It is for labs and
disposable test fleets: with it on, any host that can reach the port
becomes a fleet member.

## Running work

```sh
halite agents                                    # who is out there
halite run '*' state.highstate -test             # dry run everywhere
halite run 'os_family:FreeBSD' state.apply web.nginx
halite run 'web*' call pkg.installed name=nginx
halite run '*' grains
halite run 'web1' pillar
```

Targets use the same language as a top file: `'*'` for everything,
`grain:valueglob` for a fact, a bare glob for the host's identity, Salt's
`G@`/`L@`/`E@`/`P@` spellings, and `and`/`or`/`not` combinations of those:

```sh
halite run 'web* and not L@web9' state.highstate       # all but one host
halite run '(db* or cache*) and os_family:FreeBSD' state.apply tuning
```

The full table is in
[writing-states.md](writing-states.md#targeting). A target that does not
parse — including `I@`, `S@`, `N@`, and `R@`, which halite does not
implement — is refused at dispatch rather than reported as an empty fleet.

`halite run` waits for results (`-wait`, default two minutes) and prints
them per agent, in the same format as a local `halite apply`. It exits
non-zero if any agent failed or if any targeted agent never answered.

Set `HALITE_MASTER` to avoid repeating `-master` on every command.

At boot, neither daemon should be run from a command line in `rc.conf`:
each reads a config file, and there are rc.d scripts and systemd units
for both. See [service.md](service.md).

Watch what the fleet is doing with `halite events` — see
[events.md](events.md).

An agent can also run work on its own clock, so the fleet converges
without anything poking it:

```sh
halite agent -master master.example.com -schedule /usr/local/etc/halite/schedule.sls
```

See [the scheduler](events.md#the-scheduler).

## The `id` grain

Under a control plane, an agent's `id` grain is the identity it enrolled
as, not its hostname — that is the name operators target and the name that
appears in results. The control plane overwrites the reported value with
the one in the client certificate, so a host cannot target-spoof by lying
about its grains. Masterless, `id` is simply the hostname.

## How work reaches an agent

```
agent                                     control plane
  |--- POST /v1/hello (grains) ---------->|  registry: who is online
  |--- GET  /v1/jobs -------------------->|  held open until there is work
  |<-- [job] -----------------------------|
  |--- GET  /v1/pillar ------------------>|  rendered for this agent's grains
  |--- GET  /v1/statetree --------------->|  tar.gz of the tree
  |    extract, then engine.Run(...)      |
  |--- POST /v1/results ----------------->|
  |--- POST /v1/renew ------------------->|  once a year, near expiry
```

Job polls are long-lived HTTP/2 requests: the control plane holds one open
until work arrives or the poll window expires, and the agent immediately
comes back. Job delivery stays a poll even though there is now an event
stream, because agents need work pushed at them and operators need to
watch — see [events.md](events.md).

Queued work expires. An agent that was down when a job was dispatched
picks up nothing older than the job TTL (five minutes by default), because
replaying an operator's hours-old intent on a host that has just come back
is rarely what anyone wants.

## What the control plane does not do

* **It does not store history.** Agents and job results live in memory. A
  restart forgets which hosts were online and what ran; agents reconnect on
  their next poll. Durable results are what
  [returners](events.md#returners) are for.
* **It does not push.** Nothing connects to an agent, so agents work from
  behind NAT and need no inbound firewall rules.
* **It does not encrypt pillar at rest.** The tree lives here and only
  here; agents receive their own rendered subset over mTLS and never write
  it to disk. Protect the tree with permissions — see
  [pillar-security.md](pillar-security.md).
* **It does not run minion-supplied code.** Traffic is JSON in both
  directions; nothing is deserialized into behavior.

## More than one control plane

An agent takes several addresses and uses one at a time:

```sh
halite agent -master master1.example.com,master2.example.com -id web1
```

It tries the list from the top on every reconnection, so it prefers the
first that answers and returns to it once that one is back. Failing over
needs no re-enrolment, provided the masters share a CA:

```sh
# on the second control plane, with ca.crt and ca.key copied from the first
halite key server master2.example.com
```

Each master still keeps its own record of pending enrolment requests, so a
host enrolling for the first time is accepted on whichever master it
reached.

**This is failover, not a cluster.** Masters share nothing: not the agent
registry, not job results, not the mine, not events. An operator commands
the master its agents are connected to, so if half the fleet has failed
over, a dispatch on the primary reaches half the fleet.

That makes one address in front of several masters — DNS or a load
balancer — the topology to aim for, so "which master" is a deployment
decision rather than a per-agent accident. Run the reactor on one master:
two reactors watching two buses react twice. See ADR-11.

## Ports and files

| | |
|---|---|
| Port | 4506/tcp, `-addr` to change |
| PKI | `-pki`, `$HALITE_PKI`, else the platform path (see pki.md) |
| State tree | `-root`, `$HALITE_ROOT`, else the platform path |
| Pillar tree | `-pillar-root`, `$HALITE_PILLAR_ROOT`, else beside the states |
| Agent cache | `-cache`, else `/var/cache/halite` |

Timing, where the defaults are not what a site wants:

| Flag | On | Default | Bounds |
|---|---|---|---|
| `-poll-timeout` | `master` | 30s | how long an agent's job poll is held open before it is answered empty |
| `-orch-timeout` | `master` | 30m | a whole orchestration, so a stuck step cannot hold the control plane forever |
| `-retry` | `agent` | 10s | the delay between reconnection and enrollment attempts |

Both daemons shut down cleanly on SIGINT and SIGTERM.
