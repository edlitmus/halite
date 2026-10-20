# Extensions

<!-- abstract: How halite is extended without ever loading code into the agent: signed bundles run as separate processes, the kinds, the trust and pinning model, and the commands to build, check, sign and publish one. -->
<!-- kind: concept -->
<!-- index: extension; extensions; bundle; manifest; extension_trust_keys; extension_pins; extension_require_signature; extensions verify; extensions sign; extensions key create; extensions sync; extensions list; sys.list_extensions; saltutil.sync_all; signer extension; bridge; migrate --bridge-skeleton; pillar extension; returner extension; sandbox; declares; _ext -->

No halite program ever imports code at run time. There is no `_modules/`
directory of scripts that nodes fetch and execute in their own process. In
the systems halite descends from, that mechanism is a code-distribution
channel in which the agent, as root, runs arbitrary code from the file
server with no signature requirement, and it is also how third-party
formulas ship helper modules, so it is load-bearing.

halite's replacement keeps the usefulness and removes the danger. An
**extension** is:

- a **separate executable**, speaking a small JSON protocol over standard
  input and output;
- packaged as a **signed bundle** (the executable, a manifest of digests and
  a signature);
- delivered through the file server like anything else, but **fetched
  without being loaded**;
- **verified on every load** against keys you chose, and **pinned** by
  version and digest;
- run **out of process**, as an unprivileged user, with only the
  permissions its manifest declared.

Extensions can be written in any language, including Go (a small library,
package `ext`, is provided) and Python (a shipped example speaks the wire
with nothing but the standard library).

## What an extension can be

Each extension provides exactly one **kind**:

| Kind | Adds | Runs on |
|---|---|---|
| `module` | Execution functions | the node |
| `state` | State functions | the node |
| `grain` | Grains | the node |
| `beacon` | A beacon | the node |
| `returner` | A returner destination | the node |
| `pillar` | An external pillar source | the hub (that is where pillar compiles) |
| `runner` | A hub runner | the hub |
| `renderer` | A template or serialization stage | wherever rendering happens |
| `auth` | A login method | the API |
| `roster` | An agentless roster | the hub |
| `fileserver` | A file server backend | the hub |
| `signer` | A job signer (a KMS, a token) | the operator's side |

What ships with halite: `aws_secrets_manager` (a `pillar` extension),
`halite-ext-signer-local` and `halite-ext-signer-kms` (`signer` extensions),
and an example `pillar` extension written in Python. The sixteen returner
destinations that are not compiled in (`postgres`, `kafka`, `redis`, and so
on) are extensions of kind `returner`: `returner: postgres` finds the
`postgres` extension by name.

## What an extension may do

The manifest declares what an extension needs, and **nothing is granted that
is not declared**. The declaration is signed into the manifest, so an
extension cannot ask for more at handshake than somebody signed off on.

| Declaration | Grants |
|---|---|
| (none) | The default: an unprivileged account, no network. |
| `network` | Permission to dial out. Without it the host sets `HALITE_EXT_NETWORK=deny`. |
| `root` | Keep the extension's own identity instead of dropping to the unprivileged account. |

Declare the minimum. An extension that asks for root because it might one
day need it is an extension running as root today.

## The trust model

Three separate decisions, so that one stolen key or one clever publisher does
not defeat the others:

| Decision | Where it lives |
|---|---|
| **Whose signature do I accept?** | `extension_trust_keys` in `hub.yaml` and `node.yaml`: the public keys. |
| **Exactly which build may run?** | `extension_pins`: `{name: {version, root}}`. The `root` covers every file and every field of the manifest, including `declares` and `executables`. A version is a label the publisher controls; a digest is not, so **both halves of the pin matter**. |
| **What if there is no signature?** | `extension_require_signature` (on in production). `false` permits an *absent* signature for development and warns on every load; a signature that is present and **wrong** is refused whatever it says. |

and one rule of placement: **the signing key lives on a machine that is not
the hub.** A hub that holds both the key and the extensions it verifies is
verifying its own signatures; a compromise of the hub would then let an
attacker sign anything every node will run. The example tree signs on a
dedicated "signer" node and publishes into the git repository the hub
serves.

Fetching is not loading. `halite-hub extensions sync` (and
`saltutil.sync_all` on a node) copies bundles from `_ext/` into the cache;
what a running hub or node *runs* changes only when it is restarted. So
publishing an extension into the tree cannot change a running process's
behaviour, which is the difference that matters: in the older systems,
"sync" means "the agent will now execute new code".

## The lifecycle

The whole path, with the commands that were run to check each step:

**1. Write it.** In Go, against the `ext` package; or in anything, against the
protocol. `halite-hub extensions run <path> [<function>]` starts the file
where it lies — no bundle, no signature, no cache, no restart — for the write-
and-try loop. It verifies nothing about where the file came from and is not
how an extension reaches a node.

**2. Check it against the protocol.** This is where an extension written in
another language earns its keep:

```sh
halite-hub extensions verify contrib/extensions/python/example_pillar.py
```

