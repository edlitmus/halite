# The top file, environments and the file server

<!-- abstract: How a tree is laid out and served: the top file that assigns states to nodes, environments, file roots, git and S3 backends, and good tree structure. -->
<!-- kind: concept -->
<!-- index: top file; top.sls; environment; base; file_roots; pillar_roots; file server; salt:// URI; halite:// URI; gitfs; s3fs; fileserver_backend; match; compound match; formula; init.sls; tree layout; file.recurse; top_file_merging_strategy; file_ignore_regex -->

Two questions remain once you can write states and pillar: *which
machines get which files?* and *where do the files live, and how do they
get there?* The first is answered by the **top file**; the second by the
**file server**.

## The top file

`top.sls` is the index of a tree. It is a mapping from **environment** to
**target** to **list of SLS files**. The example tree's:

<!-- example: handbook/webserver/states/top.sls -->
```yaml
base:
  '*':
    - common
  'web*.example.com':
    - webserver
  'G@os_family:FreeBSD and web*':
    - match: compound
    - webserver.bsd_tuning
```

Read it as three rules:

1. In the `base` environment, **every node** (`*`) gets the `common`
   states.
2. Nodes whose ID matches the **glob** `web*.example.com` also get
   `webserver`, which is `webserver/init.sls`.
3. Nodes that are FreeBSD **and** whose ID starts with `web` also get
   `webserver.bsd_tuning`. This target is a **compound expression**, so
   the entry says so with `- match: compound`.

A node is matched against every rule, and the **union** of their SLS
lists is its highstate. There is no "first match wins": a web server on
FreeBSD gets all three.

With the example tree and a node ID of `web1.example.com`, as FreeBSD:

```sh
halite-node state show_states --local --file-root states --pillar-root pillar \
  --id web1.example.com
```

```
- common.common_packages
- common.admin_alice
- common.admin_bob
- common.motd
- webserver.nginx.nginx
- webserver.nginx.nginx_conf
- webserver.sites.site_www
- webserver.sites.site_api
- webserver.bsd_tuning.somaxconn
- webserver.nginx.nginx_service
```

Debian gets the same list without `somaxconn`.

### The matchers

The default matcher is a **glob** on the node ID. Write `- match: <type>`
as the first item under a target to change it:

| `match:` | Target is | Example |
|---|---|---|
| (none) | a glob on the node ID | `'web*.example.com'` |
| `list` | comma-separated exact IDs | `'web1,web2'` |
| `pcre` | a regular expression on the ID | `'^web[0-9]+$'` |
| `grain` | `grain:value`, the value a glob | `'os_family:Debian'` |
| `grain_pcre` | `grain:regex` | `'osrelease:^24\.'` |
| `compound` | a boolean expression | `'G@os:Ubuntu and not db*'` |

A compound expression combines matchers with `and`, `or`, `not` and
parentheses; prefixes pick the kind of each term: `G@` grain, `P@` grain
regex, `I@` pillar, `J@` pillar regex, `L@` list, `S@` subnet, `E@` regex
on the ID, `N@` nodegroup. A bare word is a glob on the ID. The full
grammar and all the examples are in [chapter 13](13-targeting.md); a
top-file target accepts exactly what a command-line target does, with one
exception: **pillar top files may not target on pillar, untrusted
grains or an address** ([chapter 9](09-pillar.md)).

### Check what a node would get

```sh
halite-node state show_top          # which SLS files this node matches
halite-node state show_states       # the states, in order
halite-node state show_top --id web9.example.com --local ...   # as another node
```

`--id` lets you ask "what would `db3` get?" without logging into it. For a
grain-based target, override the grain in the configuration file's
`grains:` block, as the project's own tests do.

### Several environments' top files

If you have more than one environment (below) each may have its own
`top.sls`. `top_file_merging_strategy` decides how they combine: `merge`
(default) takes all of them, `same` requires each environment's top file
to describe only itself, and `merge_all` merges without regard to which
environment declared what.

## Environments

An **environment** is a named copy of the tree. The default is `base`.
The point of environments is to let different machines, or the same
machine at different times, read different versions of the same files:
`base` for production, `staging` for a trial.

Each environment has its own list of root directories:

```yaml
# hub.yaml
file_roots:
  base:
    - /srv/halite/states
  staging:
    - /srv/halite/states-staging
    - /srv/halite/states        # fall through to production for the rest
pillar_roots:
  base:
    - /srv/halite/pillar
  staging:
    - /srv/halite/pillar-staging
    - /srv/halite/pillar
```

