# Writing an extension

Salt's extensibility is a Python file dropped in `_modules/`, `_grains/`
or `_pillar/` on the file server, which the agent imports in process, as
root, with no signature requirement. It is load-bearing — most formulas
carry one — and it is a code distribution channel with no controls on
it.

This is the replacement. An extension is a separate executable speaking
a JSON protocol over stdio, packaged as a signed bundle, delivered
through the same file server, verified on every load, pinned by digest,
and run out of process in a sandbox.

There is a complete one in this repository:
[`cmd/halite-ext-aws-secrets`](../cmd/halite-ext-aws-secrets). It is the
AWS Secrets Manager external pillar, the same job
`_pillar/aws_secrets_manager.py` does in a Salt tree, and this page
walks through it end to end. Read the source alongside this; it is about
four hundred lines and most of them are the AWS part rather than the
extension part.

## The shape

```go
func main() {
	ext := &bridge.Extension{
		Name:      "aws_secrets_manager",
		Version:   "1.0.0",
		Kind:      "pillar",
		Declares:  []string{"network"},
		Functions: functions(),
		Handler:   handle,
	}
	if err := ext.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "aws_secrets_manager:", err)
		os.Exit(1)
	}
}
```

That is the whole of it. `Serve` does the handshake, reads calls, and
writes results.

**`Kind`** is what it provides: `module`, `state`, `grain`, `beacon`,
`returner`, `pillar`, `runner`, `renderer`, `auth`, `roster`,
`fileserver`, or `signer`. The host names the kind it wants in the
handshake, and an extension asked for one it does not provide refuses
rather than pretending. A `pillar` extension runs on the hub, because
that is where pillar compiles; `module` and `returner` run on nodes.

**`Declares`** is what it needs beyond a bare process: `root`, or
`network`. Anything not declared is not granted — the sandbox denies the
network by default — and the declaration is signed, so an extension
cannot ask for more at handshake than its manifest says. Declare the
minimum: this one reads a secret over HTTPS and needs no privilege at
all, so it asks for the network and drops to an unprivileged account.

**`Functions`** is the machine-readable signature of SPEC 15.6, which
the host reads at handshake and `sys.list_extensions` reports. A pillar
extension provides exactly one, `ext_pillar` — Salt's name, because a
person porting one is reading Salt's documentation.

**`Handler`** runs one call. It gets the function name, the arguments,
and `Log`, `Progress` and `Event` for the streaming frames.

## Writing to stdout will break it

Stdout is the protocol. A stray `fmt.Println` in a handler is a frame
the host cannot read, and the host kills a process that violates the
protocol rather than failing the call — it has lost its place in the
stream. Everything for a person goes to stderr, or through `call.Log`,
which the host records against the extension's name.

## What a pillar extension is asked

One object per compilation:

```json
{
  "node_id": "web1.prod",
  "env": "base",
  "grains":  { "os": "Ubuntu", "region": "us-east-1" },
  "pillar":  { "java_home": "/usr/lib/jdk17" },
  "config":  { "region": "us-east-1", "secrets": [ ... ] }
}
```

`grains` and `pillar` are what Salt handed
`ext_pillar(minion_id, pillar, *args)`: the node's own grains, and the <!-- lexicon:allow -->
pillar the tree has produced so far. Both are always objects, never
null.

`config` is the block written under this source's name in `ext_pillar`,
handed over untouched. **The hub does not read it.** It belongs to the
extension, and a host that validated it would be a second place to keep
the schema — so there are no `aws_secrets_*` settings in `hub.yaml`.
Validate it yourself, and refuse a key you do not know: a misspelt
setting that silently does nothing is the failure this project's
configuration handling exists to prevent, and arriving over a pipe does
not exempt it.

Answer with a mapping, which is merged into pillar. Answer `null` to
contribute nothing. Return an error and the hub fails that node's
compilation — which is the behaviour to want, and the one place this
deliberately differs from the Python module it replaces. Salt logged a
failed fetch and returned the secrets it *did* get, so a state applied
with an empty password and nothing said so.

