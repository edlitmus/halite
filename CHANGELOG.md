# Changelog

halite was rewritten against [SPEC.md](SPEC.md) starting 2026-08-19. The
proof of concept that preceded it — releases 0.1.0 through 0.12.0 — was
deleted rather than evolved: it had established what the shape should be,
and carrying its code forward would have meant carrying its assumptions
too. None of it remains, so its changelog has gone with it. `git log`
before `3aec717` is where that history lives if it is ever wanted.

Nothing here is released yet. Phases 0, 1, and 2 of the delivery plan in
SPEC section 32 are complete: a node that manages its own tree, and a
fleet driven from a hub. Phases 3 and 4 are complete too — the runners,
orchestration, reactors, beacons, the scheduler, the mine, the API, and
the extension model. Versions resume at 1.0.0
when SPEC section 32's phase 6 exit criteria are met.

## Unreleased

The state of the rebuild, by what it means rather than by commit.

### A node manages its own tree

`halite-node state apply --local` compiles and applies a tree with no hub:
top file, includes, extend, exclude, every requisite including `prereq`
and the `_in` and `_any` forms, ordering with `order` and `order: last`,
and the return schema of SPEC section 11.8. Errors from the whole
compilation are reported together rather than one at a time.

Verified against a real tree on a real machine, not only in tests: a
grain-matched top file with an include, a templated pillar loop, a
`salt://` source, `require` and `onchanges`, converging and then
reporting nothing to do on the second run and reconverging after a
hand-edit.

### A fleet is enrolled and driven from a hub

`halite-hub serve` is the control plane of SPEC section 6: one mutual-TLS
port, an enrollment CA of its own, and NDJSON over a stream the node
opens outward. A node generates its key, asks to enrol, and waits; an
operator compares the fingerprint out of band and accepts. `halite-node
enroll`, `renew`, and `connect` are the other half. There is no
pre-shared secret and no key an operator has to copy.

`halite-hub run '<target>' <fun>` resolves the target against the grains
each node reported, records the job with its expected respondents
*before* delivering it, and gathers the returns from the job cache. A
missing return is therefore detectable rather than invisible, which is
the difference between "it said no" and "it said nothing" — and the
command exits 1 and 3 for those two things.

An operator edits the tree on the hub and the fleet converges to it,
which is the exit criterion SPEC section 32 names for the phase. A node
compiles against the hub's tree, caches what it fetched, and asks
conditionally afterwards, so redeploying an identical tree costs a round
trip and no transfer. Pillar is compiled on the hub, per node, from the
identity on the certificate — so a node holds no other node's secrets
and has no way to ask for them.

`--batch` belongs to the hub rather than to the terminal. In Salt it
lives in the CLI, so closing the terminal abandons the run with half the
estate updated and no record of where it stopped; here the group has its
own record, `jobs active` says what is in flight, `jobs resume` picks up
a batch a hub restart interrupted, and a safe limit stops the rest of the
estate getting the same broken change.

Every submission is authorized against one policy file that denies by
default — including when the file is absent, which the hub says at
startup rather than treating as permission. A wildcard never grants a
function that runs arbitrary code, and the set of those comes from the
signatures the build ships rather than from a list, so a function marked
in a later build is covered without anyone remembering.

The event bus is a durable segmented log, not Salt's in-memory one. A
subscriber resumes from an offset, so a restart is lossless and an
incident can be reconstructed afterwards — which is exactly what a Salt
estate discovers it cannot do during one. A node's events are namespaced
under its own ID whatever tag it asks for: in Salt a minion can fire any
tag onto the master's bus, and a reactor listening on that tag turns that
into fleet-wide execution. <!-- lexicon:allow -->

Verified with real processes rather than only in tests: a hub and two
nodes, a highstate driven across both, applied, and run again to
convergence. [DIVERGENCE 5.11](docs/DIVERGENCE.md) says what that
established, what it did not, and the defects it found that the tests had
not.

### Values keep their type across the wire

An ordered mapping is a mapping wherever it goes, and a 64-bit integer
is still that integer when it arrives. Neither was true: the standard
JSON encoder cannot see the ordered model, so every structured argument
an operator typed reached the hub as a position record —
`run '*' state.apply pillar='{"a":1}'` among them — and three decoders
turned every number in a payload into a float64, which SPEC 6.4 says
they must not.

`event.send` and `pillar.refresh` work on a node with a hub. Both had
been registered as stubs telling the caller they needed a phase that was
already delivered, so a tree using either failed on every node. An audit
now reads the stubs out of the source and fails on any that names a
phase that has landed.

### Functions that run on the hub

`halite-hub runner <module.function>` is the old `salt-run`, and the
start of phase 3. It is a request to the hub even when it is typed on the
hub, because an operator authenticates with a certificate and being
logged in is not one.

A runner is granted by the `runners:` list of a role rather than by
`functions:`. Permission to ask the hub a question and permission to run
a command on every node are different permissions, and Salt's
`external_auth` conflating them is how a `@runner` grant turns out wider
than it looked. A runner that then reaches the fleet —
`saltutil.refresh_pillar` does — is authorized a second time as the job
it dispatches, so the narrower grant cannot become the wider one.

