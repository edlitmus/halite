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

There are two complete ones in this repository.
[`cmd/halite-ext-aws-secrets`](../cmd/halite-ext-aws-secrets) is the AWS
Secrets Manager external pillar in Go — the same job
`_pillar/aws_secrets_manager.py` does in a Salt tree, and what this page
walks through.
[`contrib/extensions/python/example_pillar.py`](../contrib/extensions/python/example_pillar.py)
is a smaller one in Python, written against the wire with nothing but a
standard library, and it is there to prove the protocol does not need
Go. Both are driven by the test suite, so neither can drift from the
protocol without something failing.

## The Go package

`github.com/edlitmus/halite/ext` is the half of the protocol an author
needs, and it is the only package in this project that is not
`internal`. Importing it pulls one module with no third-party
dependencies of its own, and links one package.

```go
import "github.com/edlitmus/halite/ext"

func main() {
	// Applies the resource limits the host asked for. See "What the
	// sandbox is" below for what this does and does not buy.
	ext.Confine()

	e := &ext.Extension{
		Name:      "aws_secrets_manager",
		Version:   "1.0.0",
		Kind:      ext.KindPillar,
		Declares:  []string{ext.DeclareNetwork},
		Functions: functions(),
		Handler:   handle,
	}
	if err := e.Serve(); err != nil {
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

**`Functions`** is what it provides, in the shape of SPEC 15.6, which
the host reads at handshake and `sys.list_extensions` reports. A pillar
extension provides exactly one, `ext_pillar` — Salt's name, because a
person porting one is reading Salt's documentation.

```go
func functions() []ext.Signature {
	return []ext.Signature{{
		Module: "aws_secrets_manager", Function: "ext_pillar",
		Doc: "Fetch secrets into pillar['aws_secrets'].",
		Params: []ext.Param{
			{Name: "node_id", Type: ext.TypeString, Required: true},
			{Name: "config", Type: ext.TypeMap, Doc: "This source's ext_pillar block."},
		},
	}}
}
```

A parameter's type is its *name* — `ext.TypeString`, not an integer.
This field used to be `[]json.RawMessage` and the first extension
written here marshalled the host's own signature type into it, which
serialised the types as the integers they are internally. The host
refused every signature and reported an extension with no functions,
four steps from the cause. The typed field is why that is now impossible
to write.

**`Handler`** runs one call. It gets the function name, the arguments,
and `Log`, `Progress` and `Event` for the streaming frames.

**`ext.Confine()` is not optional**, and it is the one line that is easy
to leave out. The resource limits of SPEC 24.3 are applied by the child
to itself, because `setrlimit` bounds the *calling* process: a host
cannot set a child's limits without setting its own, so it names them in
the environment and a cooperating extension applies them.

An extension that skips it runs unbounded while `sys.list_extensions`
reports cpu, open-file and process limits as being in force — which is
worse than reporting none, because somebody reads it. Call it first
thing in `main`. An extension in another language does the same work
against `HALITE_EXT_RLIMIT_AS`, `_CPU`, `_NOFILE` and `_NPROC`;
`contrib/extensions/python/example_pillar.py` has it in about fifteen
lines.

**`ext.NetworkDenied()`** reports whether the host granted the network.
It is a declaration honoured rather than a boundary enforced, so an
extension that needs the network should check it and say so, rather than
failing later as a connection that timed out against a link-local
address.

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

## Running it while you write it

Packaging is six steps, and a one-line change does not deserve six
steps. `run` starts the file where it lies:

```sh
halite-hub extensions run ./my-source ext_pillar \
  --kwargs '{"node_id":"web1.prod","config":{"region":"us-east-1"}}'
```

No bundle, no signature, no cache, no restart. The result goes to
stdout, so it pipes; everything else — the handshake, `log`, `progress`
and `event` frames, and whatever the extension wrote to stderr — goes to
stderr, so you can see it.

It also reports what the handshake got wrong, which is where the
mistakes that surface furthest from their cause live:

```
$ halite-hub extensions run ./broken
broken  (kind not declared)
  go()
  ! go(): the how_many parameter declares no type; a type is its name, "string" and not a number
  ! it declared no version, so it cannot be pinned
```

**`--sandbox` is how you find out whether it works anywhere but here.**
By default `run` applies no confinement at all — your identity, your
limits, your network — because that is the fast loop. A node applies the
sandbox of SPEC 24.3, and an extension that has only ever been run
without one is an extension nobody has tested:

```
$ halite-hub extensions run ./my-source --sandbox
my_source 1.0.0 (pillar)
  declares: network
  sandbox: process boundary; network not granted; cpu 60s; open files 256; …
  ! it declares "network" and this run did not grant it; pass --declare network
```

`--declare network` grants it, the way a signed manifest would. Running
with less than the extension declares is the useful test: it is what
happens when somebody signs a bundle whose manifest is out of date.

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

## Writing one without Go

The protocol is JSON over stdio and nothing about it is Go's. An
extension in any language that can read a pipe and parse JSON works the
same way, and
[`contrib/extensions/python/example_pillar.py`](../contrib/extensions/python/example_pillar.py)
is one, in about a hundred and fifty lines with no dependencies. The
test suite starts it with the real host and asks it for pillar, so it
cannot quietly stop being correct.

The wire is four bytes of big-endian length, then that many bytes of a
JSON object:

```
+--------+--------+--------+--------+---------------------------+
|              length (uint32 BE)   |  JSON object, `length` bytes
+--------+--------+--------+--------+---------------------------+
```

Length-prefixed rather than newline-delimited, so a frame boundary does
not depend on nobody ever emitting a newline inside a string. A JSON
encoder that pretty-prints would otherwise break the stream, and it
would look like a protocol error in the host. A frame larger than 16 MiB
is refused before anything is allocated for it.

The host opens:

```json
{"kind": "hello", "protocol": 1, "extension_kind": "pillar"}
```

Answer, or exit non-zero having written the reason to stderr:

```json
{"kind": "hello_ok", "name": "example_pillar", "version": "1.0.0",
 "functions": [{"module": "example_pillar", "function": "ext_pillar",
                "params": [{"name": "node_id", "type": "string", "required": true}]}],
 "declares": []}
