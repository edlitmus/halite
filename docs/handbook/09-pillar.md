# Pillar

<!-- abstract: Per-node data and secrets: how pillar is written, targeted, merged and protected, and what it does and does not do. -->
<!-- kind: concept -->
<!-- index: pillar; pillar top file; pillar_roots; pillar.get; pillar.items; secrets; gpg; encrypted pillar; merge strategy; pillar_source_merging_strategy; pillar_merge_lists; pillar_trusted_grains; ext_pillar; aws_secrets_manager; redaction; --reveal; ignore_missing -->

**Pillar** is data that you give to machines. Where grains are what a
machine *says about itself*, pillar is what *you tell it*: which port a
service listens on, which users exist, which NTP servers to use, the
password for the database.

Pillar earns its place for three reasons:

1. **It separates the shape from the values.** States say *how*; pillar
   says *what*. Most day-to-day changes are pillar edits, and a state
   tree can be shared between environments by changing only pillar.
2. **It is targeted.** The same key can have a different value on
   production and staging, or on web and database servers.
3. **It is private.** Pillar is compiled **on the hub, for one node at a
   time**, and sent over the node's own authenticated connection. A node
   receives only its own pillar and cannot ask for anyone else's. That
   makes pillar the right home for secrets, and states and files in the
   tree the wrong one: a state file is served to every node that can
   read the tree.

## A first pillar

A pillar tree looks exactly like a state tree: an `SLS` directory with a
`top.sls`. This is the playground's:

<!-- example: handbook/webserver/pillar/top.sls -->
```yaml
base:
  '*':
    - common
  'web*.example.com':
    - webserver
```

Every node gets `common`; nodes whose ID matches `web*.example.com` also
get `webserver`. Each is just a YAML file (templated with Jinja like any
SLS) whose contents become the node's pillar:

<!-- example: handbook/webserver/pillar/webserver.sls -->
```yaml
webserver:
  port: 8080
  sites:
    www:
      server_name: www.example.com
    api:
      server_name: api.example.com
      root: /srv/api/public
```

The compiled pillar is all the files that matched, merged. Look at it
for a given node:

```sh
halite-node pillar items            # on an enrolled node: asks the hub
halite-node pillar items --local --pillar-root /srv/halite/pillar
halite-node pillar get webserver:port
halite-hub run web1.example.com pillar.items
```

```
web1.lab:
    motd: hello from the hub
    ntp:
        servers:
            - 0.pool.ntp.org
            - 1.pool.ntp.org
    webserver:
        port: 8080
    db_password: s3cret-value
```

(The last command, run from the hub, needs a permission that no wildcard
grants — reading a node's pillar is reading its secrets. See
[chapter 26](26-authorization.md).)

## Using pillar

In a state or a template, `pillar` is a dictionary:

```yaml
{% set port = pillar['webserver']['port'] %}
{% set tz = pillar.get('timezone', 'UTC') %}
{% for site, cfg in pillar['webserver'].get('sites', {}).items() %}
...
{% endfor %}
```

`pillar.get('webserver:port', 8080)` reads nested keys with colons and
returns the fallback if any link is missing. It is the safe form for an
optional value, because halite makes a missing key in `pillar[...]` an
error ([chapter 7](07-templates.md)).

Inside a file that `file.managed` renders, pillar is also in scope. To
write a whole pillar value into a file with no template at all, use
`contents_pillar`:

```yaml
/etc/app/license:
  file.managed:
    - mode: '0600'
    - contents_pillar: app:license
```

## Pillar and the top file

The pillar top file works just like the state top file
([chapter 10](10-top-and-files.md)): environment, then target
expressions, then SLS names. Globs, lists, regular expressions, compound
expressions and trusted grains all work as targets. Three rules apply
only here.

**Pillar can be targeted only on trusted grains.** Grains come from the
node, so a node that could pick any grain could claim `role: database`
and receive the database's secrets. The hub therefore only evaluates
pillar targeting against `pillar_trusted_grains`, which by default
are `id`, `os`, `os_family`, `osrelease`, `kernel`, `cpuarch`, `virtual`
and `fips_mode`. Anything else is refused by name:

```
pillar top expression "G@role:database" targets on the grain(s) role,
which a node controls and which are not in pillar_trusted_grains; add them
deliberately, or move the attribute to a hub-authoritative node attribute
```

To allow a custom grain, add it to `pillar_trusted_grains` in `hub.yaml`.
That *replaces* the default list rather than extending it, so write the
defaults you still want. Adding a grain to the list is a decision to
record: from then on, any node that can write its own grains can choose
its own pillar.