Forty-two functions across `jobs`, `manage`, `key`, `nodegroups`,
`pillar`, `cache`, `fileserver`, `event`, `saltutil`, `survey`, and
`error`. Every call gets a jid, is filed in the job cache with the
principal that asked for it, and puts `halite/run/<jid>/new` and its
return on the bus — so "who asked the hub to accept that key, and when"
has an answer on disk.

The other forty names in SPEC 19.2's inventory are registered and answer
with the phase they arrive in, and `halite-hub runner list` prints the
whole inventory either way. Leaving a name out of the registry would make
"orchestration is not written yet" and "you have mistyped
`state.orchestrate`" the same message at the terminal, and an operator
cannot tell those apart.

### Orchestration, with a timeline and a resume

`halite-hub orch run <sls>` compiles an orchestration on the hub and
drives it across the fleet. The SLS syntax is Salt's, and so is the
meaning: an orchestration here *is* a state run whose modules act on the
fleet, compiled and executed by the same compiler and runner a node
uses. `require`, `onfail`, `prereq`, and ordering therefore mean exactly
what they mean in a highstate, and a rollback step is expressible
because `onfail` is the real thing rather than a second implementation
of it.

A run is a first-class record with its own jid, kept on disk: every step
in the order it ran, the job each dispatched, and the per-node returns.
`orch show` prints it, and `orch resume <jid> --from <step>` runs again
from a named step, carrying the earlier ones forward as they finished so
the requisites pointing back at them are satisfied. Salt cannot resume,
and that is what makes a long deployment orchestration usable after one
step fails. A resumed run is judged by the steps it ran, not by the
failure that made someone resume it.

Every step is authorized twice — once as the orchestration, again as the
job it dispatches — so permission to run an orchestration is not
permission to run whatever it happens to name. `--test` sends state
steps out with `test` set, because a state honours test mode by
contract; it does not dispatch an execution function at all, because
finding out what a test run would do by running it is how a test run
becomes a deployment.

### The fleet, over HTTP

`/v1/run`, `/v1/jobs`, `/v1/nodes`, `/v1/keys`, `/v1/orch`, and
`/v1/pillar/{id}`, with Salt's netapi client types preserved so an
existing CI job posts the shape it always did.

Every request is authorized twice, against the same policy file. The
operator behind the token is authorized at the API — without that,
logging in would hand out the service's whole authority. The service
then forwards under its own certificate and the hub authorizes that,
which is what makes it a client rather than a component: compromising it
yields one grant, not the control plane.

The roles a token was issued with are what decide. A role granted after
the token was handed out does not widen it, and one taken away is a
reason to revoke rather than a change that applies mid-session.

A job records both identities: the certificate the hub authorized, and
the operator it was submitted for. The second is recorded and never
trusted — the hub reads identity from the connection and nothing from
the body — but "who really asked for this" now has an answer that is not
a service account.

Reading a node's pillar needs the role to name it. A wildcard never
carries it, because a role written to let someone restart a service
should not read the estate's secrets.

### An API that knows who is asking

`halite-api serve` runs. It is a client of the hub, not a component of
it: it holds its own operator certificate, and its worst case is bounded
by the policy that certificate is bound to. In Salt the API process
loads the control plane's configuration and calls into its internals, so
a flaw in the API is a flaw in the control plane. <!-- lexicon:allow -->

Local accounts are PBKDF2-HMAC-SHA-512 through the standard library.
Each hash carries its own cost, so the floor can be raised without
invalidating what is already stored — and a hash below the floor is
refused rather than quietly re-hashed, because accepting it silently is
how it stays. TOTP is there for a second factor.

A login that fails says one thing however it failed, and an account that
does not exist is verified anyway so the answer does not come back
faster for a name nobody has. Which of the three it was is in the log:
the difference between "no such account" and "wrong password" is the
difference between a guess and a confirmed name.

A token is 256 bits from crypto/rand stored as a SHA-256 digest, so a
token file that is read is not a set of working credentials. Both
expiries, an optional source network, roles frozen at issue, and
revocation individually or by principal. The secret is returned once and
appears in no log, no event, no error, and no URL.

### The bus, out and in

`GET /v1/events` streams the event bus as SSE. The `id:` of each message
is the bus offset, so a client that reconnects with `Last-Event-ID`
resumes at the event after the last one it saw rather than at "now" — a
stream that silently restarts at the present is the difference between
an audit trail and a sample. `GET /v1/ws/events` carries the same events
over a WebSocket, hand-rolled against the standard library because SPEC
4.2 allows no third-party code: masked client frames required,
fragments reassembled, a length claim refused before anything is
allocated for it, and a ping every thirty seconds so an intermediary
does not close a quiet stream.

Both transports share one filter rather than implementing it twice. A
tag naming a node reaches only a caller whose policy targets that node;
an event about no node in particular reaches any caller the policy
grants something; a principal bound to nothing sees nothing. Two
authorization paths over the same events would be two chances to leak,
and the one that leaks is the one nobody tested.

