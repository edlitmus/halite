# Remote execution

<!-- abstract: Running functions and states on many machines from the hub: arguments, output, exit codes, batching, canaries, asynchronous jobs and offline nodes. -->
<!-- kind: guide -->
<!-- index: remote execution; halite-hub run; arguments; key=value; output format; exit code 3; batch; batch-safe-limit; batch-wait; subset; canary; progress; async; timeout; ttl; offline; queue; require; operator certificate; state.apply; test mode fleet; drift report -->

Configuration management keeps machines in a described state over time.
**Remote execution** is the other half: you ask a set of machines a
question, or tell them to do something, *now*, and see the answers come
back together.

```sh
halite-hub run <target> <function> [arguments] [flags]
```

That one command is everything in this chapter. The target is from
[chapter 13](13-targeting.md); the function is any execution function
from [chapter 11](11-modules.md) or `state.apply`.

## Before you can run anything

`halite-hub run` authenticates with an **operator certificate** and is
authorized against the **policy**, which denies by default. Being logged in
to the hub host is not authority. Create yourself a certificate, and the
first time, a starting policy:

```sh
# on the hub
halite-hub keys operator create ed --admin
```

```
policy    /etc/halite/policy.yaml (administrator)
operator  ed
principal cert:CN=ed
cert      /etc/halite/pki/operator-ed.crt
key       /etc/halite/pki/operator-ed.key
expires   2026-11-10T18:24:35Z
```

`--admin` writes a policy granting that one operator everything, but only
if there is no policy yet. It is a bootstrap, not a destination: narrow it
before anyone else has access ([chapter 26](26-authorization.md)). The
certificate is good for 30 days by default (`--lifetime` changes that);
`halite-hub doctor` warns before it expires. Pass it with `--as ed`
(the name), or `--cert` and `--key` (the files):

```sh
halite-hub run '*' test.ping --as ed
```

The examples below leave off `--as ed` for readability.

> **Note:** a hub with no policy file starts, says so, and authorizes
> nothing. That is deliberate: security that depends on a file existing is
> not security. `halite-hub run` against it fails with `this hub has no
> policy, so nothing is authorized`.

## The shape of a command

```sh
halite-hub run web1.lab test.ping
halite-hub run '*' status.uptime
halite-hub run -G 'os:Ubuntu' pkg.version curl
halite-hub run -L web1,web2 service.restart nginx
```

```
web1.lab:
True
db1.lab:
    seconds: 59357
    since: 2026-10-11T02:23:56Z
    human: 16h29m0s
```

Results arrive node by node as each returns, in no particular order. The
default output is `nested`, which prints each node's ID and then its
answer; `--out` changes it.

### Arguments

An execution function has parameters, found with `sys.doc` or
[the reference](../modules.md). You supply them in three ways, and mix
them freely:

| Form | Example | Meaning |
|---|---|---|
| positional | `pkg.version curl` | Matched to parameters in order. |
| `key=value` | `pkg.install nginx version=1.24.0` | A named parameter. |
| JSON value | `cmd.run echo args='["a b","c"]'` | For lists and mappings. |

Every argument is a **string unless the function's signature says
otherwise**. A package version of `1.0` stays `1.0`; `no` stays `no`. This
removes a family of surprises from older tools that interpreted every
argument as YAML.

Quote for **the shell first**, and then for halite:

```sh
halite-hub run web1.lab cmd.run 'systemctl is-active nginx || true'
halite-hub run web1.lab cmd.run echo args='["a b","c"]' shell=false
```

```
web1.lab:
a b c
```

(The second form runs `echo` without a shell, with exactly two arguments,
`a b` and `c`. No quoting problem is possible, which is why it is the
hardened style.)

### Output formats

| `--out` | Gives | Use for |
|---|---|---|
| `summary` / `nested` (default) | `node:` then the value, indented | People |
| `json` | One JSON object keyed by node ID. **The schema is frozen and versioned.** | Scripts and other tools |
| `yaml` | The same as YAML | People who prefer it |
| `txt` | One line per node; nested values as JSON | Quick greps |
| `quiet` | Nothing; only the exit status | Conditions in scripts |

```sh
halite-hub run '*' test.ping --out json
```

```json
{
  "db1.lab": true,
  "web1.lab": true,
  "web2.lab": true
}
```

and `state.apply` with `--out json` returns, per node, the full per-state
structure of [chapter 4](04-idempotency.md), so a script can pick out
exactly the states that changed or failed.

### Exit codes

`halite-hub run` tells a script what kind of outcome it had without
parsing:

| Exit | Meaning |
|---|---|
| `0` | Every targeted node answered, and succeeded. |
| `1` | A node answered and failed; or no node matched; or the request was refused; or a safe limit stopped a batch. |
| `3` | A node was sent the job and **did not answer**. |
| `64` | The command line was not understood. |

