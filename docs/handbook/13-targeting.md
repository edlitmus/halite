# Targeting

<!-- abstract: Choosing the nodes a command, a state or a rule applies to: globs, lists, regular expressions, grains, pillar, subnets, nodegroups and compound expressions. -->
<!-- kind: concept -->
<!-- index: targeting; target; glob; compound; nodegroup; grain target; pillar target; subnet target; regular expression; precedence; -G; -P; -I; -J; -S; -N; -C; -L; -E; stale grains; no node matched -->

Every command you send to the fleet, every line of the top file, every
rule of the authorization policy and every reactor says *which nodes*. That
is a **target**: an expression that the hub evaluates to a set of node
IDs. This chapter is the complete list of ways to write one.

Two properties are worth knowing before the syntax.

**The hub evaluates targets, not the nodes.** A node receives only jobs
that matched it. A node never learns the target expression, or that other
nodes exist. (In systems where the node does the matching, a compromised
node reads everyone's targets and arguments.)

**Matching uses what the hub last heard.** Glob and list matches use the
node ID, which the hub knows exactly. Grain and subnet matches use the
**last grains the node reported** ([chapter 8](08-grains.md)); pillar
matches use the pillar the hub compiles for that node. A grain you changed
a minute ago may not have reached the hub yet.

## The target types

Each type has a flag on `halite-hub run`, and a `match:` name in top files.

| Type | Flag | Example | Matches |
|---|---|---|---|
| **glob** | (none) | `web*.prod` | Node IDs by shell-style pattern. The default. |
| **list** | `-L` | `-L web1,web2` | Exactly those IDs. |
| **regular expression** | `-E` | `-E '^web[0-9]+\.prod$'` | IDs matching an RE2 expression. |
| **grain** | `-G` | `-G 'os_family:Debian'` | Nodes whose grain matches a glob. |
| **grain regex** | `-P` | `-P 'osrelease:^24\.'` | Nodes whose grain matches a regular expression. |
| **pillar** | `-I` | `-I 'webserver:port:8080'` | Nodes whose compiled pillar value matches a glob. |
| **pillar regex** | `-J` | `-J 'db_password:^s3'` | Pillar value matches a regular expression. |
| **subnet** | `-S` | `-S 192.168.1.0/24` | Nodes holding an address in that network (or that exact address). |
| **nodegroup** | `-N` | `-N prod_web` | A named expression from `hub.yaml`. |
| **compound** | `-C` | `-C 'G@os:Ubuntu and web*'` | A boolean combination of any of the above. |

The patterns:

- **Glob** uses `*` (any characters), `?` (one) and `[abc]`. It matches the
  whole ID: `web*` matches `web1.prod` but `web` alone does not.
- **Regular expressions** are RE2 and match anywhere unless anchored with
  `^` and `$`. RE2 has no backreferences or lookahead; it is a deliberate
  choice, because linear-time matching means a hostile expression cannot
  hang the hub.
- **Grain and pillar targets** are `key:value`. The key may walk nested
  data with colons: `-G 'systemd:version:255'`,
  `-I 'webserver:sites:www:server_name:www.*'`. The value is a glob, so
  `-G 'os:Ubu*'` works. A key that does not exist simply does not match.

Each of these was run against a real hub with one enrolled node, and
matched or did not as expected. For example:

```sh
halite-hub run web1.lab test.ping            # a literal ID is a glob
halite-hub run 'web*' test.ping
halite-hub run -L web1.lab,web9.lab test.ping
halite-hub run -E '^web[0-9]\.lab$' test.ping
halite-hub run -G 'os:Ubuntu' test.ping
halite-hub run -P 'osrelease:^24\.' test.ping
halite-hub run -I 'webserver:port:8080' test.ping
halite-hub run -S 192.168.1.0/24 test.ping
```

All answered `web1.lab: True`. A target that matches nothing is not an
error in the protocol, but the command says so and exits non-zero:

```
$ halite-hub run -S 10.99.0.0/16 test.ping
no node matched "10.99.0.0/16"
$ echo $?
1
```

That makes `if halite-hub run ... ; then` safe in a script: it does not
silently do nothing.

> **Not built:** `--require-match`, `--fresh` (force a grain refresh before
> matching) and the range target. A command using the first two is refused
> with `is not a flag of`. Use `saltutil.refresh_grains` and then target.

## Compound expressions

A **compound** expression joins terms with `and`, `or`, `not` and
parentheses. Each term is a *typed term*, `T@value`, or a bare word that
is a glob on the ID:

| Prefix | Type | Example |
|---|---|---|
| `G@` | grain | `G@os_family:Debian` |
| `P@` | grain regex | `P@osrelease:^24\.` |
| `I@` | pillar | `I@webserver:port:8080` |
| `J@` | pillar regex | `J@db_password:^s3` |
| `L@` | list | `L@web1,web2` |
| `S@` | subnet | `S@192.168.1.0/24` |
| `E@` | regex on the ID | `E@^web[0-9]+$` |
| `N@` | nodegroup | `N@prod_web` |
| (none) | glob on the ID | `web*` |

```sh
halite-hub run -C 'G@os:Ubuntu and web*' test.ping
halite-hub run -C '(G@os:Ubuntu or db*) and not L@web9.lab' test.ping
halite-hub run -C 'G@os_family:RedHat and not G@virtual:physical' pkg.list_upgrades
```

**Precedence** is fixed and written down: `not` binds tightest, then
`and`, then `or`. So `a or b and c` is `a or (b and c)`. When in doubt,
add parentheses; they cost nothing.

The grammar is not a regular expression and does not degrade into a
broader match when you make a mistake. An error names the token and the
column:

```
target expression "G@os:Ubuntu and (": column 18: the expression ends where a target was expected
target expression "G@os:Ubuntu andd web*": column 13: unexpected "andd"
target expression "(G@os:Ubuntu": column 1: this `(` is never closed
```

One trap remains: a **mis-typed prefix** is not an error but an ordinary
glob that matches nothing, so `X@foo` simply reports `no node matched`.
`halite-hub runner nodegroups.expand` (below) and a `test.ping` before a
real run catch it.

### Quoting

The shell sees your target first. Quote any expression that contains a
space, `*`, `?`, `(`, `)`, `!`, `&` or `|`:

```sh
halite-hub run 'web*' test.ping           # right
halite-hub run web* test.ping             # the shell may expand it to file names
halite-hub run -C 'G@os:Ubuntu and web*' test.ping
```

## Nodegroups

A **nodegroup** is a named compound expression, defined once in `hub.yaml`
and used everywhere:

```yaml
nodegroups:
  web: 'G@os:Ubuntu and web*'
  prod_web: 'N@web and not L@web9.lab'
```

Groups may refer to other groups with `N@`, to a depth of ten. The hub
checks them **at start**: a cycle, or a reference to a group that does not
exist, is a configuration error you see when the hub starts, not a
surprise when someone runs a job at 3 a.m.

```sh
halite-hub run -N prod_web test.ping
halite-hub runner nodegroups.list
halite-hub runner nodegroups.show prod_web
halite-hub runner nodegroups.expand prod_web
```

```
prod_web: N@web and not L@web9.lab
web: G@os:Ubuntu and web*
```

`nodegroups.expand` returns the node IDs a group matches right now, which
makes it the best way to see *exactly* what a command is about to hit
without hitting it.

An undefined group is an error, not an empty match:

```
target expression "nosuch": nodegroup "nosuch" is not defined
```

## Staleness

Because grain and subnet matching use cached data, an old cache can match
the wrong set. The hub makes that visible. A node whose grains are older
than `grain_stale_after` (one hour by default) is still matched, but the
result is marked stale, so a match against a node that has not reported in
a week says so. Grains are refreshed:

- when a node starts;
- every `grains_refresh_interval` (default 30 minutes);
- on demand: `halite-hub run <target> saltutil.refresh_grains`.

If a decision depends on a grain you have just changed, refresh first, then
target.

## Targets elsewhere

The same expression syntax is used wherever a set of nodes is chosen:

| Where | Written as |
|---|---|
| A command | `halite-hub run -C '…' fun` |
| The state top file | `'G@os_family:FreeBSD and web*':` with `- match: compound` ([chapter 10](10-top-and-files.md)) |
| The pillar top file | The same, but only trusted grains and the node ID may be used ([chapter 9](09-pillar.md)) |
| An orchestration step | `tgt: 'web*.prod'` with `tgt_type: compound` |
| A reactor reaction | `tgt: {{ data['id'] }}` |
| An authorization rule | `target: 'web*.prod'` ([chapter 26](26-authorization.md)) |

In a policy, the target of a rule limits what its holder can reach: a role
that may run `service.restart` against `web*.prod` cannot run it against
`db*`, whatever the operator types.

## Choosing targets well

- **Name nodes so that globs are enough.** `web1.prod.example.com`,
  `db2.staging.example.com`: then `web*.prod*` needs no grain at all, and
  the node ID is the one thing a node cannot lie about.
- **Use grains for what is true of the platform, and IDs or nodegroups for
  what is a decision.** "Debian" is a grain. "The canary" is a nodegroup.
- **Always test a target first.** `halite-hub run -C '…' test.ping`
  lists exactly who answers. For anything destructive, run
  `nodegroups.expand` or `test.ping` and read the list before you run the
  real thing.
- **Combine a target with a batch.** [Chapter 14](14-remote-execution.md)
  shows `--batch` and `--subset`, which turn any target into a rolling
  change.
- **Prefer a nodegroup to repeating an expression** in scripts, so the
  definition of "production web servers" lives in one reviewed file.

## Key ideas

- A target selects nodes; **the hub evaluates it**, using node IDs, last-
  reported grains, and compiled pillar.
- Ten types: glob, `-L`, `-E`, `-G`, `-P`, `-I`, `-J`, `-S`, `-N`, `-C`.
- Compound: `G@…`, `P@…`, `I@…`, `J@…`, `L@…`, `S@…`, `E@…`, `N@…`; `not`
  > `and` > `or`; parentheses group; errors name the column.
- Nodegroups are named expressions, validated when the hub starts.
- A target that matches nothing says so and exits `1`.
- **Refresh grains before targeting on one you just changed**, and test
  a target with `test.ping` before a real run.

Next: [running things on the fleet](14-remote-execution.md).