`POST /v1/hook/{path}` takes deliveries the other way. It is
authenticated by construction: there is no setting that produces an
unauthenticated hook, and one configured without a credential is refused
when the file loads rather than served. HMAC-SHA-256 over the timestamp
and the raw bytes together, a replay window, a nonce cache, a
content-type allowlist, and a body limit.

A delivery becomes an event under `halite/hook/<path>` carrying the
principal it authenticated as, so a reaction decides on that identity
and never on the payload — which was written by whoever sent it. The
hook's principal is an ordinary RBAC identity with an ordinary binding,
so a hook that may cause a deployment says so in the policy file.

The nonce is recorded once the delivery has landed on the bus, not when
the signature verifies. Recording it earlier is strictly safer against a
replay and costs more than it saves: a delivery that fails downstream is
one the sender will retry carrying the same signature, and refusing that
as a replay turns a transient fault into the lost event a webhook exists
to prevent.

### Metrics, on both components

Prometheus text exposition, written directly. SPEC 26.2 says the format
is documented and stable and needs no client library; SPEC 4.2 says a
dependency in the supply chain of a control plane needs a better reason
than saving a hundred lines of formatting.

`GET /v1/metrics` on the API is the estate's scrape target, and it is
the only part of the control plane a scraper can reach — the hub speaks
its own ALPN protocol over mutual TLS. It answers with both expositions,
its own and the hub's, fetched under its own certificate and merged.
Merged, not concatenated: both components expose `halite_build_info`,
which is what its `component` label is for, and the format allows one
`# HELP` per metric name in a document. Concatenating them produces a
body a scraper rejects entirely, so the failure arrives as "no metrics
at all" rather than as one duplicated family.

A hub that cannot be reached does not fail the scrape. The reason comes
back as a comment, which a scraper ignores and a person reading the body
sees, and the service's own numbers survive — one of which counts how
often this happens.

The hub has the same endpoint behind its ordinary operator certificate,
granted as the runner `metrics.show`, and `halite-hub metrics` to read
it. An unauthenticated scrape endpoint on a control plane tells anyone
who asks how many nodes it has, what the estate runs, and when a
deployment went out.

Two decisions the format does not require. A family is declared before
anything has been observed, so that SPEC 26.2's rule — every bounded
queue and every drop path has a counter — can be checked by a scraper
rather than by reading the source. And a family holds at most 512
series, the excess counted under `__overflow__`: every label the
specification names is written by something outside the program, and an
estate with a thousand distinct states would otherwise turn one family
into a thousand series.

What a node alone knows is counted nowhere yet, because a node has no
exposition endpoint: a beacon event its own queue dropped, a local state
run's duration, the scheduler's `maxrunning` skips.

### Returns go somewhere durable

The six returners SPEC 20.3 marks Full: `local` and `file` (append-only
NDJSON, the second with rotation), `local_cache`, `syslog`, `webhook`,
and `smtp`. The seventeen it marks Bridged are named as bridged rather
than omitted — an operator who writes `returner: postgres` has made a
reasonable request, and "postgres is not a returner" would be a lie.

The webhook returner is the one SPEC 20.3 describes in three parts, and
the third is what makes the other two worth having: HMAC-SHA-256 over
the timestamp and the raw body, retry with bounded backoff, and a
durable spool. Without the spool the returns lost are exactly the ones
from the incident that took the receiver down. The backlog goes out
ahead of new returns so the order survives; a 4xx is not retried,
because a request the receiver will never accept would otherwise fill a
disk; and a full spool refuses rather than making room, because a spool
that silently discards is the failure it exists to prevent.

`event_return` ships the whole event stream, which SPEC 20.3 calls the
recommended path to a SIEM. It resumes from a bus offset, so a receiver
that was unreachable for an hour catches up rather than leaving an
hour-shaped hole in the audit trail, and a delivery failure does not
advance the offset.

Syslog is RFC 5424 written directly rather than through `log/syslog`,
which speaks the older RFC 3164 and does not exist on Windows — a
tier-1 platform in SPEC 27.1.

TLS is required wherever a return leaves the machine. The webhook
returner refuses an `http://` url and takes a CA file so an internal
receiver works without anyone reaching for a way to skip verification;
SMTP refuses to send credentials without STARTTLS.

### An operator logs in through the estate's identity provider

SPEC 23.4's two paths: Authorization Code with PKCE for a person, and a
token presented directly for automation that has no browser.

Written against `crypto/rsa`, `crypto/ecdsa`, and `encoding/json`
because SPEC 4.2 allows no third-party code — and because a JWT library
is a place where the accepted algorithm list is somebody else's default.
Here it is ours: the nine SPEC 23.4 names, with `none` absent and every
`HS*` absent. That closes the algorithm confusion attack, where a token
is signed with the provider's own public key as an HMAC secret and a
verifier that trusts the header's `alg` accepts it. The algorithm's key
type is checked against the key that was found, so a header claiming
RSA cannot be verified against an EC key.

Issuer, audience, expiry, not-before, and nonce are all verified. A
token with no `exp` is refused: one that never expires is a password
with a longer name. Clock skew is capped at five minutes however it is
configured, because it is an allowance for drift rather than a grace
period.

