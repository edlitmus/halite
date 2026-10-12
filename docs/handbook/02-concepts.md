# The concepts in one sitting

<!-- abstract: The vocabulary and the mental model: nodes, the hub, states, grains, pillar, jobs, events, and how a change travels. -->
<!-- kind: concept -->
<!-- index: glossary; node; hub; api; state; SLS; highstate; top file; grains; pillar; job; jid; return; event; beacon; reactor; orchestration; runner; mine; returner; environment; target; policy; enrollment; extension -->

halite has about two dozen words that carry the whole system. Learn them
once, here, and every later chapter reads easily. Each is defined briefly
now and taken apart later; the chapter that does so is linked.

The words fall into four groups: the **machines**, the **description** of
what they should be, the **commands** you send, and the **automation**
that links the two. Plain definitions of every term are collected in
the [glossary](98-glossary.md).

## The machines

**Node.** A machine halite manages. It runs `halite-node`. Every node has
an **ID**, a name that is unique in the fleet and, by default, its fully
qualified domain name (`web1.example.com`). Targets, certificates, jobs
and logs all refer to nodes by ID.

**Hub.** The central server, running `halite-hub`. It holds the files and
data every node needs, decides who may do what, sends jobs to nodes, and
keeps the records. A small fleet has one hub. Larger ones add **relays**,
hubs that sit between a group of nodes and the main hub
([chapter 38](38-scaling.md)).

**API.** `halite-api` is a separate program that lets software — a CI
pipeline, a dashboard, a chat bot — do what an operator does with the
command line. It authenticates callers itself and holds no state
([chapter 27](27-http-api.md)).

**Operator.** A person (or a piece of automation) who gives the fleet
instructions. Operators prove who they are with a certificate, a login, or
a token, and what they may then do is decided by the **policy**
([chapter 26](26-authorization.md)).

**Fleet** (or *estate*): all the nodes one hub manages.

**Enrollment.** The one-time act of introducing a node to a hub. The node
creates a private key that never leaves it, asks the hub for a certificate,
and an operator approves. After that the node renews its own certificate
without help ([chapter 25](25-trust-and-identity.md)).

```
   operator ───▶  hub  ◀─── node dials in, holds the connection
                   │
                   └── the hub never opens a connection to a node
```

## The description

This is the half most people spend most of their time in.

**State.** A single statement of how one thing should be: "the package
`nginx` is installed", "the file `/etc/motd` has this content and mode
`0644`", "the service `sshd` is running and enabled at boot". A state is
written in YAML, names a **module** and a **function** (`file.managed`,
`pkg.installed`, `service.running`), and gives that function its arguments.

**SLS** (the file extension `.sls`). A file holding a set of states, plus,
optionally, templating and instructions for combining files. "The `nginx`
SLS" means the states that deal with nginx. SLS names use dots for
directories: `webserver.nginx` is `webserver/nginx.sls`, or
`webserver/nginx/init.sls`.

**Module and function.** halite ships a library of modules, each with
functions. `file` is a module; `file.managed` is one of its state
functions. The same module name appears in two places, because a thing
can be *described* (a state: "make this so") or *asked* (an execution
function: "do this now"). Chapter [11](11-modules.md) explains the split.

**Highstate.** The result of applying *everything that applies to this
node*. "Run a highstate" means "bring this machine fully into line with
its description". It is the normal, everyday operation.

**Top file** (`top.sls`). The index: it says which SLS files apply to
which nodes. "Every machine gets `common`; machines whose ID starts
`web` also get `webserver`."

**Requisite.** An ordering or dependency rule between states: *require*,
*watch*, *onchanges*, *onfail*, *prereq*. Without them states run in the
order they are written; with them you can say "restart the service only
if the config file changed" ([chapter 6](06-requisites.md)).

**Template.** Jinja syntax (`{{ ... }}` and `{% ... %}`) inside SLS files
and the files they deliver, so that one description can produce many
results ([chapter 7](07-templates.md)).

**Grains.** Facts a node discovers about itself: its operating system, kernel,
number of CPUs, memory, network addresses, virtualization type, and
anything you add. Grains are *read-only truth about the machine* and are
used in templates and in targeting ([chapter 8](08-grains.md)).

**Pillar.** Data you provide for nodes: ports, user lists, credentials,
feature flags. It is compiled on the hub *per node*, so a node receives
only its own values — which is why secrets belong here and not in state
files ([chapter 9](09-pillar.md)).

The difference between the last two is the one to remember:

| | Grains | Pillar |
|---|---|---|
| Who writes it | The machine, by looking at itself | You, in files on the hub |
| Describes | What *is* | What you *want*, or what the machine *needs to know* |
| Where it lives | On the node | Compiled on the hub, sent to one node |
| Trust | Whatever the node claims | Decided by the hub |
| Typical use | "Is this Debian or FreeBSD?" | "What port should nginx listen on? What is the database password?" |

**Environment.** A named copy of the tree: `base` by default, and perhaps
`prod` and `staging`. Environments let different machines read
different versions of the same files.

**File server.** The part of the hub that serves the tree, and any other
file nodes need, over the same authenticated connection. The tree itself
usually lives in git.

**Tree.** Informally, the directories of states and pillar together: the
thing you keep in version control and treat as the source of truth.

## The commands

**Execution function.** A function you ask a node to run *now*, with a
result you read: `test.ping`, `cmd.run`, `service.restart`, `pkg.version`,
`status.uptime`. The command line is
`halite-hub run <target> <function> [arguments]`.