Several directories for one environment are searched **in order, first
file found wins**, so a staging tree can hold only the files that differ
and fall through to the production copy for everything else.

Select an environment with the `--env` flag, the `env` setting, or for a
single job `halite-hub run ... --env staging`:

```sh
halite-node state apply --env staging --test
halite-hub run 'web1*' state.apply --env staging --test
```

`saltenv` is a permanent alias for `env` (in templates, in targeting and
in the configuration); it is not going to be deprecated, since it appears
in a great deal of existing trees. If environments are git branches and
anyone can push a branch, restrict which names a run may use with
`env_allowlist` and `env_denylist`.

## The file server

The hub serves the tree to nodes. A node never reads the hub's disk; it
asks over its authenticated connection, and the file server answers for
**one environment and one path at a time**. Anywhere a state, a template
or a `source:` names a file, it can use a URI:

```yaml
- source: salt://webserver/files/nginx.conf.jinja
- source: halite://webserver/files/nginx.conf.jinja       # the same file
- source: salt://webserver/files/nginx.conf.jinja?saltenv=staging
```

`halite://` and `salt://` are identical, and `salt://` is accepted for as
long as the project exists: it appears in tens of thousands of lines of
existing trees and there is no value in churning it. A path is relative to
the root of the environment, so the example above is
`<file_root>/webserver/files/nginx.conf.jinja`.

How a node gets a file:

1. The node asks for the file list and hashes of the subtree, in one
   round trip.
2. It fetches only files whose hash it does not already have, and asks
   conditionally (`If-None-Match`) so an unchanged tree costs a request,
   not a transfer.
3. It checks each file's hash against the manifest **before** moving it
   into place, and writes it atomically (temporary file, `fsync`,
   `rename`).

The guarantees you get from the server itself:

- **Containment.** A requested path is resolved, symlinks followed, and
  rejected unless it is still inside the root. The classic directory
  traversal in this kind of service cannot happen; this code path has a
  fuzz test and a property test for it.
- **Symlinks** inside a root are followed only if
  `fileserver_follow_symlinks: true`, and never outside the root.
- **Hiding.** `file_ignore_glob` and `file_ignore_regex` hide paths from
  every backend. Use them for `.git/` and editor droppings, not as a
  security control. A file that must not be served should not be in the
  tree.
- **Hash.** SHA-256 by default (`hash_type`); sha384, sha512 and sha3-256
  are available. MD5 and SHA-1 are accepted only to check a `source_hash`
  published by an upstream that offers nothing better, and each use logs
  a warning.

### Backends

`fileserver_backend` is an ordered list of where files come from; the
first backend with the file wins.