The key set respects the provider's `Cache-Control`, bounded at five
minutes and a day, and an unknown `kid` causes one rate-limited refresh.
A rotation is therefore invisible here, and a stream of invented key
identifiers is not a way to make this service hammer the provider on
somebody else's behalf. A token with no `kid` verifies against a set of
one and is refused against a set of several: trying each key and
accepting if any verifies turns key rotation into an attack surface.

Groups map to roles through a table the estate writes. A group with no
entry grants nothing — the provider's directory is not this estate's
authorization model — and an operator whose groups all map to nothing is
told which groups they had, because that is actionable where "access
denied" is not. A session never outlives the assertion it was made on.

### An operator logs in against the estate's directory

A bind-only LDAP client, written against `encoding/asn1` as SPEC 23.3
asks. The surface is the six operations it names, and every LDAP feature
left out is one that cannot go wrong here. Referrals are not chased:
following one means authenticating against a server the estate did not
configure.

There is no plaintext mode. A simple bind puts an operator's password on
the wire, and a client that can be configured without TLS will be, on
the day somebody is debugging something. A StartTLS the directory
refuses ends the login rather than continuing in the clear.

Anonymous bind is refused in both directions. The service account is
required, and an empty operator password is refused before the directory
is asked — RFC 4513 makes an empty password an anonymous bind, which a
directory answers success to, so a client that passes one through
authenticates anybody who leaves the password field blank.

The username never becomes part of a DN. It goes into a filter, escaped
per RFC 4515, and the filter is parsed into BER rather than concatenated
as text: `*)(objectClass=*` in `(uid=%s)` would otherwise turn a lookup
for one account into a match for every account. A filter that matches
two entries is refused, because binding as whichever the directory
listed first authenticates one operator as another.

Groups come from `memberOf`, from a group search with `member` matching,
or both, with nested groups followed to a configured depth for Active
Directory and a membership cycle terminating rather than hanging.

Every failure gives the operator one message, and the log says which it
was. Not as a boolean: "was the directory reachable" gets the commonest
case wrong, because a blank password field is refused here without the
directory being asked and is not an outage.

Checked against `ldapsearch`. A real OpenLDAP client binds over LDAPS to
this build's test directory, sends a compound filter, and parses the
responses, so both halves of the BER are validated by an implementation
this project did not write.

### Extensions run out of process, signed and pinned

SPEC 24.1 names the problem: Salt's extensibility is a Python file
dropped in `_modules/` on the file server, which the agent imports and
runs in process, as root, with no signature requirement. That is a code
distribution channel, and it is load-bearing, so it needed replacing
rather than removing.

An extension is a separate executable speaking length-prefixed JSON over
stdio. Length-prefixed rather than newline-delimited, because a frame
boundary must not depend on an extension never emitting a newline inside
a string. Concurrency is a process pool, so an extension never has to be
thread-safe. A hung one is killed and replaced, so it cannot hang the
agent. A protocol violation kills the process rather than failing the
call: an extension that sent something unreadable has lost its place in
the stream, and its next frame answers a question nobody asked.

A bundle is signed with Ed25519 over a Merkle root of its contents and
verified on every load, not once at fetch — the cache is a directory on
a managed node. Verification runs in both directions: a listed file
whose digest is wrong is tampering, and an unlisted file that is present
is one nobody signed, sitting somewhere the extension can load from. The
root covers paths as well as contents, so a bundle cannot swap which
file is the executable without changing what it is signed as, and the
signed message carries a domain separator so a bundle signature can
never be replayed as a signature over anything else.

It is pinned by version and digest. The version is a label the publisher
controls; the digest is not.

`sys.list_extensions` reports what actually confines an extension on the
machine in front of you — the process boundary, the dropped identity,
the resource limits — and names what does not: Landlock, seccomp,
`pledge`, `unveil`, and Windows job objects are SPEC 24.3 and are not
built. An operator should not have to read the specification to find out
which rows of its table are real here.

An extension's functions are marked `arbitrary_code`, so a wildcard in
the RBAC policy never grants one. A name that collides with a built-in
is refused rather than overriding it: in Salt, a file on the file server
can change what `service.running` does.

Bundles are delivered by the file server under `_ext/` and fetched with
`saltutil.sync_all`, whose Salt name is kept and whose meaning is
stated: synchronizing fetches, it does not load, and what is running
does not change until the node restarts. A bundle is verified in a
staging directory and moved into the cache only if it verifies, so a
node running a good version does not lose it because somebody published
a bad one.

SPEC 20.3's seventeen bridged returners — postgres, redis, kafka, and
the rest, each needing a driver a control plane does not link — are
extensions of kind `returner`, found by name. Configuring one no longer
deadlocks the node: a returner extension arrives through
`saltutil.sync_returners`, which needs a running node, so a name this
build does not have fails every return with the reason rather than
stopping the node from starting.

For a formula that carries custom Python, `halite-hub migrate
--bridge-skeleton <dir>` writes one Go command per module with the
function names, parameters, and defaults read from the source. The
porting job does not go away — SPEC 24.6 says so plainly — but it stops
being unbounded. Every generated function returns an error, so a bridge
that was generated and forgotten fails loudly rather than answering
nothing and looking as though it worked.

### The state tree can come from git

