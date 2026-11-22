# Proposal: a node verifies signed state content before it applies it (issue 764)

**Status: a design, not built.** Written 2026-11-22 against `main` at
`d0d5925c`. Every statement below about what the code does today cites the
file and line it was read at, at that commit. Nothing in this document has
been run; section 11 lists what was not verified, and it is most of what
matters.

Nothing holds this file to the code. `internal/docsaudit` reads only
`docs/*.md` (`TestDocumentationLinksResolve` and
`TestEveryDocPageIsLinkedFromTheReadme`,
`internal/docsaudit/docsaudit_test.go:157-165` and `:200-212`), the same as
for `docs/audits/`, so this page drifts as soon as work lands. When a part of
it is built, the ledger entry for that part is the record, not this page.

**Confidence: 3/5.** The node-side verification is a well-trodden shape
(a signed, content-addressed manifest with a rollback high-water mark, as in
TUF or a package repository's signed index) and fits the code's single choke
point for tree reads. The parts that depend on context I could not see are
the signer's workflow (no CI pipeline for the estate tree was read), whether
a gitfs-served tree and a signer's own listing of the same commit agree
byte for byte (section 8 says how to find out), and how often the estate
would have to re-sign.

## 1. The problem, and who the attacker is

SPEC 25.1's threat model lists "optional signed state trees" as a control
against a malicious or compromised state tree (`SPEC.md:2482`), and phase 6
names "signed state trees" as delivery content (`SPEC.md:2977`). Neither is
built. `plan.md:1032-1034` already warns not to mistake gitfs ref
verification for it.

| Attacker | Controls | What this design must still guarantee |
|---|---|---|
| **A1, a compromised hub, or a compromised relay hub** | Everything the hub process can do: what `/v1/files` returns, the digest it publishes beside each file, the listing, which environment a job names, the pillar, which jobs are dispatched (unless SPEC 25.6 signing stops it) | A node that requires signed state content applies only files a trusted signer published for that environment, no older than the newest revision that node has already accepted, and all from one revision within a run |
| **A2, someone who can push to the git remote** | The tree's content, and the refs | The same: the push is not signed by a trusted signer, so it is not applied. (gitfs ref verification already stops A2 at an honest hub; this stops it at the node too) |
| **A3, a network attacker** | Bytes on the wire | Already out: TLS 1.3 with mutual authentication (SPEC 25.1, `SPEC.md:2478`) |
| **A4, root on the node** | The node | Out of scope, as everywhere: it can change the node's configuration |
| **A5, the signer itself** | The signing key | Out of scope by definition. Mitigated, not solved, by keeping the key in a KMS (the job signer already works that way, DIVERGENCE 5.276) so that every signature is an audit record |

**What this does not deliver, and the reader should not infer:**

- **Pillar is outside it.** Pillar is compiled by the hub per node and sent
  to it; a node never sees pillar sources. A1 controls pillar, so a signed
  state that interpolates a pillar value into `cmd.run`, a file's contents
  or a package name is still steerable by a compromised hub. Signed state
  content narrows what a hub can make a node do to "what a signed tree does
  with data the hub supplies". The state author has to treat pillar as
  untrusted input for the guarantee to mean much (open question 10.5).
- **Which job runs is SPEC 25.6's question, not this one.** A signed job
  binds the job's identifier, target, function, arguments, environment and
  expiry (`internal/job/model.go:398-409`) and says nothing about the content
  a `state.apply` will read. Without signing jobs too, A1 can still choose
  *when* a signed tree is applied, and to which environment. Section 3.9
  binds the two together optionally.
- **Agentless mode** (`halite-hub ssh`) is out, for the reason the hardening
  chapter gives for job signatures (`docs/handbook/28-hardening.md:113-115`):
  the caller already has a shell there.

## 2. What happens today

### 2.1 GitFS verification happens on the hub, for the hub

- `gitfs_verify_signatures` runs `git verify-commit` or `git verify-tag`
  with `GNUPGHOME` set to `gitfs_keyring`, against the ref's full object id
  (`internal/gitfs/git.go:207-248`). A ref that fails is not served
  (`internal/gitfs/backend.go:161-170`), and a kept tree is verified again
  when its remote serves nothing new (`backend.go:303` and the comment above
  it).
- A served environment can be the union of several remotes' trees, the
  first listed shadowing later ones (`backend.go:226-234`).
- The tree is materialised with `git archive` into a directory named by the
  commit (`git.go:252-298`, the archive call at `:282-290`).

That check is the hub's. Against A1 it is the attacker's own check. SPEC
25.1's compromised-hub row lists "signed gitfs refs" among the controls
(`SPEC.md:2481`), and the hardening chapter's table does the same
(`docs/handbook/28-hardening.md:19`). A hub that has been taken over is not
constrained by a verification it performs itself, so that cell overstates
what is built. Open question 10.1 asks whether to correct it now or with
this feature.

### 2.2 The node trusts the hub's digest for each file

- A node's view of the hub's tree is `fileserver.Remote`, built by
  `fileserver.NewRemote` (`internal/fileserver/remote.go:147-149`) in
  `useHubTree` (`cmd/halite-node/enroll.go:722-741`). There is no
  `cmd/halite-node/hublink.go`; the wiring is in `enroll.go`.
- Every file read goes through `Remote.cacheFile`
  (`remote.go:263-338`): the digest of any cached copy goes up as the entity
  tag, and a new body is checked against the digest **the hub publishes in
  the same response** before it is moved into the cache (`:316-329`). That
  is integrity in transit against the hub's word. It is not authenticity: A1
  publishes a digest for whatever it sends.
- The listing comes from the hub too (`ManifestFor`, `remote.go:368-395`).
  The manifest type has an environment, a prefix, a hash type, the files and
  a truncation flag (`internal/fileserver/manifest.go:25-31`), and no
  revision and no signature. A listing is bounded at 50,000 entries
  (`manifest.go:34`).
- Nothing pins a run to one revision. Each file is fetched when first asked
  for, under its path. A tree that changes on the hub in the middle of a
  highstate would, by this reading, give that run files from two revisions.
  I have not demonstrated it.

### 2.3 Everything that consumes the tree on a node

All of it reaches `cacheFile` or `ManifestFor`:

| Consumer | Path to the tree |
|---|---|
| SLS sources | `Remote.Source` (`remote.go:178-192`), which tries candidate names (`foo.sls`, `foo/init.sls`) |
| Template `include` / `import` | `Remote.Templates` (`remote.go:195-212`) |
| `salt://` sources of `file.managed` and friends | `Remote.Fetch`, `Hash`, `Exists` (`remote.go:225-253`) |
| `archive.extracted` | `c.Files.Fetch` (`internal/builtin/archive.go:601-605`); the format is then chosen by the **local path's suffix** (`archive.go:137`) |
| `cmd.script` | an `ArbitraryCode` function (`internal/builtin/cmd_more.go:466`) that fetches the script from the tree |
| `file.recurse` and other recursive layouts | `Remote.ListUnder` (`remote.go:530-537`) |
| Extension bundles under `_ext/` | `Remote.ListPrefix` (`remote.go:508-521`) via `extensionSource` (`cmd/halite-node/extensions.go:290-297`) |

Exec functions reach the tree too (`cmd.script`, `cp.*`), not only state
runs, so verification cannot live only at the start of a state run.

### 2.4 The render sandbox never reads the tree itself

With `render_sandbox` on (`internal/config/keys.go:99`), the child process
asks its parent for every template by name (`frameLoad`,
`internal/rendersandbox/frame.go:45-46`, sent from `child.go:170`), and the
parent answers from its own loader (`internal/rendersandbox/sandbox.go:517-522`).
Module calls cross the same way (`frameCall`, `frame.go:47-49`). So a check
in the parent's loader covers sandboxed rendering without any change to the
sandbox, and the sandbox does not need the signer keys.

### 2.5 Offline, fallback and the last known good pillar

- When the hub cannot be reached, a cached copy is served
  (`remote.go:284-297`), and a saved whole listing answers for the tree
  (`remote.go:387-393`, `:420-446`). That is a last known good tree, keyed by
  path, with no notion of which revision it was.
- When a hub answers but does not serve files at startup, the node
  **applies its own local tree** (`enroll.go:729-734`).
- The last known good pillar is a separate store, encrypted under a key
  derived from the node's own key (`cmd/halite-node/pillarstore.go:37-58`;
  SPEC 25.3, `SPEC.md:2515`). Nothing here changes it.

