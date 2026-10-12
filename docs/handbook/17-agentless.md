# Agentless mode

<!-- abstract: Managing machines that run no halite agent, over SSH: the roster, what is pushed, what works, and what cannot. -->
<!-- kind: guide -->
<!-- index: agentless; halite-hub ssh; roster; thin directory; ssh_config; ProxyJump; ansible inventory; sshconfig roster; --clean; ssh-concurrency; bootstrap -->

Not every machine can, or should, run an agent: an appliance you may not
install software on, a machine you are about to retire, a fresh server you
will use `halite-node` to enrol, a network segment where you cannot open
a port to the hub. **Agentless mode** configures such a machine over SSH,
using a single static binary that is pushed, run once and (if you wish)
removed.

```sh
halite-hub ssh '*' test.ping
halite-hub ssh -G 'role:edge' state.apply --test
halite-hub ssh web1.example cmd.run 'uname -a'
```

The commands and functions are the ones you already know; only the way
they reach the machine differs.

## How it works

1. The hub reads a **roster**: the list of machines, how to reach them,
   and any grains to attach.
2. It connects with the system `ssh` binary. Your `ssh_config`,
   `ProxyJump`, `ProxyCommand`, certificate authentication, agent policy
   and `known_hosts` all apply, because halite uses the program you already
   trust to speak SSH rather than reimplementing it.
3. It pushes a static `halite-node` for the target's platform and checks
   its digest **after** the transfer. The binary is cached on the target
   under a directory named by its digest (`/var/tmp/halite-thin/<digest>`),
   so the next run skips the transfer.
4. It sends the job on standard input and reads the answer from standard
   output, in a framed form that separates it from any other output.
5. For `state.*` functions it compiles the state tree and pillar **on the
   hub** and sends them with the job. The target holds no tree and no other
   machine's secrets, and uses only what it was sent, never a local tree
   left behind by an older system.

The point of the design is the static binary. Systems that need an
interpreter on the target have to find or install a compatible one first,
which is where most of their agentless failures come from. There is nothing
to find here.

## The roster

The default roster is a YAML file at `<root>/roster` (or pass
`--roster-file`):

```yaml
web1.example:
  host: 10.0.0.4
  port: 2222
  user: deploy
  priv: /home/ed/.ssh/estate
  sudo: true
  identities_only: true
  proxy_jump: bastion.example
  grains:
    role: edge
db1.example: 10.0.0.9
lonely.example:
```

A bare string is the host. A name with nothing under it connects by its own
name. Fields: `host`, `port`, `user`, `priv` (key file), `sudo`,
`identities_only`, `proxy_jump`, `timeout`, `thin_dir`, and `grains` to
attach. A misspelt field is refused rather than ignored: `sudo_usr` would
otherwise run everything as root.

Other rosters, with `--roster`:

| `--roster` | Reads |
|---|---|
| `flat` (default) | the YAML file above |
| `sshconfig` | your `~/.ssh/config`, skipping wildcard patterns |
| `cache` | the nodes the hub has heard of, with the grains they reported |
| `ansible` | an Ansible inventory, in INI or YAML form, since many estates already have one |

The `scan`, `cloud` and `terraform` rosters are named as not built, rather
than reported as typos.

Targeting is the same grammar as the rest of the fleet, matched against the
roster's names and the grains it attached: `-G 'role:edge'` works exactly
as it does for an enrolled node.

## Running it

A lab session, with two roster names resolving to the host this handbook
was written on:

```
$ halite-hub ssh '*' test.ping --roster-file roster
local1.example: True
```

```
$ halite-hub ssh -G 'role:edge' grains.get role --roster-file roster
local1.example: edge
```

```
$ halite-hub ssh local1.example cmd.run 'hostname; id -un' --clean
local1.example: |
        system76-pc
        ed
```

Flags:

| Flag | Meaning |
|---|---|
| `--roster <backend>` | `flat`, `sshconfig`, `cache` or `ansible`. |
| `--roster-file <path>` | The roster. |
| `--thin <path>` | The `halite-node` binary to push. |
| `--clean` | Remove the cached binary before and after. |
| `--ssh-concurrency <n>` | How many targets at once (default 8). |

Applying a tree:

```
$ halite-hub ssh local1.example state.apply --test
local1.example:
    file_|-playground_dir_|-/tmp/halite-lab/local1.example_|-directory:
        name: /tmp/halite-lab/local1.example
        comment: The directory /tmp/halite-lab/local1.example would be created.
    …
```

All the state machinery applies: `--test`, requisites, templates, pillar.

### Pillar and grains for an agentless machine

The hub compiles pillar for an agentless target from **the grains the
roster attaches**, because there is no node to report any. A pillar file
that reads `grains['id']`, or a top file that matches on a grain, therefore
needs that grain in the roster entry. Forgetting is reported, not silently
wrong:

```
compiling pillar for local1.example: state compilation failed with 1 error(s):
```

and adding `id: local1.example` under `grains:` fixes it. Pillar targeting
(`-I`, `-J`) works against the same compiled pillar.

### A tip

Give every machine exactly one roster entry. Two entries that resolve to the
**same host** run concurrently and both try to install the cached binary,
and one fails:

```
local2.example: installing the binary: exit status 1: chmod: cannot access
'/var/tmp/halite-thin/….partial': No such file or directory
```

Re-running succeeds, and `--ssh-concurrency 1` avoids it, but the duplicate
is the thing to remove.

## What agentless mode cannot do

This is inherent to having no persistent connection, not a gap in the
build:

- **No beacons, no scheduler, no mine, no presence, no events from the
  machine.** Nothing on the machine can start anything. A reactor can act
  on an agentless target but cannot be triggered by one.
- **No drift repair between your runs.** You must run it. A cron job or
  CI schedule on the hub host running `halite-hub ssh '*' state.apply`
  gives a pull-like loop.
- **A tree larger than 4 MiB is refused**, rather than being sent on every
  run to every machine. (The reverse tunnel that would avoid this is not
  built.)

## A natural use: preparing a machine for enrolment

Agentless mode can put the agent on a machine: a state that places the
`halite-node` binary and a `node.yaml` naming the hub and its fingerprint
(`file.managed` with `mode: '0755'` and `makedirs: True`), pushed with
`halite-hub ssh`. Enrolment itself still needs the fingerprint comparison
and an operator's approval ([chapter 31](31-install-nodes.md)); after that
the node is connected and self-managing and its roster entry can go.

> **Not verified:** this handbook ran each piece (agentless state
> application, `file.managed` of a binary, enrolment) but not the whole
> bootstrap as one procedure on a fresh machine. Treat it as a pattern to
> test on a throwaway host, not a runbook.

## Key ideas

- `halite-hub ssh` runs the same commands over SSH, pushing one static
  binary that is digest-checked and cached.
- It uses the **system `ssh`**, so your SSH configuration applies.
- The **roster** (flat, `sshconfig`, `cache`, `ansible`) names machines and
  attaches grains; **pillar is compiled from those grains**.
- No agent means **no beacons, scheduler, mine or events** and no drift
  repair unless you schedule runs yourself.
- One roster entry per host.

Next, Part IV: automation. [The event bus](18-events.md).