```
halite-hub extensions verify — contrib/extensions/python/example_pillar.py

  pass  handshake/answers                              it answers hello with hello_ok
  pass  handshake/names-itself                         it names itself
  pass  handshake/declares-a-version                   it declares a version
  pass  handshake/announces-functions                  it announces at least one function
  pass  call/answers-with-one-result                   a call is answered with exactly one result
  pass  call/an-unknown-function-is-an-error           an unknown function is an error, not a crash
  pass  call/a-failed-call-leaves-it-usable            a failed call leaves it able to answer the next
  pass  lifecycle/shutdown-ends-it                     a shutdown frame ends the process
  pass  protocol/refuses-another-version               it refuses a protocol version it does not speak
  skip  protocol/refuses-another-kind                  it refuses a kind it does not provide
        no --kind was given, so there is no kind to ask for other than the right one
  pass  protocol/ignores-an-unknown-field              it ignores a field it does not recognise

  13 pass, 0 fail, 1 skip

  A skip is not a pass: each says what could not be established.
```

(Shortened.) `verify` drives a candidate through the protocol deliberately:
a good call, a call that cannot succeed, a version it should refuse, a
shutdown it should honour; and it says which rule failed and why the rule
exists.

**3. Make a signing key, once, on the signing machine:**

```sh
halite-hub extensions key create release
```

It writes `extension-release.key` (readable by its owner alone, refusing to
replace one) and prints the `extension_trust_keys` line.

**4. Sign a bundle:**

```sh
halite-hub extensions sign ./build --name aws_secrets_manager \
  --ext-version 1.1.1 --kind pillar --exe aws-secrets --declares network \
  --key release --publish /srv/halite/states
```

This writes `manifest.json` (a digest of every file) and `manifest.sig` (a
signature over a root that covers those files and every field of the
manifest), and prints the `extension_pins` entry. `--publish` copies the bundle to
`_ext/<name>/<version>/` in the tree and refuses a version that already
exists. Read the manifest's `declares` before you ship: it is what the
sandbox is built from.

**5. Tell the hub and nodes what to trust**, in configuration:

```yaml
extension_trust_keys:
  - 'release rK7XI/MZuaShviflQHZDgrzWtKPQDLACL8gILM4+6u4='
extension_require_signature: true
extension_pins:
  aws_secrets_manager:
    version: 1.1.1
    root: 9f2c…
```

**6. Fetch and look:**

```sh
halite-hub extensions sync        # copy from _ext/ into the cache; load nothing
halite-hub extensions list        # what is installed, what it provides
halite-node call sys.list_extensions --local
```

then restart to pick it up. A node syncs its bundle with
`halite-hub run '*' saltutil.sync_all` (or the per-kind
`sync_modules`, `sync_states`, `sync_grains`, `sync_beacons`,
`sync_returners`, `sync_renderers`).

## At run time

An extension is a process pool, so it never has to be thread-safe. The host
sends `hello` and `call` frames; the extension answers `hello_ok` and
`result`, and may send `log`, `progress` and `event` frames meanwhile. A hung
extension is killed (SIGTERM, then SIGKILL, then a fresh process), so it
cannot hang the agent. A protocol violation kills the process rather than
failing the call: an extension that sent something unreadable has lost its
place in the stream. The child gets a clean environment, and writing stray
text to standard output **will break it**, because standard output is the
protocol; log to standard error or with the `log` frame.

The protocol is a published interface with a stated compatibility rule, which
extension authors need to know:

- The host offers **one** version; speak it or refuse. There is no range.
- **Ignore a field you do not recognise.** This costs nothing and breaks
  everybody if you do not, since it is what makes a field addable at all.
- **Refuse a frame kind you do not recognise.** Deliberately not
  symmetric: a receiver that skipped an unknown kind would also skip a
  misspelt one.

The step-by-step walkthrough, with the Go package and a full example, is
[Writing an extension](../extensions.md), included in this handbook.

## Porting a custom module

A tree that carries Python modules in `_modules/` or `_states/` cannot use
them as they are; there is no way around that. The migration tool bounds the
work:

```sh
halite-hub migrate /srv/salt --bridge-skeleton ./bridges
```

writes one Go command per Python module, with function names, parameters and
defaults filled in from the source (`__virtualname__` honoured; private
functions and `__virtual__` skipped). **Every generated function returns an
error until it is written**, so a bridge that was generated and forgotten
fails loudly rather than answering nothing and looking as if it worked, and a
file that already exists is never overwritten. `_utils/` and other import
targets get no skeleton, since they are not extension points.

## When to write one

Reach for an extension when the thing is genuinely new, such as a secrets
source, a destination for results, a vendor API or a hardware token. For
anything you can do by running a program on the node, a guarded `cmd.run`
([chapter 4](04-idempotency.md)) is simpler; and for configuration, the
existing modules almost certainly cover it
([chapter 11](11-modules.md)).

## Key ideas

- No code is ever loaded into the agent. An **extension** is a separate,
  **signed**, **pinned**, **sandboxed** program; the kinds include pillar
  sources, returners, modules, grains, signers.
- Trust is three decisions: **which keys** (`extension_trust_keys`),
  **which exact build** (`extension_pins`, version *and* root), **whether
  unsigned is allowed** (`extension_require_signature`).
- **Sign off the hub.** Syncing **fetches; it does not load**: restart to
  run.
- `extensions verify` checks any implementation against the protocol;
  `extensions sign`/`key create`/`sync`/`list` run the pipeline.
- `migrate --bridge-skeleton` turns custom Python modules into Go
  skeletons that fail loudly until finished.

Next, Part VI: the runbooks. [Installing the hub](30-install-hub.md).