### 2.6 Relays

A relay is a hub that serves its own nodes (`cmd/halite-hub/relay.go:24-102`).
Its nodes fetch files from the relay hub's own file server: I found no code
that forwards `/v1/files` upstream. A search of `internal/relay`,
`internal/hub/files.go` and `cmd/halite-hub` for `FetchFile` and
`FileManifest`, and of `internal/hub/files.go` for `relay` and `upstream`,
returned nothing, and the relay's message handling covers job, ping, event,
revoke, reload, quiesce and drain messages only (`internal/relay/relay.go`,
the `case transport.Msg…` lines). SPEC 5.3 says a relay "proxies jobs,
returns, events, and file requests" (`SPEC.md:338`), and the comment at
`cmd/halite-hub/relay.go:26-28` says the same. That pair disagrees. I read
it and did not run it, and it is reported separately rather than fixed here.
For this design the consequence is good: a node checks the manifest
whichever hub served it, so a relay is an A1 like any other.

### 2.7 What already exists to reuse

- **ECDSA job signing**: P-256 or P-384, ASN.1 DER, SHA-256 digest
  (`internal/jobsign/jobsign.go:14-26`), trust lines `<name> <base64
  SubjectPublicKeyInfo>` (`ParseSignerKey`, `jobsign.go:316-353`), every
  trusted key tried in turn so that a signature cannot name its own checker
  (`Verify`, `jobsign.go:475-518`), and `Digest` exported so that a bridged
  KMS signer signs the same 32 bytes (`jobsign.go:433-443`). The bridged KMS
  signer refuses a digest that is not 32 bytes (DIVERGENCE 5.276).
- **Domain separation was anticipated**: the job signature's domain exists
  "so that a signature over an extension bundle, a token or a state tree can
  never be replayed as a signature over a job" (`jobsign.go:68-71`).
