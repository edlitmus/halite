# Writing states

A state describes how part of a machine should be. halite reads the
description, compares it with the machine, and changes only the
difference. This page is the shape of that description.

## The declaration

```yaml
nginx_config:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: salt://nginx/nginx.conf
    - mode: '0644'
```

- `nginx_config` is the **ID**. It names the declaration so other states
  can refer to it. It must be unique across the whole compiled tree.
- `file.managed` is the **state function**: the module and what to do.
- The list under it is the arguments.

When `name` is left out, the ID is used as the name. So this is the same
thing, and it is how most trees are written:

```yaml
/etc/nginx/nginx.conf:
  file.managed:
    - source: salt://nginx/nginx.conf
    - mode: '0644'
```

### Quote your modes

`mode: 0644` is an integer in YAML, and an octal one at that: it means
420. halite refuses it rather than writing the wrong permissions, and the
error says to quote it. This is the single most common way a Salt tree is
subtly wrong, and it is silent there.

```yaml
    - mode: '0644'     # correct
    - mode: 0644       # refused, with a message
```

## Ordering

States run in the order they appear in the file, and the files run in the
order the top file lists them. That is often enough. Where it is not,
requisites say so:

| Requisite | Meaning |
|---|---|
| `require` | Run after the target, and only if it succeeded. |
| `watch` | `require`, plus react if the target reported a change. |
| `onchanges` | Run **only** if the target reported a change. |
| `onfail` | Run only if the target failed. |
| `prereq` | Run before the target, but only if the target *would* change. |
| `use` | Inherit the target's arguments. |
| `listen` | Like `watch`, but the reaction runs at the end of the whole run. |

Each also has an `_in` form that points the other way, so a state can
declare a requisite on something written before it without editing that
one:

```yaml
/etc/nginx/nginx.conf:
  file.managed:
    - source: salt://nginx/nginx.conf
    - watch_in:
      - service: nginx
```

And an `_any` form, where any one of several targets satisfies it:

```yaml
    - require_any:
      - pkg: nginx
      - pkg: nginx-light
```

A requisite names a target by module and ID:

```yaml
    - require:
      - pkg: nginx          # the declaration whose ID is "nginx"
      - sls: common.users   # every state in that SLS file
```

`halite-node state show_lowstate --local` prints the ordered run before
anything happens. It is the fastest way to find out why something ran
where it did.

### `prereq` is the awkward one

`prereq` inverts the direction: the state carrying it runs *before* its
target, and only if the target would make a change. That means halite has
to run the target in test mode first to find out. A target whose test
mode is unreliable makes `prereq` unreliable too, and halite warns when a
tree does that rather than letting it be discovered in production.

## Test mode

`--test` runs everything and changes nothing:

```sh
halite-node state apply --local --test
```

Every state function is held to a contract:

- It makes no change.
- It returns no result, which prints as `would change`.
- It says what it *would* have changed, in the same `changes` shape a real
  run reports.
- Its comment is a sentence, in the conditional.

That contract is enforced by a shared harness every state module passes,
which additionally checks that a second run changes nothing and that the
system is untouched afterwards. Salt has no such harness, which is why
`test=True` there is unreliable for a fair number of its modules.

A function whose test mode genuinely cannot be reliable — `cmd.run`
cannot know what a command will do — declares that in its signature, and
the module reference marks it.

## Templates

An SLS file is rendered before it is parsed. The default is Jinja, and
the engine is Jinja-compatible rather than Jinja-like: existing trees
render unchanged.

```yaml
{% for user in pillar.get('admins', []) %}
{{ user }}_account:
  user.present:
    - name: {{ user }}
    - groups: [wheel]
{% endfor %}
```

Available in every template: `grains`, `pillar`, `salt` (the execution
modules), `opts`, `env`, `sls`, `id`, and the path helpers. See SPEC
section 10.2.7.

### The short form, for a state with no arguments

A declaration whose function takes nothing may be written as a bare
name:

```yaml
nginx:
  pkg.installed
```

which is the same as `pkg.installed: []`. It is how a tree spells "just
install it".