**Target.** The set of nodes a command is for. Written as a **glob**
(`web*`), a list, a regular expression, a grain (`G@os:FreeBSD`), a pillar
value, a network, a named group, or a **compound** expression combining
these with `and`, `or`, `not` ([chapter 13](13-targeting.md)).

**Job.** One command sent to a set of nodes. Every job gets an identifier,
the **JID**, such as `20261011T182508812680`, which is also a timestamp.
Everything about the job — who asked, which nodes were targeted, who
answered — is recorded.

**Return.** One node's answer to a job: success or failure, the
return code, and the data. The hub collects the returns and shows them to
you.

**Job cache.** The hub's record of every job and every return, kept for a
configurable period ([chapter 15](15-jobs.md)).

**Batch.** Running a job on part of the target at a time ("10% at a
time, stop if two fail") ([chapter 14](14-remote-execution.md)).

**Test mode** (`--test`). Any state run can be made a rehearsal: nothing is
changed, and each state reports what it *would* have changed
([chapter 4](04-idempotency.md)).

## The automation

**Event.** A small message with a **tag** (`halite/node/web1/start`) and a
body of data, put on the hub's event bus whenever something happens: a
node connects, a job starts or ends, a key is accepted, a beacon fires.
You can read the stream live, replay it from a point in the past, or react
to it ([chapter 18](18-events.md)).

**Beacon.** A small watcher that runs *on a node* and fires an event when
something changes: a file is modified, a disk crosses 85%, a service
stops, a certificate nears expiry ([chapter 19](19-beacons.md)).

**Reactor.** A rule on the hub: "when an event matching this pattern
arrives, run this" — a command, a state, a runner. Beacons are the
senses; reactors are the reflexes ([chapter 20](20-reactors.md)).

**Orchestration.** A multi-step procedure that runs on the hub and
coordinates *across* nodes: drain the load balancer, deploy to the web
tier ten percent at a time, verify, and roll back if verification fails
([chapter 21](21-orchestration.md)).

**Runner.** A function that runs on the hub itself rather than on a node:
`jobs.list_jobs`, `manage.up`, `event.send`. Orchestration and reactors
use them heavily ([chapter 22](22-runners-and-mine.md)).

**Mine.** A small shared notice board. Nodes publish facts to it (their
addresses, say) and other nodes read them, so that a load balancer's
configuration can list every web server without anyone typing the list.

**Scheduler.** Timed work *on a node*: "apply the highstate at 03:17 with
up to fifteen minutes of random delay"
([chapter 23](23-scheduler-and-returners.md)).

**Returner.** A destination for results: a local file, a webhook, syslog,
a mail relay.

**Extension.** A separate, signed program that adds a module, a pillar
source, a grain provider or a returner. It runs in its own process;
nothing is ever loaded into the agent ([chapter 29](29-extensions.md)).

## How a change travels

Here is the full path of the most common thing you will do: change a
configuration file on every web server.

```
 1. You edit  states/webserver/nginx.conf.jinja   in your tree, and commit.
 2. The hub serves the new tree (it reads git, or a directory).
 3. A node's schedule says "apply the highstate"  — or you run it:
        halite-hub run 'web*' state.apply
 4. The hub checks the policy: may you do this, to these nodes?
 5. The hub resolves 'web*' to the connected nodes that match, assigns a JID,
    records the job, and writes it to each node's open connection.
 6. Each node checks the job is fresh and unique, fetches its pillar
    (compiled for it alone) and the files it needs, and compiles its states.
 7. For each state the node asks the module: is the machine already like this?
        yes → do nothing           ("already in the requested state")
        no  → change it, record what changed
 8. If nginx.conf changed, a `watch` requisite restarts nginx.
 9. The node returns the result.  The hub records it, fires
    halite/job/<jid>/ret/<node>, and shows you the summary.
10. A reactor listening for that event can start the next thing.
```

Steps 6–7 happen on the node, not the hub, so the hub does not become a
bottleneck compiling configuration for thousands of machines. If the hub
cannot be reached, a node falls back to its own copy of the *state* tree;
it does not fall back for pillar by default, because applying states
against missing secrets would report a convergence that never happened
([chapter 12](12-hub-and-nodes.md)).

## Names you will see in files and commands

| Name | What it is |
|---|---|
| `node.yaml`, `hub.yaml`, `api.yaml` | The configuration file of each program. |
| `policy.yaml` | The authorization policy. |
| `top.sls` | The index of states (or pillar) to nodes. |
| `*.sls` | A set of states, or pillar data. |
| `file_roots`, `pillar_roots` | The directories the file server and pillar compiler read. |
| `pki_dir` | Where certificates and keys live. |
| `state_dir` | Durable state: keys on the hub, the evidence chain on a node. |
| `cache_dir` | Disposable cache. |
| `halite://`, `salt://` | URIs for files in the served tree. Both mean the same thing. |
| `halite/...` | The root of every event tag. |

## Key ideas

- The **machines**: nodes (agents), a hub (the server), an API
  (optional), operators (people and automation).
- The **description**: states, grouped in SLS files, selected by the top
  file, parameterised by grains and pillar, ordered by requisites.
- The **commands**: execution functions, aimed at targets, recorded as
  jobs whose returns come back to the hub.
- The **automation**: events, beacons, reactors, orchestration, runners,
  the scheduler.
- **Grains are what a machine knows about itself; pillar is what you tell
  it.**

Next: [a guided tour](03-guided-tour.md), where you use all of the
description half on a real machine in ten minutes.