- **Extensions use Ed25519** (`internal/extension/sign.go`), and DIVERGENCE
  5.128 records why jobs use ECDSA instead. A FIPS build has no Ed25519
  (DIVERGENCE 1.10, `docs/DIVERGENCE.md:285-289`), and this estate has a
  FIPS node (`ref-salt1`). So ECDSA, not Ed25519.
- **A durable replay record** that is written and synced before a signed job
  can run (`cmd/halite-node/execute.go:479-488`).

### 2.8 Two traps for whoever builds it

`adoptJobEnvironment` type-asserts `n.files` to `*fileserver.Remote`
(`cmd/halite-node/execute.go:220`), and `extensionSource` does the same
(`extensions.go:292`). A wrapper around `n.files` fails those assertions.
The node already wraps it for tracing (`traceFiles`,
`cmd/halite-node/trace.go:230-246`) at `execute.go:55`, before
`adoptJobEnvironment` runs at `execute.go:87`. By that reading, a traced
job naming another environment falls into the `else` branch and switches the
node to its local roots (`execute.go:224-226`). I have not run it. Either
way, a verifying wrapper must not depend on those assertions. It should
reach the hub's tree through `n.hubRemote` (`cmd/halite-node/main.go:271`)
or an interface, or the node will quietly stop verifying the first time a
job names a second environment.

## 3. The design

### 3.1 In one paragraph

A signer outside the hub lists an environment's tree, writes a manifest
naming every file with its digest, the environment, and when it was issued,
and signs the manifest's bytes with an ECDSA key the hub does not hold. The
manifest and its signature are served as two ordinary files under a reserved
directory of that environment. A node with `require_state_signature` fetches
them first. It verifies the signature against keys in its own
configuration, refuses a manifest older than the newest it has accepted for
that environment, and records the one it accepted before anything else
happens. Then, for the rest of the run, it answers every read and every
listing of that environment from the manifest. A path the manifest does not
name does not exist. A body whose digest differs is refused, whatever the
hub said about it.

### 3.2 The manifest

A JSON document at `_halite/manifest.json` in each environment it covers.
Proposed schema `halite.tree/1`:

```json
{
  "schema": "halite.tree/1",
  "env": "base",
  "issued": "2026-11-22T18:04:05.123456789Z",
  "expires": "2026-12-22T18:04:05Z",
  "source": "commit 3f1c0c2e9b5d7a4f0c1e2d3b4a5968776655aa01 refs/heads/production",
  "hash_type": "sha256",
  "files": [
    {"path": "nginx/init.sls", "hash": "9f86d0…", "size": 412},
    {"path": "nginx/nginx.conf.j2", "hash": "60303a…", "size": 1880},
    {"path": "top.sls", "hash": "fd61a0…", "size": 233}
  ]
}
```

- **The revision is the manifest itself.** Its identity is the SHA-256 of
  its exact bytes, which is immutable by construction and does not depend on
  git's object ids (SHA-1 for most repositories; `git.go:177-188` accepts
  both lengths). `source` is what the signer says it built from. It is
  signed and shown to an operator, and nothing decides on it.
- **`issued`** orders revisions of one environment. RFC 3339 in UTC with
  nanoseconds, the way a signed job's expiry is spelt (`jobsign.go:164-175`).
  A timestamp rather than a counter, so that a signer needs no state between
  runs. A CI runner's clock becomes the trust root for ordering; open
  question 10.2.
- **`expires`** is optional. With it, a node refuses the manifest after that
  instant, which bounds how long A1 can freeze a node on an old revision by
  withholding the new one (3.8).
- **`hash_type`** is one of `sha256`, `sha384`, `sha512`, `sha3-256`, the set
  `hash_type` already allows (`internal/config/keys.go:151`). MD5 and SHA-1
  are refused by name. The node verifies with the manifest's hash type,
  whatever its own `hash_type` says.
- **`files`** has the shape `fileserver.Entry` already has
  (`manifest.go:12-17`), sorted by path, with no duplicates. A path is
  relative, slash-separated, has no empty, `.` or `..` segment, no
  backslash, no NUL, and does not start with `_halite/`. The node refuses a
  manifest that breaks any of these rather than normalising it. At most
  50,000 entries (`MaxManifestEntries`, `manifest.go:34`): a tree larger
  than that cannot be signed in this version, and `truncated` does not exist
  in this schema.
- **Unknown fields are refused**, and so is an unknown `schema`. A field
  this build does not understand could be one that narrows what the manifest
  authorises, and ignoring it would widen the authorisation.
