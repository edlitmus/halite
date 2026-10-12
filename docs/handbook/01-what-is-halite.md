# What halite is

<!-- abstract: The problem halite solves, what you get from it, what it is not, and how to read this handbook. -->
<!-- kind: concept -->
<!-- index: purpose of halite; features; benefits; configuration management; remote execution; fleet; node; hub; limits of halite -->

halite is a system for looking after a lot of computers as though they
were one.

You describe, in plain text files, how each kind of machine should be:
which packages are installed, which files exist and what is in them,
which services run, which users and firewall rules are present. halite
reads the description, looks at the machine, works out what is
different, and changes only that. You can also reach out to any set of
machines and run a command, collect an answer, or react automatically
when something happens on one of them.

Those two jobs have names you will meet constantly:

- **Configuration management** is keeping machines in a *described
  state*, continuously, instead of configuring each by hand once and
  hoping it stays that way.
- **Remote execution** is running a command or a function on many
  machines at once and getting the results back in one place.

Everything else in halite — events, beacons, reactors, orchestration, the
scheduler — is built from those two, and exists to make them safe,
automatic and observable at scale.

This handbook assumes you know nothing about halite, and nothing about
the tools it descends from. It does assume you can use a shell, edit a
text file, and have administered at least one Unix-like machine. If you
read it from the front, you will come out knowing every feature and how
to use it. If you are in a hurry, [chapter 3](03-guided-tour.md) has you
changing a real machine in ten minutes, and
[Reading this handbook](#how-to-read-this-handbook) at the end of this
chapter shows shorter routes.

## The problem

Configure one server by hand and it is a pleasant craft. Configure thirty
and three things begin to go wrong.

**Machines drift.** Somebody installs a package to debug an incident and
never removes it. Somebody edits a configuration file in place on a
Friday. Six months later no two machines that were meant to be identical
are, and nobody can say how they differ. The differences are found by the
incident they cause.

**Knowledge lives in heads.** The procedure for building a web server is
a mixture of a wiki page that stopped being true two years ago, shell
history, and one colleague. When that colleague leaves, or when the
server has to be rebuilt at three in the morning, the procedure is
reconstructed from fragments.

**Change is unreviewable.** A change made by typing into a terminal has
no author, no review, no diff and no undo. A change made by editing a
text file in version control has all four.

**Scale breaks the manual loop.** Patching a vulnerability on three
machines is an afternoon. On three hundred it is a campaign, with
ordering constraints (drain the load balancer first), safety
constraints (stop if two fail), and a requirement that you can say
afterwards exactly what was done where.

halite's answer to all four is the same idea, and it is the most
important idea in this handbook:

> **You declare the end state. halite makes it so, and makes it so again,
> and does nothing when it is already so.**

That last clause is called *idempotency*, and it is why you can run
halite against a whole fleet every hour without fear. It gets its own
chapter ([chapter 4](04-idempotency.md)) because everything else in
the system rests on it.

## What you get

| You want to... | halite gives you | Chapter |
|---|---|---|
| Describe what a machine should look like | **States**: YAML files naming packages, files, services, users, firewall rules and several hundred other things | [5](05-states.md) |
| Control the order things happen in | **Requisites**: "install the package, *then* write the config, *then* restart the service if the config changed" | [6](06-requisites.md) |
| Reuse one description for many machines | **Templates**: Jinja inside state files, so one file renders differently on Debian and FreeBSD | [7](07-templates.md) |
| Know facts about a machine | **Grains**: automatically collected facts such as OS, kernel, CPU count, addresses | [8](08-grains.md) |
| Give machines different data, and keep secrets | **Pillar**: per-machine data compiled on a central server, delivered only to the machine that should have it | [9](09-pillar.md) |
| Say which machines get which description | **The top file** and **targeting** | [10](10-top-and-files.md), [13](13-targeting.md) |
| Run a command everywhere | **Remote execution**: `halite-hub run 'web*' service.restart nginx` | [14](14-remote-execution.md) |
| Roll a change out safely | **Batching**, test mode, canaries, safe limits | [14](14-remote-execution.md), [16](16-fleet-operations.md) |
| See what happened | **The job cache**, **the event bus**, metrics, an evidence chain on every node | [15](15-jobs.md), [18](18-events.md) |
| Notice that something changed on a machine | **Beacons**: watchers for files, disks, load, services, certificates | [19](19-beacons.md) |
| React automatically | **Reactors**: "when this event happens, run that" | [20](20-reactors.md) |
| Coordinate many machines in order | **Orchestration**: multi-step, multi-machine procedures with rollback | [21](21-orchestration.md) |
| Do things on a timetable | **The scheduler** | [23](23-scheduler-and-returners.md) |
| Manage machines with no agent installed | **Agentless mode** over SSH | [17](17-agentless.md) |
| Control who may do what | **A deny-by-default policy** with per-target, per-function rules | [26](26-authorization.md) |
| Drive it from other software | **An HTTP API** | [27](27-http-api.md) |
| Extend it | **Signed extensions**: separate programs, never code loaded into the agent | [29](29-extensions.md) |

## What makes it different

halite keeps the model that made its predecessor useful — states,
pillar, grains, events, reactors, orchestration — and changes how it is
built and how much it trusts.

**Three static programs and nothing else.** There is one program for the
managed machine, one for the central server, one for the HTTP API. Each
is a single file with no interpreter, no libraries to install and no
packages to resolve. Copy it onto a machine and it runs. The whole build
uses exactly one third-party library, and a test fails the build if that
ever changes.

**Machines call in; the hub never calls out.** Each managed machine opens
one connection to the central server and holds it open. The server
listens on exactly one port. Nothing on a managed machine listens for
anyone. A machine behind NAT or a firewall that blocks inbound traffic
needs no special arrangement.

**Everything is mutually authenticated.** Every connection is TLS 1.3
with certificates on both ends. A node's certificate is issued by an
enrollment step that an operator approves, lasts 90 days, and renews
itself. There is no shared secret to leak.

**Nothing is permitted by default.** Operators can do only what a
policy file grants, to the machines it names, with the arguments it
allows. Running arbitrary commands is a separate permission that no
wildcard ever grants.

**A test run you can trust.** Every state function is held to a shared
conformance test: in test mode it must change nothing, and it must say
what it *would* have changed. "Look before you leap" is a feature you
can rely on, not a hope.

**Mistakes fail loudly.** A template that mentions a value that does not
exist is an error with a file and line number, not a silently empty
string. A file mode written as `0644` instead of `'0644'` is refused with
an explanation. A requisite loop prints the cycle. Duplicate keys in a
YAML file are an error.

**The record is the machine's own.** Each node keeps a hash-chained log of
every job it ran. The central server holds anchors of that chain. A
compromise of one cannot silently rewrite history held by the other.

## The three programs

| Program | Runs on | What it is |
|---|---|---|
| `halite-node` | Every managed machine | The agent. It holds a connection to the hub, runs jobs, applies states, collects grains, runs beacons and the scheduler. Also the local tool: `halite-node state apply` configures the machine it is on, with or without a hub. |
| `halite-hub` | The central server (one, or a few) | The control plane. It issues certificates, serves files, compiles pillar, authorizes and dispatches jobs, keeps the job cache and the event bus, runs reactors and orchestration. Also the operator's command line: `halite-hub run`, `halite-hub keys`, `halite-hub orch`. |
| `halite-api` | Beside the hub, optionally | The HTTP interface for dashboards, CI systems and scripts. It holds no state of its own and is only a client of the hub, with its own identity and the same policy applied. |

A node never needs `halite-hub`. A hub never needs `halite-node`.
Neither needs `halite-api`. You can run **only a node**, against a tree of
files on its own disk, and get configuration management with no server at
all. That is how [chapter 3](03-guided-tour.md) starts.

## How the pieces fit

```
    you ──────────┐
    (CLI)         │
                  ▼
 CI, dashboards  ┌───────────┐  mutual TLS, HTTP/2, port 4510
 ──────────────▶ │ halite-api│───────────────┐
    (HTTP)       └───────────┘               ▼
                                       ┌───────────┐         ┌───────────────┐
                                       │ halite-hub│◀────────│  halite-node  │ web1
                                       │           │◀────────│  halite-node  │ web2
   your tree of                        │ files     │◀────────│  halite-node  │ db1
   states & pillar ───────────────────▶│ pillar    │   every node dials in;
   (git, or a directory)               │ jobs      │   the hub never dials out
                                       │ events    │
                                       │ reactors  │
                                       └───────────┘
```

The central idea is a loop. You change the tree (usually a commit). The
hub serves it. Each node fetches what applies to it, compares it with
reality, and converges. Events — a node connected, a job finished, a file
changed, a disk filled — flow back to the hub, where reactors can start
new work.

## What halite is not

Honesty about the edges saves time.

- **It is not a container scheduler.** It will happily install and
  configure Docker, and manage the files and services around your
  containers. It does not decide where containers run.
- **It is not a provisioning tool.** It configures machines that exist.
  Creating the machines in a cloud account is the job of tools built for
  that; halite takes over once they boot.
- **It is not a monitoring system.** It exposes metrics for Prometheus and
  has beacons that notice conditions, but it does not store time series
  or draw dashboards (it ships a Grafana dashboard to start from).
- **It runs no Python.** There is no Python renderer, no Python module
  loader, and no `_modules/` directory of code that nodes import. The
  extension model is separate signed programs
  ([chapter 29](29-extensions.md)).
- **It does not manage every platform equally.** FreeBSD and Linux carry
  the most testing. macOS is covered. Windows is partial: four of the
  eighteen planned Windows modules ship. The evidence level for each
  module is public — see [the evidence report](../evidence.md) — and
  `halite-node call sys.evidence` prints it on the machine in front of
  you.
- **It does not implement everything its predecessor did.** The
  [divergence ledger](../DIVERGENCE.md) is the complete, dated list of
  what is deliberately different and what is not built. Where this
  handbook describes a feature that is partial, it says so in the text
  beside it.

> **Not built:** where a chapter in this handbook says *not built*, a
> test ran the real command and it refused. The wording comes from what
> the binary says, not from what the specification intended.

## Which platforms

halite organises platforms in tiers. The tier is a statement about what
is tested, not about popularity.

| Tier | Platforms | Meaning |
|---|---|---|
| 1 | Ubuntu, Debian, RHEL-family, Amazon Linux, FreeBSD 14 and 15, Windows Server | Full test legs and functional tests. FreeBSD is tier 1 and the project is developed on it. |
| 2 | SUSE, Alpine, macOS on Apple silicon | Built and unit-tested; functional tests on a subset. |
| 3 | OpenBSD, NetBSD, illumos, other CPU architectures | Compiles and is published as binaries. |

The hub and the API run on Linux and FreeBSD. Nodes run on everything
listed.

Within a platform, modules differ in how much anyone has actually checked
them against the real tool. halite records this per module as one of
three **evidence levels**, and it is worth learning the words now because
they appear throughout:

| Level | Meaning |
|---|---|
| `assumed` | Tests were written from documentation. Nothing has watched the module change a real machine. |
| `captured` | Tests hold output captured from the real tool, so parsing is checked. |
| `hardware` | The module's changing path has been run against the real tool on a real system, and a note says which and when. |

A release is not made while a module that changes the system as root is
still `assumed`.

## How to read this handbook

The handbook is in nine parts. You do not have to read them in order, but
each part assumes the ones before it unless it says otherwise.

| Part | You will learn | Read it if you are |
|---|---|---|
| **I. Orientation** | What halite is, the vocabulary, and a hands-on tour that needs no server | Everyone, first |
| **II. Describing machines** | Idempotency, states, requisites, templates, grains, pillar, the file tree, the module library | Writing configuration |
| **III. Acting on the fleet** | The hub, targeting, remote execution, jobs, rolling changes, agentless mode | Operating a fleet |
| **IV. Automation** | Events, beacons, reactors, orchestration, the mine, the scheduler, whole scenarios | Making the fleet look after itself |
| **V. Security and identity** | Trust, authorization, the API, signing, extensions | Deploying responsibly |
| **VI. Runbooks** | Installing, configuring, monitoring, backing up, upgrading, troubleshooting, responding to incidents | On call |
| **VII. Reference** | Commands, configuration, modules, events, exit codes, glossary | Looking something up |
| **VIII. The code** | How the source is organised, how a job and a highstate travel through it, every package | Contributing or auditing |
| **Appendices** | Migrating from Salt, the specification, the ledger of known gaps | Migrating or checking a claim |

Three suggested routes:

- **"I want to see it work."** Chapter 2 (ten minutes), chapter 3, then
  chapter 4.
- **"I am going to run this for a team."** Parts I and II, then III, then
  the runbooks in VI. Read V before you give anyone else access.
- **"I know the older tool this descends from."** Chapter 2's
  [glossary](98-glossary.md), then the [Salt dictionary](../migrating-from-salt.md) and
  the [migration walkthrough](../from-salt.md).

### Conventions

Commands you type are in blocks starting with `$` when a prompt matters
and bare when they are only commands. Output is real: the examples in
this handbook were run, and where an output is shortened the text says
so. A block beginning `# on the hub` runs on the central server, `# on
the node` on a managed machine, and `# on your workstation` anywhere an
operator works.

Paths that differ by platform are written for Linux and FreeBSD
together; the table in the [configuration runbook](33-configuration.md)
lists both. In short: configuration is in `/etc/halite` on Linux and
`/usr/local/etc/halite` on FreeBSD.

Commands that change a machine say so before they run. When there is a
safer way to look first — there nearly always is — it comes first.

### Keeping the handbook honest

This handbook is part of every release and is held to the code by tests:

- Every code block labelled with a file in `contrib/examples/` is
  compared byte for byte with that file, and the files are compiled,
  linted, or applied by tests.
- The module reference, the configuration reference and the evidence
  report are generated from the build and fail the test suite when they
  disagree with it.
- Every command, setting, event, beacon and runner the build ships must
  be mentioned here, or the build fails.
- The index is built from the same registries, so a function added
  tomorrow is findable the day a chapter names it.

If you find a statement that is wrong, that is a defect: the project's
first rule is that a claim nobody demonstrated is a bug, whoever wrote it.

## Key ideas

- halite keeps machines in a **declared state** and runs **commands**
  across them; everything else is built from those two.
- **Idempotency** — doing nothing when already correct — is what makes
  it safe to run continuously.
- **Three programs**: `halite-node` on machines, `halite-hub` in the
  middle, `halite-api` for software. Nodes call in; the hub never calls
  out.
- **Trust is explicit**: mutual TLS, approved enrollment, deny-by-default
  authorization, signed everything that runs.
- The **evidence level** of a module tells you how much anyone has
  actually checked it.

Next: [the concepts in one sitting](02-concepts.md).