SPEC 13.3's git file server, through the system `git` binary. That
replaces pygit2 and libgit2 — together a large C dependency with its own
CVE history — and means an estate gets its operating system's git
patching cadence rather than this project's.

A bare mirror is fetched and verified; the ref that is served is
materialised into a directory that becomes a `roots` search path. The
manifest, the hashing, the ignore globs, the conditional requests and
ranges are all the existing code. A gitfs that served blobs through its
own path would be a second implementation of file serving, and the
second one is the one with the traversal bug in it.

A branch becomes an environment. Tags do not unless the estate asks:
every tag becoming one turns a release history into a file server.
Roots come first, so a local directory still shadows a repository for
the same path.

`gitfs_verify_signatures` is a control rather than a log line. A ref
whose tip commit or tag is not signed by a key in `gitfs_keyring` is not
served. Verification with no keyring is refused, because checking
against the hub user's own GnuPG home would pass for whatever that user
happens to trust — which is not a decision anybody made.

A repository that goes unreachable does not empty the file server, and
neither does one whose refs are all refused: the last tree that verified
stays, because a network blip or a withdrawn signing key must not take
the estate's state tree away. A branch deleted upstream does stop being
served, because an estate that keeps applying a tree nobody maintains is
the other failure.

`git://` and `http://` remotes are refused unless the remote says
`insecure: true`. A state tree fetched over an unauthenticated transport
is whatever the network says it is.

### The state tree can come from S3

SPEC 13.4's S3 backend, with SigV4 signing written directly. The whole
of what is needed is a canonical request, a string to sign, a key
derived over four HMAC-SHA-256 rounds, and an Authorization header;
importing the AWS SDK would add hundreds of packages to satisfy it.

A signing algorithm's failure mode is a signature that does not verify,
so an implementation agreeing with itself proves nothing. This one is
checked against AWS's published derivation of the signing key for its
documented example credentials, and against an S3 that recomputes the
signature with its own implementation.

The details that are easy to get wrong: the path is not re-encoded,
because a key holding a literal `%2F` and a key holding `/` are
different objects; a space in a query value is `%20` and never the `+`
that Go's `QueryEscape` produces; every header is signed, because one
left out is one a proxy can add; and the session token is set before the
canonical headers are built, because a token added after signing can be
stripped in flight.

Credentials resolve in SPEC 13.4's order — configuration, environment,
container endpoint, instance metadata — with **IMDSv2 only**. IMDSv1 is
a plain GET on a link-local address that any process on the instance can
make, including a server-side request forgery in an application running
there, and falling back when v2 refuses would give the hardening away
for a convenience nobody asked for. IRSA is tried first when configured,
because a pod holding a web identity token has it instead of the node's
role.

Endpoints and the STS host are built from a partition value rather than
hardcoded to `aws`: one built for the commercial partition is wrong in
GovCloud and in China.

### A machine with no agent can still be driven

`halite-hub ssh`, SPEC section 21, replacing `salt-ssh`. It is simpler
than the original for one reason: it has a static binary to push. Salt
ships a Python "thin" tarball and then has to find or bootstrap a
compatible Python on the target, which is where most `salt-ssh` failures
come from.

The connection is the system `ssh` binary rather than a linked library.
That is what makes an estate's `ssh_config`, `ProxyJump`,
`ProxyCommand`, certificate authentication, agent policy, and
`known_hosts` handling work without any of it being written again here —
and it means `paramiko`, the largest dependency in `salt-ssh` and the
source of its most persistent bugs, is not replaced by anything.

The binary is pushed once, verified by digest after transfer, and cached
under `<thin_dir>/<digest>`, so a second run skips the transfer. It
takes its cached name only after verifying, so a concurrent run never
finds a half-written binary, and a target with no digest tool at all is
a refusal rather than an unverified executable.

Pillar and the state tree are compiled on the hub and sent with the job.
A target therefore holds no tree, no pillar, and no other target's
secrets — the same property an enrolled node has. It uses what the hub
sent and only that: a local fallback would compile against whatever a
previous configuration system left on the machine.

ssh hands its command to the target's *login* shell, and a login shell
is not always POSIX. Every setup script goes over stdin to `sh -s`,
where no quoting is involved at all; the one command that cannot — the
job invocation, whose stdin carries the job — takes values that are
validated rather than escaped, because the POSIX escape for a quote does
not survive every shell.

The return is framed, because a login banner, a motd, a sudo lecture,
and a `.bashrc` that echoes something all arrive on the same stream.

Rosters are `flat`, `sshconfig`, `cache`, and `ansible` — the last
because many estates have an inventory already, and asking them to write
the same list of machines again to try this is asking them not to try
it. Targeting is the ordinary grammar against the roster's grains.

SPEC 21.3's limitations are stated rather than worked around: with no
persistent connection there are no beacons, no scheduler, no mine, no
presence, and no node-initiated events for an agentless target.

### A running node can be changed without restarting it

The nineteen management functions of SPEC 16.1 and 20.1 act on the
running engines. A watcher or a schedule that can only be changed by
restarting the node is one nobody changes during an incident, which is
when the reason to change it usually arrives.