- **Not in it, deliberately:** file modes, owners and timestamps. I found
  nothing on a node that takes these from the tree. If a future state does
  (Salt's `mode: keep`), the manifest has to grow a `mode`, and that is a
  new schema version (open question 10.10).

### 3.3 The signature

`_halite/manifest.sig` holds one line: base64 of an ASN.1 DER ECDSA
signature over

```
digest = SHA-256( "halite-tree-v1\x00" || <the exact bytes of manifest.json> )
```

- **Over the bytes, not a canonical re-encoding.** The node verifies the
  bytes it received and only then parses them. There is no canonicaliser to
  get wrong at either end; the job signature's v1-to-v2 history
  (`jobsign.go:73-81`) is the reason not to need one.
- **Its own domain**, distinct from `halite-job-v2\x00` (`jobsign.go:81`), so
  neither signature can be replayed as the other. Open question 10.4 asks
  whether to use a separate key as well, as the KMS job signer does for the
  release key (DIVERGENCE 5.276).
- **The digest is 32 bytes**, so the existing bridged signer
  (`--sign-extension`, `internal/extsigner`) and `halite-ext-signer-kms` sign
  a manifest without change. A P-384 key signs a SHA-256 digest, as it does
  for jobs.
- **One signature.** A node accepts the manifest if any key it trusts
  verifies it, which is `jobsign.Verify`'s rule. Requiring two signers is
  open question 10.6.

### 3.4 Where it lives, and why there

In the tree, under `_halite/`, beside the content it describes. The hub
needs no change to serve two more files. It works the same for gitfs,
`file_roots` and a relay. Content and manifest arrive together from gitfs,
because one commit holds both and a commit is materialised into its own
directory and renamed into place (`git.go:258-298`). The precedent is
`_ext/`, where signed extension bundles are published into the tree
(`cmd/halite-hub/extensioncmd.go:20-25`).

The cost is a loop for git: the manifest cannot describe the commit that
contains it, so it describes everything **except** `_halite/`, and the
signer commits it on top of the content (section 5). Section 4 lists the
alternatives to that.

### 3.5 Trust configuration on the node

All names below are proposed, not built.

| Key | Values | Meaning |
|---|---|---|
| `require_state_signature` | `false` (default), `warn`, `true` | `true`: refuse to compile from, or read, an environment without a valid manifest. `warn`: verify everything, log and count every refusal `true` would have made, and go on as today. A value this build cannot parse **stops the node at start**, for `require_job_signature`'s reason (`cmd/halite-node/jobsignature.go:45-50`) |
| `state_signer_keys` | list of `<name> <base64 SubjectPublicKeyInfo>` | The keys whose manifests this node accepts. The same line format as `job_signer_keys` (`jobsign.go:324-330`). A line that will not parse stops the node at start, as a bad `job_signer_keys` line does (`jobsignature.go:171-187`) |
| `state_manifest_max_age` | duration, default `0` (no limit) | Refuse a manifest issued longer ago than this, even without `expires`. A freeze bound for estates that re-sign on a schedule |
| `state_signature_local` | `true` (default), `false` | Whether the requirement also covers the node's own `file_roots`: the `--local` path and the startup fallback (`enroll.go:729-734`). See 3.10 |

**An empty `state_signer_keys` with the requirement on is not fatal.** The
node starts, refuses every state run and every tree read with a message
naming the setting, and still answers exec jobs. This follows the job
signature's behaviour (`jobsignature.go:204-211`) and the project's rule that
a node that cannot boot cannot be fixed: where this node's configuration is
itself managed by the tree, a fatal setting is one only a person at the
console could repair.

The keys are node configuration, not tree content and not anything the hub
sends, as SPEC 25.6 says of job signer keys (`SPEC.md:2556-2557`).

### 3.6 Verification, in order

Per environment, at the start of each state run, and lazily on the first
tree read in an exec job:

1. **Fetch** `_halite/manifest.sig` and `_halite/manifest.json` with the
   transport call `cacheFile` already uses (`FetchFile`), bypassing the
   manifest check (there is none yet). Bounded: 16 MiB for the manifest
   (50,000 entries of a few hundred bytes), 1 KiB for the signature. The
   hub's published digest for these two files is not used for trust.
2. **Verify the signature** against every key in `state_signer_keys`. None
   verifies: refuse.
3. **Parse strictly** (3.2): schema, unknown fields, path rules, sorted and
   unique, hash type.
4. **The environment must match.** `env` must equal the environment being
   read, or A1 could serve the `dev` environment's validly signed manifest
   and content as `base`.
5. **Expiry and age.** Refuse if `expires` has passed, or if
   `state_manifest_max_age` is set and `issued` is older than that. A minute
   of skew is tolerated, as for signed jobs (`jobsignature.go:239-243`).
6. **Not stale.** Compare with this environment's high-water record (3.7):
   - `issued` earlier than the record: refuse (a rollback).
   - `issued` equal and the same manifest digest: accept (the common case,
     an unchanged tree).
   - `issued` equal and a different digest: refuse, and raise an event. Two
     different revisions signed for the same instant mean a signer
     misbehaving or a key in two hands.
   - `issued` later: accept.
7. **A pin, if the job carries one** (3.9): the manifest digest must equal
   it.
8. **Record** the accepted manifest as the new high-water mark
   (`<state_dir>/tree-revisions/<env>.json`: `issued`, digest, signer, time
   accepted), written atomically and synced **before** any file of the
   revision is read. The signed-job replay record follows the same
   discipline for the same reason (`execute.go:479-488`).
9. **Answer every read from it** for the rest of the run (3.7).

Steps 2 to 7 are pure functions of bytes, keys, a clock and the stored
record, so each can be tested without a hub.

### 3.7 Using an accepted manifest during a run

- **One revision per run.** The manifest accepted at step 8 is pinned to the
  run. A tree that changes on the hub mid-run makes the run fail at the first
  changed file ("the hub now serves X with digest A; this run's revision R
  says B") instead of mixing two revisions.
- **Reads.** A path not in the manifest is not found. `Source`'s candidate
  search (`remote.go:183-191`) asks the manifest, not the hub, so A1 cannot
  answer for a name the signer never published. A path that is in it is
  fetched as now, with the manifest's digest as the entity tag, and the body
  must hash to the manifest's digest before it is kept. The hub's published
  digest is used only in the diagnostic.
- **Listings.** `ManifestFor`, `List`, `ListUnder` and `ListPrefix` answer
  from the signed manifest and never from the hub's listing, so the hub can
  neither add a file to a `file.recurse` nor hide one from it.
- **A content-addressed cache for signed revisions.** Verified bodies are
  kept at `<cache_dir>/objects/<hash_type>/<first two hex>/<digest>/<basename>`,
  read-only, mode 0600, and `Fetch` returns that path. Three reasons:
  1. Two runs at different revisions cannot overwrite each other's file
     between the check and the use. State runs are exclusive
     (`cmd/halite-node/jobqueue.go:30-34`), but exec jobs can run alongside
     each other under `parallel_jobs` (`jobqueue.go:35-37`), and
     `cmd.script` and `cp.*` read the tree.
  2. An outage can apply the last accepted revision whole, if its objects
     are present, rather than whatever mixture the path cache holds.
  3. Going back to an earlier revision costs no transfer.

  The basename stays at the end because `archive.extracted` picks the format
  from the local path's suffix (`archive.go:137`); a digest-only name would
  make every archive look like a tarball. Objects that neither the current
  nor the previous accepted manifest names are removed after a successful
  run.

### 3.8 Missing, changed, stale and frozen content

| Case | `true` does | `warn` does |
|---|---|---|
| No manifest, or no signature | Refuse the run, naming the environment and the two paths | Log, count, apply as today |
| Signature verifies against no trusted key | Refuse | Log, count, apply |
| Manifest for another environment | Refuse | Log, count, apply |
| Expired, or older than `state_manifest_max_age` | Refuse | Log, count, apply |
| Older than the high-water mark | Refuse | Log, count, apply |
| Same `issued`, different digest | Refuse, raise an event | Log, count, raise the event, apply |
| A file's body differs from the manifest | Refuse the read (the state fails) | Log, count, use the body |
| A path not in the manifest | Not found (the state fails as for a missing file) | Log, count, fetch as today |
| Hub unreachable, every object of the accepted revision cached | Apply that revision, warning once per outage as today (`remote.go:92-102`) | The same |
| Hub unreachable, some object missing | Refuse | As today |

**Freezing.** A1 can keep serving the newest manifest a node has accepted
and withhold its successor, and the node cannot tell that from a quiet
week. `expires` and `state_manifest_max_age` turn a long freeze into a
refusal, at the cost of re-signing on a schedule (a CI job that re-signs the
same content with a new `issued`). Without either, the defence is
observation: the node reports the `issued` of its accepted revision (3.11),
and an alert on its age is an alert on absence.

### 3.9 Binding a revision to a signed job (optional, phase 2)

`state.apply`, `state.sls` and `state.highstate` gain a keyword argument,
proposed `tree_revision: sha256:<manifest digest>`. Keyword arguments are
inside the job signature (`internal/job/model.go:405`), so on a signed job A1
cannot change it, and step 7 refuses any other revision. "Run *this*
revision, now, on these nodes" becomes one signed instruction. The operator
takes the digest from their own checkout (`halite-hub tree manifest`,
section 5), never from the hub, because a hub that has been taken over would
hand out the digest of its own manifest.

The three functions' signatures do not accept such a parameter today; I did
not check what adding one involves.

### 3.10 How it meets the rest of the system

- **GitFS ref verification** stays, and is independent. It stops A2 at an
  honest hub; this stops A1 and A2 at the node. With both on, the CI that
  writes the manifest commit also signs that commit with GnuPG, so an honest
  hub serves only signed commits and a node applies only signed manifests.
  Neither replaces the other.
- **The file server** needs no change for version one: `_halite/` is served
  like any other path. An operator's `file_ignore_glob` or
  `file_ignore_regex` (`internal/fileserver/roots.go:48-53`) must not hide
  it. A hub-side `halite-hub tree verify` (section 5) gives early warning,
  needing only public keys and changing nothing.
- **Several remotes in one environment** (`backend.go:226-234`). The
  manifest describes the environment as served, so the signer has to list
  the same union in the same order. A file served from a remote the manifest
  does not cover is "not in the signed revision" and fails, closed and
  loudly. Recommended for version one: one remote per signed environment.
- **The render sandbox** needs nothing (2.4).
- **Pillar and the last known good pillar** are unchanged, and outside the
  guarantee (section 1).
- **Offline nodes** apply the last accepted revision if all its objects are
  cached (3.8). A node that has never accepted a manifest for an environment
  cannot apply that environment offline under `true`.
- **Relays** need nothing. A relay serving the same repository serves the
  same manifest; a relay serving something else fails at the node, which is
  correct.
- **Extensions under `_ext/`** are covered by the manifest as files, in
  addition to their own Ed25519 bundle signatures, not instead of them.
- **The node's own roots.** With `state_signature_local: true`, the `--local`
  path, which compiles from the node's own roots (`cmd/halite-node/main.go:464`), and the startup fallback
  (`enroll.go:729-734`) read `_halite/manifest.json` from the local root and
  verify it the same way. Without that, an unreachable file server at
  startup would be a way round the requirement. A4 can turn the setting off,
  so the local case is about one rule everywhere rather than security
  against A4.
- **The node's own schedule** queues its jobs on the same executor (the `local` field,
  `cmd/halite-node/execute.go:395-398`), and every module is handed `n.files` through its context (`cmd/halite-node/main.go:765`), so it is covered. Agentless mode builds its own tree from what the hub sent (`cmd/halite-node/oneshot.go:343-350`) and is out of scope (section 1).
- **Top files from several environments.** A highstate can read top files
  from more than one environment. Each environment's manifest is verified on
  its first read in the run, and each has its own high-water mark.

### 3.11 What the node reports

Names proposed:

- A log line at `warn` for every refusal, with `env`, `reason`,
  `manifest_issued`, `manifest_digest`, `signer` when one verified, and the
  path for a per-file refusal. Examples:
  - `the hub serves no signed manifest for environment base (_halite/manifest.json); this node requires one (require_state_signature) and applied nothing`
  - `the manifest for base was issued 2026-11-01T09:00:00Z and this node has already accepted one issued 2026-11-20T18:04:05Z; refusing an older revision`
  - `nginx/nginx.conf.j2 arrived with sha256 60303a… and revision 3c9e… (issued 2026-11-22T18:04:05Z, signed by tree-ci) says 1d2f…; it was not used`
- At `info`, every accepted change of revision: `env`, old and new
  `issued`, digest, signer.
- Metrics: `halite_node_tree_refusals_total{reason}` and
  `halite_node_tree_revision_issued_timestamp_seconds{env}`, the second
  being the one to alert on, for its age and for its absence.
- Events: `halite/node/<id>/tree/refused` with the reason, and
  `halite/node/<id>/tree/equivocation` for the same-`issued`,
  different-digest case.
- Evidence (SPEC 25.7): each state run's result record carries the manifest
  digest and the signer's name, so the node's own chain says which signed
  revision it applied. That changes the evidence record's shape, which needs
  its own schema handling, not designed here.
- `doctor`: the requirement, the number of trusted keys, and the accepted
  revision per environment.

## 4. Alternatives considered

| Alternative | Why not, for now |
|---|---|
| **The manifest committed in a separate repository, merged into the environment as a second remote** | No commit loop on the content repository. But gitfs updates remotes independently, so for a while after each deploy content and manifest disagree and nodes fail closed. An alarm on every deploy is one nobody keeps reading. It falls out of 3.10 as a supported layout and is not recommended |
| **A new hub endpoint a signer uploads manifests to** | A write path into the file server through the API is the class SPEC 25.2 records (CVE-2021-25281, `SPEC.md:2496`), and the hub would need storage for it. The in-tree file needs neither |
| **Verify the commit's GnuPG signature on the node** | The node would need the repository's objects, a git binary and GnuPG, and would check SHA-1 tree hashes for most repositories. It would not cover `file_roots`, or a relay serving another layout |
| **Only pin the manifest digest in signed jobs (3.9), without a manifest signature** | Covers signed jobs only: the node's own schedule, unsigned jobs and exec jobs reading the tree are not covered. Kept as phase 2, on top of the signature |
| **A hub-held key that signs what the hub serves** | Gives A1 the key. It is the objection DIVERGENCE 5.169 makes to a hub signing its own orchestration steps (`docs/DIVERGENCE.md:16630-16637`) |
| **Ed25519, as for extensions** | Not available in a FIPS build (2.7), and not what the working KMS signer offers (DIVERGENCE 5.276) |
| **A canonical JSON encoding instead of signing bytes** | A canonicaliser at both ends is two things to keep in step; the job signature's v1 shows the cost (`jobsign.go:73-81`) |
| **Per-file signatures** | Thousands of signatures per tree, no single revision, and no defence against a hub mixing files from two signed revisions |

## 5. The operator's workflow

All commands are proposed; none exists. `halite-hub tree` runs without a hub
or hub configuration, so it can run in CI.

```sh
# Once: a key, kept off the hub. The existing command, reused.
halite-hub keys signer create tree-ci --out signer/tree-ci
#   or a KMS key behind halite-ext-signer-kms, as for jobs

# In CI, on every change to the branch the estate serves:
halite-hub tree manifest --repo . --commit "$COMMIT" --env base \
    --ignore-glob '*.swp' > _halite/manifest.json
halite-hub tree sign _halite/manifest.json \
    --sign-extension ./halite-ext-signer-kms > _halite/manifest.sig
halite-hub tree verify --dir . --env base --keys trusted-tree-keys.txt
#   then commit _halite/ on top of $COMMIT, signed, and push it to the
#   branch the hub serves

# On a node:
#   require_state_signature: warn        first, then true
#   state_signer_keys:
#     - "tree-ci MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE…"

# On the hub, optionally, to find a broken signature before the nodes do:
halite-hub tree verify --env base --keys trusted-tree-keys.txt
```

- `tree manifest` builds the listing with `fileserver.Roots.Manifest`
  (`manifest.go:44-95`), the function the hub serves listings from, so the
  hub and the signer share one listing rule, symlink containment included.
  With `--repo` and `--commit` it materialises the commit with `git archive`
  as gitfs does (`git.go:282-290`): a checkout can differ from an archive
  (`export-ignore` attributes, for one), and the node sees what the hub
  materialised.
- The ignore patterns given to `tree manifest` must be the hub's
  `file_ignore_glob` and `file_ignore_regex`. Otherwise the manifest names a
  file the hub hides, and a recursive state that lays it out fails.
- **A rollback is a new signature over old content**: sign the old commit's
  tree with a new `issued` and push. The high-water mark forbids going back
  without one, which is the point.
- **Rolling a revision out in stages** is what environments already do: sign
  `staging`, let it apply, sign `production`. With 3.9, a signed
  `state.apply` pinned to a revision rolls a revision out by target.
- **Rotating a key**: add the new key to `state_signer_keys` everywhere
  (through a state the old key signed), switch CI to the new key, then remove
  the old one.

## 6. Failure modes

| Failure | What the operator sees | Recovery |
|---|---|---|
| CI forgot to sign, or content was pushed without the manifest commit | Every node in `true` refuses, with the "digest differs" or "not in the signed revision" message, and `halite_node_tree_refusals_total` rises | Sign and push. Nothing was applied |
| A hub with `file_ignore_glob: '_*'` | "the hub serves no signed manifest" on every node | Exempt `_halite/` |
| A signer clock behind the last `issued` | "older revision" on every node, after a good signing run | Fix the clock and re-sign (open question 10.2) |
| A key withdrawn from `state_signer_keys` | Its manifests are refused at the next run; the high-water record stays | Sign with a trusted key |
| An outage longer than `state_manifest_max_age` | The node stops applying the tree it has, saying why | The trade-off of setting it; leave it at `0` where outages matter more than freezes |
| The high-water record lost (state directory wiped) | The node accepts any validly signed manifest again, an old one included, once | Inherent; re-enrolment is the same event |
| A manifest over 16 MiB or 50,000 entries | Refused, naming the bound | Split environments |

## 7. Migration and rollout

1. Build the signer side (`halite-hub tree manifest|sign|verify`) and the
   node's verification behind `require_state_signature: false`. A node
   without the setting behaves exactly as today: no wire change, no hub
   change, no job record change.
2. In CI, sign every environment the estate serves, and run `tree verify`
   against the hub to see that what it serves matches.
3. Set `warn` on one FreeBSD node and one Linux node, and watch
   `halite_node_tree_refusals_total` over a week of real highstates. Every
   count is something `true` would have refused.
4. Set `true` on those, then on the rest. FreeBSD carries most of the
   estate, so the FreeBSD leg is the one that has to be shown working.
5. Phase 2: `tree_revision` on signed state jobs (3.9).

A node older than the feature never asks for `_halite/`, and a hub older
than it serves those files without knowing what they are, so the steps can
happen in any order across builds.

## 8. Test plan

**Unit, each broken on purpose and seen to fail**

- Signature: verifies with P-256 and P-384; fails with a flipped byte in
  either file; fails under the job domain, and a job signature fails under
  the tree domain. A signature made by `openssl dgst -sha256 -sign` over the
  domain-prefixed bytes verifies, so the format is checked against a tool
  other than its own producer, as the job format is.
- Parsing: each path rule, unsorted and duplicate entries, an unknown field,
  an unknown schema, `md5` and `sha1`.
- Order and staleness: older, equal-same, equal-different (the event fires),
  newer; `expires` and `state_manifest_max_age` either side of the minute of
  skew; the record synced before the first read, and a process killed
  between the two restarting on the new mark.
- Reads: a path not in the manifest is not found **even though the hub
  serves it**; a body the hub serves with a matching *hub* digest and a
  different *manifest* digest is refused; a listing never includes a file
  the hub adds; `Source`'s candidate search asks only the manifest.
- Mid-run change: the hub's tree changes between two reads of one run, and
  the run fails at the changed file rather than applying a mixture.
- The object store: two exec jobs at different revisions reading the same
  path at once, under `-race`; an archive keeping its suffix.
- The wrapper trap (2.8): a job naming a second environment on a traced node
  still verifies, by a test that fails if the node falls back to local roots.
- `warn`: everything counted, nothing refused.

**Integration, through the assembled node and hub**, because a stubbed
fetcher proves less than this needs:

- A real hub serving a `file_roots` tree and a gitfs tree, each with a signed
  manifest, and a real `halite-node` running `state.apply`: it applies. Then
  every row of the table in 3.8 against the real pair.
- A differential: the manifest `tree manifest --repo --commit` writes equals,
  entry for entry, the listing the hub serves for that commit through gitfs
  (`Roots.Manifest` over the materialised tree), on a repository with a
  symlink, an `export-ignore` attribute, a file the hub's ignore glob hides,
  and a non-ASCII path.

**On real machines, which is what closes it**

- A FreeBSD 14 and a FreeBSD 15 node from `contrib/tofu` (tier 1), and one
  Linux node: a highstate under `true`, an outage with the hub stopped
  mid-run, and an attempted rollback. Torn down in the same session.
- `ref-salt1` (FIPS, arm64): verification under a FIPS build.
- The AWS KMS signer signing a manifest through `--sign-extension`, and a
  node accepting it.
- A relay hub serving the same repository, with a node behind it.
- A macOS or Windows node, for a case-insensitive file system and a manifest
  holding two paths that differ only in case. I expect the object store to
  make this harmless; that expectation is what to check.
- The estate's own state tree signed, and verified in `warn` on one
  non-production node, so that sizes and times are measured rather than
  assumed.

## 9. What Salt does

Read in `~/saltstack` at `221495872f` (2026-11-19), whose `salt/version.py`
names 3008 (Argon) as the latest released version (`version.py:85`).

- **No signed state trees.** The file client takes the digest from the
  server's `hash_file` (`salt/fileclient.py:1292`) and checks a download
  against the digest the server sent (`fileclient.py:1394-1402`), which is
  the trust halite has today (2.2).
- **No signature verification in its gitfs.** A search of
  `salt/utils/gitfs.py` for `gpg` and `verify` finds only `ssl_verify`
  settings, so halite's hub-side `gitfs_verify_signatures` is already beyond
  Salt.
- Salt's publication signing (`sign_pub_messages`) signs jobs with a key the
  Salt server itself holds (`salt/channel/server.py:1950-1956`, checked on
  the receiving side at `salt/channel/client.py:767-780`). That
  authenticates the server, which halite gets from mutual TLS; it is no
  defence against the server.

So there is nothing to stay compatible with. An existing Salt tree works
unchanged until somebody signs it.

## 10. Open questions for the maintainer

1. **Correct SPEC 25.1's compromised-hub row and the hardening table now?**
   "Signed gitfs refs" is listed against a compromised hub (`SPEC.md:2481`,
   `docs/handbook/28-hardening.md:19`), and the check runs on the hub (2.1).
   In its own change, or with this feature?
2. **Is a CI clock an acceptable ordering root?** The alternative is a
   counter, which needs the signer to keep state or read the last manifest
   back from the branch. Timestamps make rollback-by-re-signing natural.
3. **A freeze bound by default?** `state_manifest_max_age: 0` leaves the
   freeze open and costs nothing. Something like `30d` with a nightly
   re-sign closes it, and makes a month-long hub outage stop state runs.
4. **The same keys as jobs, or separate ones?** I proposed
   `state_signer_keys`, separate: a CI signer for trees and an operator key
   for jobs are different authorities. The domains keep the two signatures
   apart either way; a separate KMS key would also keep them apart in the
   audit log.
5. **Pillar.** Accept that pillar is outside the guarantee and say so
   plainly, or pursue a node-verifiable pillar later? Pillar is compiled by
   the hub from secrets, so a signature would have to be over the pillar
   *sources*, compiled again on the node. That is a much larger design.
6. **Two signers?** Should a manifest be able to require M of N keys, for an
   estate that wants a two-person rule?
7. **The reserved path.** `_halite/` mirrors `_ext/`. Another name? And should
   the hub refuse to serve a tree whose `_halite/` holds anything else?
8. **`state_signature_local`.** Cover the node's own roots by default, as
   proposed, or only the hub's tree?
9. **Order against issue 765.** Both are P2. This one has a consumer in every
   highstate on every node; 765 has no orchestration in the estate tree to
   serve today (its proposal, section 2.8). I would build this one first.
10. **File modes.** Should the first schema carry a mode per file, so that a
    later state taking its mode from the source does not need a schema
    version? I left it out because I found no consumer.

## 11. What I did not verify

- **Nothing in this document has been run.** Every statement about today is
  a reading of `d0d5925c`.
- That a run can mix two revisions when the tree changes mid-highstate
  (2.2): inferred from `cacheFile` fetching per path, not demonstrated.
- That a traced job naming another environment falls back to local roots
  (2.8): inferred from the order of `execute.go:55` and `:87` and the
  assertion at `:220`, not run. Reported separately as a candidate defect.
- That relays do not proxy file requests (2.6): a search found no such code
  and SPEC 5.3 says they do. Not run against a relay.
- That `git archive` output and a checkout differ for this estate's tree:
  a known behaviour in general, not measured here.
- That no state takes a file mode from the tree (3.2).
- What the three state functions' signatures accept (3.9).
- How the estate's tree reaches the hub (gitfs or `file_roots`), and whether
  any CI exists for it. I did not read the hub's configuration.
- Every size and cost: manifest size, verification time, object store disk
  use. The 16 MiB bound is arithmetic, not a measurement.
- Salt's behaviour was read in source, not run.