"It said no" and "it said nothing" are different problems with different
remedies, so they are different codes. (Note that `halite-hub run
state.apply` exits `0` for a converged fleet as well as for one that
changed; the `0`/`2` distinction is `halite-node`'s. The hub's question is
only "did every node succeed?".)

A function that returns an error, such as a command that exits non-zero,
is reported as a failure from that node:

```
$ halite-hub run web1.lab cmd.run 'exit 3'
web1.lab:
command exited 3:
$ echo $?
1
```

## Running states from the hub

`state.apply` is the function that applies the highstate, exactly as
`halite-node state apply` does on the machine, but to many at once:

```sh
halite-hub run '*' state.apply --test           # rehearse everywhere
halite-hub run 'web*' state.apply               # apply to the web tier
halite-hub run web1.lab state.apply webserver   # only one SLS file
halite-hub run web1.lab state.sls playground --test
```

Each node returns the same per-state report as in the tour, then a
`Summary for <node>`. `--test` is the single most important flag: it turns
any state run into a drift report that touches nothing. Run it first on
anything you do not fully understand.

The read-only inspection functions work from the hub too:

```sh
halite-hub run web1.lab state.show_top
halite-hub run web1.lab state.show_states
halite-hub run web1.lab pillar.items     # needs an explicit permission
halite-hub run '*' grains.item osrelease
```

## An ad-hoc cookbook

Every one of these is an execution function, run in the lab used to
write this handbook:

| To | Run |
|---|---|
| Check who is up | `run '*' test.ping` |
| How long a machine has been up | `run '*' status.uptime` |
| Load and memory | `run 'web*' status.loadavg`, `status.meminfo` |
| Disk usage by filesystem | `run '*' disk.usage` |
| A package's version | `run '*' pkg.version nginx` |
| Packages with upgrades available | `run '*' pkg.list_upgrades` |
| What owns a file | `run web1 pkg.owner /usr/bin/curl` |
| Is a service running? | `run web1 service.status sshd` (`True`/`False`) |
| Restart one | `run web1 service.restart nginx` |
| Does a file exist, and what are its mode and owner? | `run web1 file.stats /etc/hostname` |
| Processes | `run web1 ps.pgrep nginx`, `ps.top` |
| Addresses | `run '*' network.ip_addrs` |
| Who has accounts | `run '*' user.list_users` |
| A kernel parameter | `run '*' sysctl.get net.ipv4.ip_forward` |
| Time and version | `run '*' test.version`, `status.time` |
| Anything else, last resort | `run web1 cmd.run 'uptime'` |

`cmd.run` is the answer to everything and should be your last resort: it
is the most dangerous function, a separate permission in the policy, and
the one a wildcard never grants ([chapter 26](26-authorization.md)). Every
module function is also documented from the hub: `halite-hub run web1
sys.doc pkg.install`.

## Changing a lot of machines safely

The first rule of running a change across a fleet is: **do not**. Run it
on one, then a few, then the rest, stopping if it goes wrong. halite builds
that into the command line, and *the hub owns the process*.

### Batches

```sh
halite-hub run '*' state.apply --batch 25% --batch-wait 30s
halite-hub run 'web*' state.apply --batch 10 --batch-safe-limit 3
halite-hub run '*' state.apply --batch 1 --progress
```

| Flag | Meaning |
|---|---|
| `--batch N` / `N%` | Run on at most N nodes (or that percentage) at a time; wait for those to return before the next. |
| `--batch-wait <dur>` | Pause between batches. |
| `--batch-safe-limit N` | Stop the whole run once N nodes have failed. |
| `--batch-timeout <dur>` | How long to wait for one batch's returns. |
| `--progress` | Show nodes dispatched, returned and outstanding as it goes. |

The batch belongs to the **hub**, not to your terminal. Close the laptop
or lose the SSH session and the batch carries on; `halite-hub jobs active`
says what is in flight and how far it has got, and if the hub itself
restarts, `halite-hub jobs resume <jid>` picks the batch up where it
stopped. (In the predecessor, `--batch` is a client-
side loop, and a dropped connection abandons half an estate with nothing
recording where it stopped.)

`--batch-safe-limit` is the one to always set. A change that fails on the
first node will usually fail on the next hundred, and you want to stop at
two. Here, with a command that fails everywhere:

```
$ halite-hub run '*' cmd.run 'exit 1' --batch 1 --batch-safe-limit 1
3 nodes, 1 at a time; the hub owns the batch, so this can be interrupted

the batch was stopped by its safe limit with 2 node(s) never reached
db1.lab:
command exited 1:
web1.lab: no answer
web2.lab: no answer
$ echo $?
1
```

and the job record names who was never reached:

```
$ halite-hub jobs show <jid>
batch     1 at a time, 1 delivered
to go     web1.lab, web2.lab
missing   web1.lab, web2.lab

db1.lab: FAILED (retcode 1, 1 ms)
```