Disabling holds without forgetting, so enabling afterwards restores
exactly what was there. Modifying keeps what the change did not mention:
a beacon turned off during an incident stays off when someone fixes its
threshold, and a job keeps its last run so an interval does not restart
every time it is adjusted. `schedule.run_job` runs one job now, out of
its turn and without splay, and leaves the schedule alone.

A change lives in memory until `save` writes it to a file of the node's
own under `beacons.d` or `schedule.d` — numbered last, so a runtime
change beats the file it was made against, and never over what a package
manager put there. What it writes parses back into the same beacons and
jobs, and a person can edit it.

### The mine

`mine_functions` on a node computes values and publishes them; a state
on another node reads them. That is how a load balancer's configuration
learns its backend list, and the backends are what said it.

The store is on the hub rather than passed between nodes: a node asking
another node directly would be a second authorization surface and a
connection in the wrong direction. A node publishes under the identity
on its certificate and no other, which is what makes the answer worth
believing.

Reading is the peer interface, and it is deny-by-default in the one RBAC
policy rather than in Salt's separate `peer` dialect. A grant names the
functions and the targets and nothing wider. `allow_tgt` sits on top of
that as the publisher's own restriction — a node publishing something
sensitive decides who may see it without trusting every reader's policy
to be right.

### A scheduler that gets the clock right

`schedule:` runs jobs on a node with no hub involved, which is how a
node keeps itself converged and matters most exactly when the hub cannot
be reached.

The cron parser is written directly. It implements standard cron's
surprising rule — both day fields restricted means *either* matches — so
a crontab moved here keeps meaning what it meant, and it refuses `L`,
`W`, `#`, `?`, and a seconds field by name, because a `0 0 L * *` that
quietly runs on the first of the month is worse than one that will not
load.

Time handling is where missed runs come from, and SPEC 20.1 spells it
out. Schedules evaluate on the wall clock out of Go's embedded time zone
database, so the node needs no tzdata package. An hour that repeats runs
a job in it once rather than twice. An hour that is skipped runs it once
at the transition — computed, because Go resolves a nonexistent local
time to one side of the gap without saying which.

`splay` comes from crypto/rand: a fleet whose splay is predictable has
arrival times anyone who knows the schedule can predict.

A scheduled job's return goes to the `local` returner — append-only
NDJSON on the node — which is what SPEC 20.3 makes the default. Sending
it to the hub is `local_cache`, and the hub refuses a return for a job
it never dispatched.

### Beacons, and the loop closes

`beacons:` on a node starts the watchers of SPEC section 16, and what
they see goes to the hub's bus where the reactor is waiting. A file
edited by hand, and the estate does something about it.

A beacon is a function over the node's own execution modules rather than
a second reader of the system: `diskusage` asks `disk.usage`, `service`
asks `service.status`. It is portable wherever its module is, and a
beacon and the state that manages the same thing cannot disagree about
what the thing is.

Seven are built — diskusage, load, memusage, service, filechanges,
cert_info, status — and the other seventeen names in the inventory say
when they arrive. A configuration naming one that is not built stops the
node rather than leaving the watcher silently absent, which is the worst
available outcome for a watcher.

The controls are the design rather than a refinement, because beacon
events are the classic self-inflicted denial of service: a token bucket
per instance, identical events collapsed into one carrying a count, a
bounded queue that reports what it dropped, and
`disable_during_state_run`, which is how a state run is stopped from
firing the beacon that triggers the state run.

`inotify` is not built: it needs raw syscalls through a module SPEC 4.2
leaves as an open question. `filechanges` polls on digest and metadata,
which is what the specification names for platforms without a notifier.

### Reactors, authorized and unserialized

`reactor:` maps an event tag glob to the reaction SLS the hub runs when
a matching event arrives. The four reaction types and the SLS syntax are
Salt's, so an existing reaction translates unchanged. Two things are
not.

A reaction runs as a named principal and is authorized by the policy
exactly like a human caller. Salt's reactor runs with the control
plane's full privilege, so a node that can fire the right event can
cause arbitrary fleet-wide execution; an entry here that names no
principal gets a restricted default bound to nothing, and stays refused
until someone writes what it may do.

And it does not serialize. Salt's reactor is single-threaded, so a burst
becomes a backlog and the backlog becomes an outage — the most common
scaling failure in a Salt estate. This is a worker pool with same-chain
events hashed to a fixed worker, a bounded queue that drops the oldest
and reports the count rather than blocking the bus reader, and per-glob
debounce, deduplication, and rate limiting. A reaction that fails to
render or dispatch says so on the bus; Salt fails that silently, and the
event does not come again.

`runner reactor.test` renders what a tag would fire and prints what it
would dispatch — including what the policy would decide — without
dispatching any.

### The dialects, held to their own specifications

The **YAML 1.1 subset** parser of SPEC section 10.1, with ordered
mappings, anchors, aliases, merge keys, block scalars with correct
folding, and a node budget that stops an alias bomb. It runs the YAML
Test Suite on every `go test`: 331 of 402 cases agree, 34 disagree by
design under section 10.1.2, and 37 are recorded gaps. The dialect SPEC
10.1 actually asks for is PyYAML's rather than the standard's, so 114
documents also go to PyYAML itself and the *resolved type* is compared
as well as the value — the two agree on every character of `mode: 0644`
and can still disagree on whether it is a string or the integer 420.
That found SPEC 10.1.3 stating PyYAML's behaviour incorrectly twice
more, for `0o17` and `1e3`.