Every string an extension returns is treated as secret and goes to the
redactor. The hub cannot tell which of an out-of-process source's values
are credentials, so it assumes all of them are.

## Building and installing it

Four steps. The first is `go build`; the rest are the supply chain.

**1. Build.** One executable per platform you will run it on.

```sh
GOOS=linux GOARCH=amd64 go build -o build/aws-secrets ./cmd/halite-ext-aws-secrets
```

`make extensions` builds the ones in this tree for the host platform,
into `bin/`, which is enough to try it on one machine.

**2. Bundle and sign.** `-key` names the signing key and generates one
the first time, so step two and step three are one command.

```sh
go run ./tools/extbundle \
  -dir      ./build \
  -name     aws_secrets_manager \
  -version  1.0.0 \
  -kind     pillar \
  -exe      aws-secrets \
  -declares network \
  -key      ./ext-signing.key
```

That writes `manifest.json` — the digest of every file — and
`manifest.sig`, an Ed25519 signature over the Merkle root of the
manifest. It prints the trust key to put in `hub.yaml` and the root to
pin. Keep `ext-signing.key` off the hub: a machine holding both the
signing key and the extensions it verifies is verifying its own
signature.

**3. Check what it declared**, because the manifest is what the sandbox
is built from and an undeclared permission is one the extension will not
have:

```sh
grep declares ./build/manifest.json
```

**4. Publish it into the tree** under `_ext/<name>/<version>/`, where the
file server already serves everything else:

```
/srv/halite/states/_ext/aws_secrets_manager/1.0.0/
    aws-secrets
    manifest.json
    manifest.sig
```

Then tell the hub which key to trust and pin what may run:

```yaml
# hub.yaml
extension_trust_keys:
  - 'release AAAAC3NzaC1lZDI1NTE5AAAAI...'
extension_require_signature: true
extension_pins:
  aws_secrets_manager:
    version: 1.0.0
    root: 9f2c...        # printed by extbundle, and by `extensions sync`
```

Both halves of the pin matter. The version is a label the publisher
controls; the digest is not.

Fetch it, and see what will run:

```sh
halite-hub extensions sync
halite-hub extensions list
```

**Synchronizing fetches; it does not load.** Publishing an extension
into the tree cannot change a running hub — restart it to pick one up.
That is the difference SPEC 24.5 states plainly, and it is the point: in
Salt, syncing means "the agent will now execute new code from the file
server".

## Using it as a pillar source

```yaml
# hub.yaml
ext_pillar:
  - aws_secrets_manager:
      region: us-east-1
      secrets:
        - name: database.creds
          secret_id: arn:aws:secretsmanager:us-east-1:1:secret:prod/db-AbCdEf
```

The list keeps Salt's shape: one mapping per source, the key naming it.
The name is the *extension's* name, and a name with no installed
extension behind it stops the hub at startup rather than quietly
compiling a pillar without it.

Sources run after the top file, in the order listed, each seeing what
the ones before it produced. A source that fails fails the whole
compilation; `fail: ignore` inside a source's own block is the exception
for one that is genuinely optional.

## What is not solved yet

**An extension outside this repository has to implement the protocol
itself.** `internal/bridge` is what makes the example four hundred lines
instead of a thousand, and `internal` means exactly what it says — the
helper is not importable from another module. The protocol is specified
in SPEC 24.2 and an extension may be written in any language, but there
is no published package for it yet. An extension that lives in this tree
has no such problem, which is what the reference bridges of SPEC 24.4
are for.

## Further reading

- SPEC.md section 24 — the extension model, specified.
- [command-reference.md](command-reference.md) — the trust, pinning and
  sync surface in full.
- [DIVERGENCE.md](DIVERGENCE.md) 5.79 — what this change found.