| Backend | Serves | Notes |
|---|---|---|
| `roots` | Local directories named in `file_roots` | The default. |
| `git` | Branches and tags of a git repository, as environments | Needs the system `git`. [Details](#serving-the-tree-from-git). |
| `s3` | An S3 bucket's objects | Signs requests itself; no cloud SDK. |

Put `roots` first and a local file overrides what the repository serves,
which is useful for a hotfix.

### Serving the tree from git

This is how most real fleets serve a tree: it is the source of truth,
reviewed and versioned, and the hub only mirrors it.

```yaml
# hub.yaml
fileserver_backend:
  - roots
  - git
gitfs_remotes:
  - url: https://git.example/estate/halite-tree.git
    name: estate
    root: states
gitfs_base: main
gitfs_update_interval: 5m
gitfs_verify_signatures: true
gitfs_keyring: /usr/local/etc/halite/gitfs-keyring
```

- Each **branch and tag becomes an environment**; `gitfs_base` names the
  branch that becomes `base`.
- `root` serves a subdirectory of the repository.
- The hub runs the system `git` against bare mirrors in its cache, every
  `gitfs_update_interval`, and on demand with
  `halite-hub runner fileserver.update`.
- **Signature verification** (`gitfs_verify_signatures`) requires the tip
  of a served ref to carry a valid signature by a key in `gitfs_keyring`,
  checked with `git verify-commit`. A ref that fails is **not served**,
  and the failure is an event. This is the control that turns a file
  server from a code-delivery path into a *signed* code-delivery path.
  Turn it on for any tree that can change a production machine.

Credentials for a private repository go in the environment of the hub
process or an SSH key, never on a command line where they would appear in
the process table.

> **Not built:** a webhook that makes the hub fetch the moment someone
> pushes (`/v1/hook/gitfs` in SPEC 13.3). The generic webhook endpoint
> exists and can feed a reactor that calls `fileserver.update`
> ([chapter 20](20-reactors.md)), which gives the same effect.

### Serving the tree from S3

```yaml
fileserver_backend:
  - roots
  - s3
s3_buckets:
  - name: estate
    prefix: states/
```

The first path segment after the prefix is the environment, so an object
`states/base/top.sls` serves `top.sls` in `base`. Requests are signed
inside halite; credentials come from the environment, the instance
metadata service or a web-identity token. Prefer a role to a key.

### Other things the file server does

| Command | Does |
|---|---|
| `halite-hub runner fileserver.update` | Refresh git and S3 backends now. |
| `halite-hub runner fileserver.file_list` | List files served for an environment. |
| `halite-hub runner fileserver.dir_list` | List directories. |
| `halite-hub runner fileserver.envs` | List environments. |
| `halite-hub runner fileserver.clear_cache` | Drop the hub's cache. |

A node never lists the hub's files itself: it asks for a path and gets
that file, so there is no `cp` module to browse with. (The `cp.*`
functions of its predecessor are not built.)

## Laying out a tree

A tree is code, and the same advice applies: small files with one job,
named for what they do, assembled by a thin top file.

```
states/
  top.sls                  # only assignments; no logic
  common/
    init.sls               # what every node gets
    map.jinja              # platform differences
  webserver/
    init.sls               # include: [webserver.nginx, webserver.sites]
    nginx.sls
    sites.sls
    map.jinja
    files/
      nginx.conf.jinja     # templates this formula delivers
      site.conf.jinja
pillar/
  top.sls
  common.sls
  webserver.sls
```

- **One directory per service or concern** (some call this a *formula*):
  `init.sls` as the entry point, other `.sls` files for pieces, `files/`
  for what it delivers, `map.jinja` for platform differences. A dotted
  name `webserver.nginx` is `webserver/nginx.sls`; `webserver` alone is
  `webserver/init.sls`.
- **`top.sls` assigns; it does not compute.** All logic is in the
  formulas, so a reviewer reading the top file reads the policy of the
  estate.
- **Ship files with the formula**, so `source: salt://webserver/files/…`
  moves with it.
- **Prefer data to code.** If you find yourself writing `{% if
  grains['id'] == 'web3' %}`, you want a pillar key.
- **Name IDs for what they do**, and keep them unique across the tree:
  `nginx_conf`, not `conf`.
- **Keep secrets out of `files/`.** Everything in the tree is served to any
  node that asks for it by path; secrets are pillar.

### Whole directories

`file.recurse` copies a directory of the tree onto the node and renders
templates in it. It compares the **rendered** output, which is what lets a
tree of templates converge.

```yaml
/etc/app/conf.d:
  file.recurse:
    - source: salt://app/conf.d
    - template: jinja
    - file_mode: '0644'
    - dir_mode: '0755'
    - clean: True
```

### Testing a tree

The tools of [chapter 5](05-states.md) apply to a whole tree:

| Stage | Command | Catches |
|---|---|---|
| Edit | `halite-node lint FILE --local --file-root … --pillar-root …` | Syntax, tabs, duplicate keys, YAML-boolean warnings |
| Review | `halite-node state show_lowstate --local …` for each role | Unresolved names, missing modules, requisite errors, cycles |
| CI | the same, for each platform you run (override `os_family`/`kernel` in a config) | Platform branches that fail to compile |
| CI | the idempotency script of [chapter 4](04-idempotency.md) in a disposable machine | A second run that still changes something |
| Rollout | `halite-hub run <canary> state.apply --test`, then without `--test` | Everything else |

The project's own test suite compiles the example tree, for FreeBSD and
for Linux, on every change, which is the pattern to copy.

## Key ideas

- The **top file** assigns SLS files to nodes by environment and
  target; a node gets the union of every rule it matches.
- `- match: compound` (and `grain`, `list`, `pcre`, …) switches the
  matcher.
- **Environments** are named copies of the tree; roots in an
  environment are searched in order, first match wins.
- The **file server** serves one environment and path at a time with
  containment, atomic writes and hash checks; `halite://` and `salt://`
  are the same.
- Serve the tree from **git** with **signature verification** on.
- Lay the tree out as one directory per concern; keep the top file
  logic-free and secrets in pillar.

Next: [the module library](11-modules.md).