The **Jinja-compatible template engine** of section 10.2, with the
statements, filters, and tests that section names, strict undefined by
default, and deterministic seeding so a `--test` run and the run after it
agree. It runs two corpora: 198 cases extracted from Jinja's own test
suite, of which 157 agree and 26 are outside the subset by design, and
123 written here for the surface those cannot reach.

Both corpora are enforced in both directions — an unrecorded disagreement
fails, and so does a recorded one that has been fixed — so neither can
quietly go stale.

### 44 execution modules, 20 state modules

240 execution functions and 56 state functions, against a specification
naming roughly 90 modules and 46. Every one is listed, with its
parameters, in [docs/modules.md](docs/modules.md); every gap is listed in
[docs/DIVERGENCE.md](docs/DIVERGENCE.md).

`x509` is worth calling out. Salt's needs M2Crypto or `cryptography`
compiled against OpenSSL headers, which is the most common reason a Salt
install fails; this one is `crypto/x509` and needs nothing. Its
`certificate_managed` also converges, where Salt's re-issues on every
highstate — a re-issued certificate has a new serial and a new expiry, so
it never matches what the last run left.

An existing `#!yaml|gpg` pillar works. SPEC 12.6 fixes the shape and this
follows it: shell out to the system gpg, link no OpenPGP library, take
the binary, home directory, and timeout from configuration, and pass the
ciphertext on standard input and never on a command line. Salt's
`gpg_keydir` maps onto `gpg_home` through the compatibility shim.

### The departures from Salt, each with a switch

Undefined template names are an error naming the file, line, and
identifier. `cmd.run` takes an argument vector and a shell is explicit
and logged. Command line arguments are strings unless the signature says
otherwise. Duplicate YAML keys are an error naming both lines. A
regular expression RE2 cannot express is refused by name rather than
quietly failing to match. `--test` is a contract enforced by a shared
harness every state module passes.

Each is described in the table in [README.md](README.md) with the
specification section that defines it, and each has a setting that
restores Salt's behaviour for a transition.

### Documentation

[Getting started](docs/getting-started.md), [writing
states](docs/states.md), [operations](docs/operations.md), [migrating
from Salt](docs/migrating-from-salt.md), and a [command
reference](docs/command-reference.md) giving every Salt command and what
to type instead — plus a configuration reference and a module reference
generated from the code and checked against it by a test.

The prose is checked too, as far as prose can be. A test runs every
command the matrix presents as working and confirms that every command
it promises in a later phase is one the binary already knows the name
of; another reads any sentence stating how many functions ship and
compares it with the registries. Example configurations live in
`contrib/examples/`, and a test loads each as the program it is written
for and fails on any warning.

Service files for FreeBSD `rc.d` and systemd are in `contrib/`. The
periodic-highstate ones, the `halite-hub` and `halite-node` daemons, and
`halite-api` all work today.

### The tests the specification asks for

Of SPEC section 31's fourteen layers: unit coverage on every package, the
YAML conformance suite, the template corpus, the state-module conformance
harness (stronger than specified — it also checks that test mode changed
nothing), all five named property tests, fuzzing of the YAML parser, the
template engine, and the target parser, the dependency-graph assertion,
and the FreeBSD half of the version-comparison differential.

The **differential against Salt** — SPEC 31's primary correctness gate —
runs, against Salt 3006.25 and 3008.2. It compiles a corpus of trees
with both implementations and compares all three things the section asks
for: the low state, the pillar, and the state results, the last as
test-mode predictions rather than as an apply, since applying needs
somewhere to apply. `HALITE_SALTDIFF_TREES` points it at a real estate's
tree, and doing that to one found ten defects in an hour — among them a
`names:` entry whose own arguments were dropped, which on
`file.managed` meant seven scripts would have been overwritten with
empty files.

Absent, and recorded as such: the comparison of what an apply actually
does, which needs the containerised harness; the integration, scale,
upgrade, and chaos suites, which need the hub; `govulncheck`; and
reproducible-build verification, which needs a second builder.

### A relay serves a segment and answers upstream as one client

`relay: true` makes a hub a relay of SPEC 5.3. It serves its own nodes —
issues their certificates, dispatches to them, files their returns — and
presents itself to its upstream as a single connected client. Nodes
behind it enrol with it, not with the upstream, and the upstream holds
no key for them: what it holds is the relay's assertion, accepted only
from a certificate its policy grants `relay.proxy`, and refused for any
node that relay has not claimed or that is connected to the upstream
directly.

From upstream the segment is ordinary. `manage.up` lists the relayed
nodes, targeting matches them, a job goes down the relay's stream naming
the node it is for, and the return is filed against the node that ran
it. Depth is capped at two, because unbounded nesting is how a syndic
estate becomes undebuggable.

Two things the syndic does not do. Returns are spooled durably while the
upstream is unreachable and drained oldest-first when it returns, so an
outage delays returns rather than losing them; and event forwarding is
by tag glob — `relay_event_tags` — so a busy segment can send its job
returns upstream and keep its beacon chatter local. Empty forwards
nothing, which is the default.