**Pillar cannot be targeted on pillar.** `I@` and `J@` targets are
refused, because the answer would depend on the thing being computed:

```
pillar top expression "I@app:port:80" targets on pillar, which is not
available while pillar is being compiled
```

**Node ID is always safe to target.** A node's ID comes from its
certificate and is not a grain it supplies, so `web*.example.com` is
trustworthy in a way `G@role:web` is not. The strongest practice is to
name nodes by role and target pillar by name.

An entry `- ignore_missing: True` in a target's list makes the pillar top
file tolerate an SLS file that does not exist (without it, a missing file
is an error: `pillar sls "nonexistent" was not found in environment
"base"`). It helps when machines are added before their data is:

```yaml
base:
  '*':
    - ignore_missing: True
    - one
    - nonexistent
```

> **Not built:** SPEC 12.4 describes *hub-authoritative node attributes*,
> values an operator attaches to a node that the node cannot forge,
> targeted as `node:`. The pillar compiler understands the `node:` prefix,
> but this build has no command for setting an attribute. For now, role
> by node ID, or trust a grain deliberately.

## Merging

When several matched files define the same key, halite must combine them.
The strategy is `pillar_source_merging_strategy` in `hub.yaml`; `smart`
is the default. Given these two files, matched in this order:

```yaml
# one.sls                      # two.sls
app:                           app:
  port: 80                       port: 8080
  tags: [a, b]                   tags: [c]
  db:                            db:
    host: db1                      host: db2
    user: app
```

the four strategies produce (the merged `app`, tested):

| Strategy | `port` | `tags` | `db` |
|---|---|---|---|
| `smart` (default) | 8080 | `[c]` | `{host: db2, user: app}` |
| `recurse` | 8080 | `[c]` | `{host: db2, user: app}` |
| `aggregate` | 8080 | `[a, b, c]` | `{host: db2, user: app}` |
| `overwrite` | 8080 | `[c]` | `{host: db2}` |

- Mappings are merged key by key (except under `overwrite`, where the
  later value replaces the whole key).
- Scalars: the later file wins.
- Lists: replaced, unless the strategy is `aggregate` (concatenate), or
  `pillar_merge_lists: true` is set, which makes `recurse` and `smart`
  concatenate as well.

"Later" means the order the top file lists the SLS files, with later
meaning more specific by convention. Put defaults first and overrides
after.

## Secrets

The point of pillar is that you can put secrets in it. Three rules keep
them secret.

**They live in pillar and not in states.** The rendered `file.managed`
that writes a password is in the state tree, which every node can read;
the password itself is in pillar, which only that node receives.

**They are masked on output.** Anything decrypted from an encrypted file
or fetched from a secrets manager is registered with a redactor, which
then scrubs it from every log line, event, error message and command
output:

```sh
halite-node pillar items
```

```
db:
    user: app
    password: **********
```

`--reveal` shows the real value; having to ask for it is the point.
Masking is a display rule and nothing more: a template still renders with
the real value. Plain (unencrypted) pillar values are not masked, because
masking every address and path would make the output useless for the
thing it is used for, which is checking what a state will see.

**They are written to disk carefully.** The file a state creates should
say so, with a tight mode:

```yaml
/etc/app/db.conf:
  file.managed:
    - mode: '0600'
    - user: app
    - contents: |
        password = {{ pillar['db']['password'] }}
```

and a node holds pillar in memory only; it writes none to disk unless
`pillar_cache_disk` is set, in which case the cache is encrypted at rest
(see below for why you might want it).

### Encrypting values with GPG

For secrets that live in the tree alongside everything else, encrypt the
value, so the tree can sit in git. halite decrypts on the **hub**, at
compile time, with the system `gpg` binary, so the private key never
goes to a node.

The file starts with a renderer line, and each secret is an armored GPG
message in a block scalar. This was run end to end with a throwaway key:

```yaml
#!yaml|gpg
db:
  user: app
  password: |
    -----BEGIN PGP MESSAGE-----

    hQEMA1f…
    -----END PGP MESSAGE-----
```

Encrypt a value to the hub's key:

```sh
printf 'the-password' | gpg --armor --encrypt -r pillar@example.com
```