```

Then read frames until the stream ends or a `shutdown` arrives. For each
`call`, write zero or more of `log`, `progress` and `event`, then
**exactly one** `result`:

```json
{"kind": "call", "id": "1", "function": "ext_pillar", "kwargs": { … }}
{"kind": "result", "id": "1", "ok": true, "value": { … }}
{"kind": "result", "id": "1", "error": "the role cannot read that secret"}
```

Exactly one, whatever happens. An exception that escapes leaves the host
waiting for an answer that is not coming until the timeout kills the
process — a correct outcome reached slowly, and reported as a hang
rather than as the error it was.

Three things bite:

- **Stdout is the protocol.** A `print()` is a frame the host cannot
  read. Everything for a person goes to stderr.
- **A parameter's type is its name**, `"string"`, never a number. An
  enum serialised as an integer has every signature refused, and the
  extension then reports no functions at all.
- **Flush after every frame.** A buffered writer that holds the result
  until exit is an extension that hangs.

A bundle carrying a script rather than a compiled binary runs by its
shebang, so it needs an interpreter on the machine that runs it and it
is not portable to Windows, which has no such mechanism. A bundle for
Windows names the interpreter as the executable instead.

## Checking it against the protocol

`run` tells you whether an extension answered. Whether it is *right* is
a different question, and one an author working outside Go has had no
way to ask — the host is written to run extensions, not to diagnose
them, and its complaints surface a long way from their cause.

```sh
halite-hub extensions verify ./my-source --kind pillar
```

It drives the candidate through the protocol deliberately: a good call,
a call that cannot succeed, a version it should refuse, a shutdown it
should honour. Each rule is reported by name with what happened and why
the rule is there.

```
  pass  handshake/answers                    it answers hello with hello_ok
  fail  handshake/parameters-are-typed       every parameter declares a name and a type
        go(): how_many declares its type as 1, which is not a string
        A parameter's type is its name -- "string", "map" -- never a number.
        A language that serialises an enum as an integer produces exactly
        this, and the host refuses the whole signature: the extension then
        reports no functions at all, which is several steps from the cause.
  fail  call/answers-with-one-result         a call is answered with exactly one result
        calling go(): nothing arrived within the timeout. A writer that
        buffers until exit looks exactly like this: flush after every frame.
  skip  call/the-result-carries-the-call-id  the result carries the id of the call
        no result arrived

  5 pass, 3 fail, 3 skip
```

Fourteen rules, in four groups: the handshake, the calls, the lifecycle,
and what it refuses and tolerates. `--kind` is worth passing — without it the rules
about refusing the wrong kind are skipped, because there is no other
kind to ask for.

Three things to know about what it establishes.

**A skip is not a pass.** Each one says what could not be established
and why. A run with skips has checked less than it looks like.

**It exits non-zero on a failure**, so it belongs in whatever builds
your extension.

**It checks the protocol and nothing else.** A conforming extension can
still be entirely wrong about its own job. This is the part that can be
checked without knowing what the extension is for.

Both extensions in this repository pass all fourteen, and a test asserts
it — an example that has quietly stopped conforming teaches the wrong
thing to everyone who copies it.

## What you can rely on, and what will break you

The protocol is a published interface now, which means the version is a
promise rather than a constant. SPEC 24.7 is the policy; this is what it
means for somebody maintaining an extension.

**One version, offered, not negotiated.** The host names a single
integer in its `hello`. Speak it or refuse and exit non-zero. There is
no range and no fallback — a host that negotiated down would have a path
in it that nothing tests, and an extension that answered a version it
had never seen would be agreeing to a contract it cannot have read.

**Ignore a field you do not recognise.** This is the one rule that costs
nothing to follow and breaks everybody if you do not. It is what makes a
field addable at all. A strict decoder is a reasonable instinct and this
is the one place it is wrong: an extension that refuses an unknown field
works today and stops working the first time anything is added, on every
host it is installed on. `extensions verify` checks it.

**Refuse a frame kind you do not recognise.** The other direction, and
deliberately not symmetric with fields: a receiver that skipped an
unknown kind would also skip a misspelt one, leaving the sender waiting
for an answer to a frame that was silently dropped.

These can happen without a new version, and an extension built against
an earlier one keeps working:

- a new optional field on an existing frame;
- a new extension kind, parameter type name, or value in an enumerated
  field — an unknown type name means untyped, not refused;
- a new function, or a new parameter on one.

These require a new version, and you will be told:

- any change to the framing, the length prefix, or the size limit;
- a frame kind added, removed, or renamed;
- a field removed, renamed, or given a new meaning;
- an optional field made required;
- a change to the order of the handshake.

**Declaring something new is neither.** An extension that declares a
permission an older host does not know is refused by it — correctly,
because the host cannot grant what it cannot enforce, and running as
though it had is the failure the declaration exists to prevent. Your
extension will not run there. That is not a break to be fixed; it is a
permission an older host is right to withhold.

## Further reading

- SPEC.md section 24 — the extension model, specified.
- [command-reference.md](command-reference.md) — the trust, pinning and
  sync surface in full.
- [DIVERGENCE.md](DIVERGENCE.md) 5.79 — what this change found.