### Canaries

`--subset N` runs on N nodes chosen **at random** from the target, using
`crypto/rand`, so a canary set cannot be predicted and arranged to miss.

```sh
halite-hub run 'web*' state.apply --subset 2 --test   # rehearse on two
halite-hub run 'web*' state.apply --subset 2          # change two
halite-hub run 'web*' state.apply --batch 25% --batch-safe-limit 2   # the rest
```

For a fixed canary (always the same machine), give it a nodegroup:
`-N canary`.

### The standard rollout

Putting it together, the shape of a careful change to a service tier:

```sh
# 1. Rehearse everywhere. Read the diffs.
halite-hub run 'web*' state.apply --test

# 2. Change one, chosen at random. Check it.
halite-hub run 'web*' state.apply --subset 1
halite-hub run 'web*' cmd.run 'curl -fsS localhost/healthz'   # or your check

# 3. Change the rest, a quarter at a time, stopping at two failures.
halite-hub run 'web*' state.apply --batch 25% --batch-wait 30s --batch-safe-limit 2
```

For a rollout that needs steps between batches (take it out of the load
balancer, check health, put it back), that is a job for
**orchestration** ([chapter 21](21-orchestration.md)).

## Asynchronous jobs

```sh
halite-hub run '*' status.uptime --async
```

```
20261011T185329068766
```

prints the **JID** and returns at once. The job runs; everything is
recorded as it returns. Read it later:

```sh
halite-hub jobs show 20261011T185329068766
halite-hub jobs missing 20261011T185329068766
```

Useful for long runs, for scripts that fire many jobs and collect later,
and for starting something from a terminal you may lose. `--timeout`
(default five minutes) bounds how long a foreground `run` waits to gather
returns.

## A node that is not there

A job is sent only to nodes **connected right now**. The `--offline` flag
decides what to do about the others:

| `--offline` | Behaviour |
|---|---|
| `skip` (default) | Report them as `not connected`; send them nothing. Exit `3`. |
| `queue` | Hold the job and deliver it when the node next connects, if it has not expired. |
| `require` | Refuse the whole job if any matched node is not connected. |

The three, run against a stopped node:

```
$ halite-hub run web1.lab test.ping
web1.lab: not connected
web1.lab: no answer                     (exit 3)

$ halite-hub run web1.lab test.ping --offline require
halite: /v1/jobs: 1 of 1 matched nodes are not connected ([web1.lab]) and the
offline policy is "require"             (exit 1)

$ halite-hub run web1.lab test.ping --offline queue --async
web1.lab: not connected
20261011T184928102545
```

and when the node came back, the bus recorded the queued job being
delivered and answered:

```
halite/node/web1.lab/start
halite/job/20261011T184928102545/queued/web1.lab
halite/job/20261011T184928102545/ret/web1.lab
```

**A queued job expires.** `--ttl` is the lifetime of a job: 15 minutes by
default, and **one hour** for a queued job, since it is waiting for a
machine that is off. A node that returns after the expiry does not run a
stale instruction, and the hub records that it did not — a node coming
back to find nothing happened, with no reason, is the failure that bound
exists to prevent. `halite-hub jobs missing <jid>` lists the nodes a job was sent to that have
not answered.

## Safety habits

- **Test first.** `--test` for states; for other functions, read
  `sys.doc` and try it on one node.
- **Read the target before the command.** `test.ping` with the same
  target lists the nodes that will be hit.
- **Prefer a state to a command.** If you find yourself running the same
  `cmd.run` twice, it belongs in the tree, where it is reviewed,
  idempotent and repaired automatically.
- **Always set a batch and a safe limit** on anything that changes a fleet.
- **Record why.** Jobs record who ran them and what; the commit that
  changed the tree records why.
- **Use the narrowest permission.** Most people should have roles that
  allow `state.apply` for named SLS files on named targets, and not
  `cmd.run`.
- For regulated environments, **sign jobs**: nodes can be configured to
  refuse any job that does not carry a signature from a key the hub does
  not hold ([chapter 28](28-hardening.md)).

## Key ideas

- `halite-hub run <target> <function> [args]`, authorized by **operator
  certificate + deny-by-default policy**.
- Arguments are strings; use `key=value` and JSON for structure; `--out
  json` for scripts.
- Exit codes: **0** all succeeded, **1** failed (or no match), **3** a
  node did not answer.
- `state.apply --test` across the fleet is a **drift report**.
- **Batches and safe limits live on the hub**: `--batch`,
  `--batch-safe-limit`, `--subset`, `--progress`.
- `--async` returns a JID; `--offline skip|queue|require` decides what to
  do about nodes that are off; a queued job expires.

Next: [jobs and the job cache](15-jobs.md).