Point the hub at the key ring with `gpg_home` (set it explicitly; a
service account's environment is rarely the one you tested in) and keep
`gpg_binary` as `gpg` unless yours has another name:

```yaml
# hub.yaml
gpg_home: /usr/local/etc/halite/gpgkeys
gpg_timeout: 30s
```

If `gpg` is not installed, a tree that names the renderer fails at load,
not at first use.

> **Not built:** the native `crypt` renderer of SPEC 12.5 and the
> `halite-hub pillar encrypt|decrypt|rekey|recipients|migrate-gpg`
> commands. `#!yaml|crypt` fails with `the crypt renderer runs as a bridged
> extension, which is not available in this build`. Encrypted pillar is GPG
> for now, or a secrets manager (next).

### Fetching secrets from a secrets manager

If the secrets live in AWS Secrets Manager rather than in the tree, the
hub can read them at compile time through the signed `pillar` extension
`aws_secrets_manager`, named under `ext_pillar` in `hub.yaml`:

```yaml
ext_pillar:
  - aws_secrets_manager:
      secret: true
      region: us-east-1
      secrets:
        - name: database.creds
          secret_id: arn:aws:secretsmanager:us-east-1:123456789012:secret:prod/db-AbCdEf
```

A JSON secret becomes a mapping, and a dotted name nests:
`pillar.get('aws_secrets:database:creds:password')`. Everything this
source returns is masked like any other secret. A source that fails
**fails the whole compile** by default: a pillar missing the half that
held the credentials is worse than none, because the run would proceed
and the state could not tell an absent secret from one that failed to
arrive. `ext_pillar_fail: ignore` is available per source where a source
is genuinely optional. Full details, and how to write your own source,
are in [the states guide](../states.md#secrets-from-aws-secrets-manager)
and [the extension guide](../extensions.md).

### Other external sources

`ext_pillar` takes a list of sources, each a signed extension of kind
`pillar` that the hub runs as a separate process
([chapter 29](29-extensions.md)). One ships with the project
(`aws_secrets_manager`). The others the specification names — consul,
etcd, vault, mysql and so on — are written as extensions when a site needs
them. A source name the build does not know is refused when the hub
starts.

## Refreshing

The hub recompiles a node's pillar whenever the node asks, and caches the
result keyed by everything that went into it: the node's trusted grains,
the pillar environment and a digest of every source file. Any change to
any of those invalidates the entry, so an edit to a pillar file takes
effect on the node's next request without anyone refreshing a thing.

A node asks every time it compiles states, so the next `state.apply`
sees the change. To push new pillar into the node's running memory
(for beacons, the scheduler, the mine) run:

```sh
halite-hub run 'web*' saltutil.refresh_pillar
```

If the hub cannot be reached, **pillar does not fall back** to anything
local, and a command that needs it fails. This is deliberate: a state
rendered against an empty or stale pillar would report a convergence that
never happened. The exception is one you opt into: with
`pillar_cache_disk: true` the node keeps its last compiled pillar,
encrypted at rest, so a run survives a hub outage. It is still a copy of
your secrets on that disk, which is why it is off by default.

## Pillar and the hub's own limits

A pillar template renders on the hub, where the `salt` dispatcher is
restricted to a safe set of read-only functions (SPEC 25.5). `cmd.*`,
file writes and anything else with side effects are absent. The reason
is simple: if a pillar template could run a command, then "can edit the
pillar tree" would mean "root on the hub". Nothing about this changes how
you write pillar; it just means you cannot compute a value by running a
shell command there. Compute it in a state, or in a grains script, or put
the answer in the file.

## Patterns

**One file per concern.** `pillar/ntp.sls`, `pillar/users.sls`,
`pillar/nginx.sls`, each with a namespaced top key (`ntp:`, `users:`).
Avoid putting unrelated data at the root, where two files can collide.

**Defaults in the state, overrides in pillar.** Write `pillar.get('ntp:servers',
['pool.ntp.org'])` so a node with no entry still works.

**Per-environment pillar.** Use environments (`base`, `prod`) with
different `pillar_roots`, or target on a trusted grain/ID, instead of
branching on environment names inside states.

**Secrets in one place.** Keep every secret in files named so that nobody
grants access by accident, encrypted or in the secrets manager, and put
the encrypted files' recipients under change control.

**Test a node's pillar before you apply.** `halite-node pillar items`, or
`halite-hub run <node> pillar.items` (with the permission), shows exactly
what a node will be given.

## Key ideas

- Pillar is **your data for nodes**, compiled **on the hub, per node**,
  delivered only to that node: the right place for secrets.
- It uses a **top file and SLS files** like states; targeting uses
  **trusted grains and node ID only** and never pillar itself.
- **Merging** is deep for mappings, replace for lists and scalars under
  `smart`; `aggregate` and `pillar_merge_lists` concatenate lists.
- Secrets are **masked on output** (`--reveal` to see), kept off disk,
  and encrypted in the tree with GPG or fetched from a secrets manager.
- An edit takes effect on the node's next request; **pillar does not fall
  back** when the hub is unreachable, unless you enable
  `pillar_cache_disk`.

Next: [the top file and the file server](10-top-and-files.md).