### One declaration, several targets

`names` expands a declaration into one state per name, and each name may
carry arguments of its own:

```yaml
scripts:
  file.managed:
    - user: root
    - mode: '0755'
    - names:
      - /usr/local/bin/one:
        - contents: first
      - /usr/local/bin/two:
        - contents: second
        - mode: '0700'
```

That produces two `file.managed` states. Both get `user: root`; the
first gets mode 0755 from the declaration and the second overrides it.
Requisites pointing at the ID reach every state the expansion produced.

The arguments under a name are a list, spelled like any other
declaration body. A mapping there is accepted here and is not valid
Salt, so a tree that has to work under both should use the list.

### Rendering a file before writing it

A `source` may be a template rather than a finished file:

```yaml
/usr/local/etc/app.conf:
  file.managed:
    - source: salt://files/app.conf.jinja
    - template: jinja
    - context:
        port: 8080
    - defaults:
        workers: 4
```

The source is rendered with the same names a template in an SLS file
sees — `grains`, `pillar`, `salt`, `opts` — plus anything in `context`
and `defaults`, where `context` wins. Only `jinja` is supported; another
engine is refused by name rather than ignored.

Rendering happens after `source_hash` is checked, so the digest verifies
the file that was fetched rather than what was made from it.

### Undefined names are an error

This is halite's most visible departure from Salt. In Salt,
`{{ pillar_value_that_does_not_exist }}` renders as an empty string, and
the state that results silently does the wrong thing. Here it is an error
naming the file, the line, and the identifier.

Say what you mean instead:

```yaml
{{ pillar['maybe'] | default('fallback') }}
{% if pillar.get('maybe') is defined %}...{% endif %}
{{ pillar.get('a:b', 'fallback') }}
```

Migrating a tree that relies on the old behaviour: set
`undefined: permissive`, which restores Salt's behaviour and logs every
resolution at warning level with its position. Fix the warnings, then
switch back.

### `set` does not leak out of a loop

Jinja's rule, and halite follows it: an assignment inside a `for` body
does not survive to the next iteration and does not escape the loop. Use
a namespace to carry a value out:

```yaml
{% set ns = namespace(total=0) %}
{% for n in [1, 2, 3] %}
{%- set ns.total = ns.total + n %}
{% endfor %}
total is {{ ns.total }}
```

## Pillar

Pillar is per-machine data, compiled separately and merged in a defined
order. Its top file targets machines the same way the state top file
does.

```yaml
# pillar/top.sls
base:
  '*':
    - common
  'G@os_family:Debian':
    - debian
```

Two rules are worth knowing before you rely on them.

**Pillar cannot be targeted on a machine's own grains** unless you list
the grain in `pillar_trusted_grains`. Grains come from the machine, so a
machine able to target pillar on its own grains could claim to be
anything and ask for another machine's secrets.

**Pillar cannot be targeted on pillar.** `I@` and `J@` in a pillar top
file are refused, because the answer would depend on the thing being
computed.

## Files

`salt://` names a file in the state tree, served by whichever backend the
environment uses:

```yaml
/etc/nginx/nginx.conf:
  file.managed:
    - source: salt://nginx/nginx.conf
    - source_hash: sha256=e3b0c44298fc1c149afbf4c8996fb924...
```

A `source_hash` is checked before anything is written. `md5` and `sha1`
digests are refused by name: collisions in both are within reach, so
comparing one verifies nothing.

Every path is resolved and then checked to be inside its root, after
symlinks are followed. That check is the control for CVE-2020-11652,
where Salt's file server could be walked out of its own roots.

## What a state may not do

- Refer to an ID that does not exist. The compiler collects every such
  error and reports them together, rather than stopping at the first.
- Declare the same ID twice.
- Use a module function this build does not ship. The error names near
  misses, so a typo is one line to fix rather than a search.

## Further reading

- [Module reference](modules.md) — every function, its parameters, and
  whether its test mode is reliable.
- [Migrating from Salt](migrating-from-salt.md).
- SPEC.md sections 11 and 12 — the state system and pillar, specified.