Verified as three processes on one machine, and
[DIVERGENCE 5.14](docs/DIVERGENCE.md) records the six defects that
found, all of which the tests had passed: a relay that panicked before
it connected, relayed nodes that no job could target, returns refused
because the relay never recorded the job it forwarded, a spool discarded
wholesale on every reconnection, one refused entry blocking every return
behind it, and relayed returns tagged with the relay instead of the node
— which made a reactor upstream watching for its own node fire never.
DIVERGENCE 1.8 and 1.9 record what a relay deliberately does not
forward, and what the upstream is trusting it for.

### A FIPS artifact set, and what it refuses

`make fips` builds `halite-hub-fips`, `halite-node-fips`, and
`halite-api-fips` against the certified Go Cryptographic Module of SPEC
27.4, and `make fips-cross` is the release set for the tier 1 platforms.
The build asks each artifact what module it carries and refuses to
finish if the answer is wrong — `GOFIPS140` is an environment variable,
and a build that loses it produces a working binary with the right name
and the wrong cryptography.

What a binary is, is read from the module at runtime rather than stamped
at link time, so `version` reports what the process is doing rather than
what its builder intended:

```
halite-node v1.0.0+abc123def456 (fips v1.0.0)
fips mode on, module v1.0.0, self-tests passed
```

In FIPS mode Ed25519 is refused by name, TLS key exchange is P-256 or
P-384, and TOTP is refused because RFC 6238 is HMAC-SHA-1. The TOTP
refusal fails closed — the account still declares it needs a second
factor, so a password alone is never enough — and `halite-api` names the
accounts it locks out at startup rather than leaving it to be found at a
login prompt.

`make check` now runs the whole suite twice, once as an ordinary build
and once as a FIPS one. Running it that way the first time found three
tests that assumed SHA-1 and Ed25519 were always available, which is the
assumption any caller would have made.

[DIVERGENCE 5.15](docs/DIVERGENCE.md) records what the lab established,
including the key exchange measured from outside with OpenSSL rather
than read back from the configuration this build sets. 1.10 records the
substantive divergence: `GODEBUG=fips140=on`, which SPEC 27.4 has the
service unit set, does not enforce anything — HMAC-SHA-1 computes
happily under it — so the restrictions are this build's own rather than
the setting's. Under `fips140=only` the module panics instead of
returning an error, which is why the TOTP refusal is load-bearing: an
unguarded second factor took the login handler down.

### A worked authorization policy, and three functions a wildcard was granting

`contrib/examples/policy.yaml` is a commented policy covering the shapes
an estate actually needs: a role scoped to a target pattern with
argument constraints, a read-only role, a runner-only deployer, an
administrator, a break-glass role nothing is bound to, and a relay. It
is loaded by the policy parser in a test with the decisions its comments
describe asserted, so it cannot teach a grant that does not exist.

Writing it found that `cmd.shell`, `file.write`, and `file.replace` were
not declaring `arbitrary_code`, so `functions: ['*']` granted all three
— three of the six functions SPEC 23.5 names as never granted by a
wildcard. `cmd.shell` is the one that mattered: an estate that
deliberately withheld `cmd.run` from a role had handed over a shell
anyway, while the log and `policy show` both reported the control as
enforced.

Found by asking `halite-hub policy test` what it decided rather than by
reading the policy. [DIVERGENCE 5.16](docs/DIVERGENCE.md) has the
detail, and the list is now checked against SPEC's own names.

### What is not built

The rest of phase 5, and phase 6. No Windows or macOS parity, no
detached job signing, no signed state trees, and no backtracking regex
engine. `doctor`, which SPEC 27.4 gives the FIPS mismatch warning to, is
not built either.

Two things inside phase 2 are still absent: `halite-hub files`, the push
in the other direction from `salt-cp`, and external pillar. Phase 3's
gaps are listed in [DIVERGENCE 6.1](docs/DIVERGENCE.md): beacons and
schedules delivered through pillar, node-initiated execution on another
node, `inotify`, most of the beacon inventory, and a node's own local
reactor.

A subcommand whose phase has not landed reports that by name, which is
deliberate: the alternative is a program that appears to work.

FreeBSD is the platform this is verified on. `make test-linux`
cross-compiles the suite and runs it under this host's Linux compat
layer — 23 of 25 packages pass, and the `/proc` grain collector returns
the same 63 keys as the FreeBSD one with every hardware fact agreeing —
but that layer has no apt, dnf, systemctl, or useradd, so the providers
that need them, 60 of the 62 platform modules of SPEC section 15.3, and
macOS and Windows entirely, remain compiled and unexercised.

The filesystem layout follows the platform rather than SPEC 27.3's FHS
paths: a BSD keeps configuration under `/usr/local/etc` and durable
state in `/var/db`, and following the text literally put files in three
places no BSD administrator looks. DIVERGENCE 1.5 records it.

[docs/DIVERGENCE.md](docs/DIVERGENCE.md) is the full accounting, and it
is checked against the code by `internal/specaudit` rather than
maintained by hand.
