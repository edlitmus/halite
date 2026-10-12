# States and SLS files

<!-- abstract: The language you describe machines in: YAML declarations, IDs, arguments, include and extend, options, and how to run and inspect them. -->
<!-- kind: concept -->
<!-- index: SLS; state declaration; state ID; state function; YAML; include; extend; exclude; names; unless; onlyif; creates; retry; order; failhard; timeout; lowstate; highstate; show_lowstate; lint; test module -->

A **state** says how one thing should be. An **SLS file** is a text file
holding a set of states. This chapter is the grammar: how to write them,
how they combine, and how to see what halite understood. The next two
chapters add ordering ([chapter 6](06-requisites.md)) and templating
([chapter 7](07-templates.md)).

## A little YAML

SLS files are YAML, a plain-text format for nested data. Four things are
enough to read every example in this handbook.

**A mapping** is `key: value` pairs, nested by indenting with spaces.
Indentation is the structure; use two spaces and **never tabs** (a tab
is an error, and halite names the line).

```yaml
nginx:
  pkg.installed:
    - version: 1.24.0
```

**A list** is lines starting with `- `, at the same indentation:

```yaml
    - groups:
      - wheel
      - operators
```

**A scalar** is a single value: a string, a number, `true`/`false`, or
nothing. Most of the time you do not quote strings:

```yaml
    - shell: /bin/bash
    - enable: True
```

**A block string** (`|`) keeps line breaks, which is how you write file
contents in place:

```yaml
    - contents: |
        line one
        line two
```

YAML has traps, and this one dialect of it is what the predecessor
tools use, so the traps are real. halite either removes them or says so:

| Trap | What happens in halite |
|---|---|
| `mode: 0644` is the *number* 420 (octal), not a mode | **Refused** with an explanation. Write `mode: '0644'`. |
| `yes`, `no`, `on`, `off`, `y`, `n` are booleans, so `name: no` is `false` | Accepted, because existing trees depend on it, but **every use is a warning** naming file and line (`"yes" resolves to the boolean true under YAML 1.1; quote it to keep the string`). Quote it. |
| A key repeated in one mapping silently keeps the last | **An error**: `duplicate mapping key "a"`, with both lines shown. |
| A tab for indentation | **An error**: `tab character used for indentation; YAML permits only spaces`. |
| `1:30` read as 90 (sexagesimal) | Read as the string `1:30`. |
| `2026-08-19` read as a date object | Read as the string. |
| `!!python/object` tags that construct code | Not supported; the parser can build nine plain types and nothing else. |

You will rarely write anything more exotic than the above. (Anchors and
aliases, `&name` and `*name`, are supported and mostly used in pillar.)

## The declaration

Here is the shape of every state:

```yaml
nginx_conf:                         # 1. the ID
  file.managed:                     # 2. module.function
    - name: /etc/nginx/nginx.conf   # 3. arguments, one per list item
    - source: salt://webserver/files/nginx.conf
    - mode: '0644'
    - require:                      #    arguments include requisites
      - pkg: nginx
```

1. **The ID** (`nginx_conf`) names this declaration. It must be unique
   across the **whole compiled tree**, not just the file — two SLS files
   that both declare `nginx_conf` are an error. It is how other states
   refer to this one, and what appears in the output.
2. **The state function** is `module.function`. `file` is the module,
   `managed` the function.
3. **Arguments** are a list of single-key mappings. Which ones exist
   depends on the function; see [the module reference](../modules.md) or
   ask the binary (below).

### `name` defaults to the ID

Most functions have a `name` argument that says what they act on: a path,
a package, a service. If you leave it out, the ID is used. So these are
identical:

```yaml
/etc/motd:
  file.managed:
    - contents: hello

motd:
  file.managed:
    - name: /etc/motd
    - contents: hello
```

Using the path or package name as the ID is the convention for short
states. Use a descriptive ID with an explicit `name` when the same
target is touched twice, or when the name is long or templated.

### States that take no arguments

```yaml
nginx:
  pkg.installed: []
```

`[]` is an empty list of arguments. The bare form is the same thing:

```yaml
nginx:
  pkg.installed
```

### Several targets from one declaration

`names` expands one declaration into one state per name. Everything else
in the declaration applies to all of them, and a name may carry its own
overrides:

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

That is two `file.managed` states. A requisite pointing at `scripts`
reaches both. In the output they share an ID and differ in `Name`:

```
          ID: named
        Name: one
          ID: named
        Name: two
```

For many similar states, a loop in the template
([chapter 7](07-templates.md)) is usually clearer, and gives each its own
ID.

## Finding out what a function takes

You do not need to memorise arguments. The binary documents itself:

```sh
halite-node call sys.state_doc file.managed --local
```

```
file.managed(name: path, source: string = , source_hash: string = ,
  skip_verify: bool = false, contents: any, template: string = ,
  context: map, defaults: map, mode: mode = , user: string = ,
  group: string = , makedirs: bool = false, dir_mode: mode = ,
  create: bool = true, replace: bool = true, backup: string = ,
  show_changes: bool = true, check_cmd: any, ...)
  Ensure a file exists with the given contents, mode, and ownership.
  - changes the system
```

Each parameter is `name: type = default`. `sys.doc` does the same for
the execution function of that name, and `sys.argspec` gives machine-
readable detail. Add `--out json` to any of them for scripting. Everything
those commands print is also in the generated
[module reference](../modules.md), which is current for the build that
shipped the handbook.

A typo is caught for you. A misspelt function lists the real ones:

```
b: the file state module has no function "managd"; it provides absent,
   append, blockreplace, comment, copy, directory, line, managed,
   prepend, recurse, rename, replace, serialize, symlink, touch, uncomment
```

and a misspelt module is refused by name. All such errors in a tree are
collected and reported together, not one per run.

## Files that build on files

### `include`

A file can pull another file's states in:

```yaml
include:
  - webserver.nginx
  - webserver.sites
```

Names are dotted paths from the root of the tree: `webserver.nginx` is
`webserver/nginx.sls`. If a directory has an `init.sls`, the directory
name alone refers to it, so `include: [webserver]` reads
`webserver/init.sls`. Includes are resolved before anything runs, so
states in an included file can be required by name, as if they were in
the same file. An include cycle is reported as a warning.

### `extend`

`extend` changes a declaration that lives in a file you included, without
editing it. Here is the FreeBSD-only tuning file from the example tree:

<!-- example: handbook/webserver/states/webserver/bsd_tuning.sls -->
```yaml
include:
  - webserver.nginx

extend:
  nginx_service:
    service.running:
      - require:
        - sysctl: somaxconn

somaxconn:
  sysctl.present:
    - name: kern.ipc.somaxconn
    - value: '4096'
```

`nginx_service` is declared in `webserver/nginx.sls`. After `extend`,
on a FreeBSD web server it also requires `somaxconn`. The compiled run
shows the merge (requisites are *added to*, other arguments are
replaced):

```yaml
- __id__: nginx_service
  state: service
  fun: running
  name: nginx
  enable: true
  __requisites__:
  - require: 'pkg: nginx, sysctl: somaxconn'
  - watch: 'file: nginx_conf, file: site_www, file: site_api'
```

`extend` of an ID that does not exist is an error. It is the right tool
when a *site-specific* file needs to adjust a *shared* one — much cleaner
than copying the shared file and editing the copy.

### `exclude`

`exclude` removes a state, or a whole SLS file, from the run:

```yaml
include:
  - helper

exclude:
  - id: helper_state
```

The excluded ID does not run. Excluding a whole file is
`- sls: helper`.

### The top file

Which SLS files apply to which machines is not decided inside the SLS
files but in `top.sls`. See [chapter 10](10-top-and-files.md).

## Options every state accepts

Besides its own arguments, every state accepts these. The ones marked
**(shown)** were run and their output is in this chapter or the guided
tour.

| Option | Effect |
|---|---|
| `require`, `watch`, `onchanges`, `onfail`, `prereq`, `use`, `listen` and their `_in` / `_any` forms | Ordering and dependency ([chapter 6](06-requisites.md)). |
| `unless` | Skip the state if this command succeeds, or if this function returns true. **(shown)** |
| `onlyif` | Skip the state unless this command succeeds. **(shown)** |
| `creates` | Skip the state if this path exists. **(shown)** |
| `names` | Expand into one state per name. **(shown)** |
| `order` | `order: 1` runs it first; `order: last` runs it last. Honoured before declaration order. **(shown)** |
| `failhard` | If this state fails, abort the whole run. States after it report `not attempted`. **(shown)** |
| `retry` | Try again on failure: `attempts` and `interval`. **(shown)** |
| `timeout` | Kill a command that runs longer than this many seconds. **(shown)** |
| `onchanges_in`, `watch_in`, `require_in` and the like | Point the dependency the other way. |
| `check_cmd` (on `file.managed`) | A command that validates the new file before it replaces the old. |

`unless` and `onlyif` take a command (a string, or a list) or a structured
function call, which needs no shell at all and is preferred. With a list,
`unless` skips the state if **any** command succeeds, and `onlyif` runs it
only if **every** command succeeds. Both were run to check:

| `unless` list | `onlyif` list | Result |
|---|---|---|
| two commands that succeed | | skipped |
| one succeeds, one fails | | skipped |
| | one succeeds, one fails | skipped |

The structured form:

```yaml
create_only_if_missing:
  file.managed:
    - name: /etc/app/license
    - contents_pillar: app:license
    - unless:
      - fun: file.file_exists
        args: [/etc/app/license]
```

A skipped state reports `result: True`, no changes, and says why
(`skipped because its unless condition was met`). It counts as
converged.

> **Not built:** the state options `runas` and `umask` on a *state*
> (as opposed to the `runas` argument of a function that has one, such
> as `cmd.run`) are accepted and have no effect, and `parallel: True` runs
> states in order with a warning. These are recorded in the
> [ledger](../DIVERGENCE.md) (sections 4.3 and 4.4).

## The `test` module: states for learning and plumbing

The `test` state module changes nothing and exists for demonstrating and
testing trees:

| State | Does |
|---|---|
| `test.succeed_without_changes` | Always succeeds, reports no change. |
| `test.succeed_with_changes` | Always succeeds, reports a change. |
| `test.fail_without_changes` | Always fails. |
| `test.nop` | Does nothing. |

They are what you use to check an ordering or a requisite chain without
touching a machine.

## The modules you will use most

halite ships a large library of state modules. The ones nearly every tree
begins with:

| Module | Manages | Typical functions |
|---|---|---|
| `file` | Files, directories, links, content edits | `managed`, `directory`, `line`, `replace`, `blockreplace`, `append`, `symlink`, `recurse`, `absent` |
| `pkg` | Packages (whatever the platform's manager is) | `installed`, `latest`, `removed`, `purged` |
| `service` | Services (systemd, rc.d, launchd, …) | `running`, `dead`, `enabled`, `disabled` |
| `user`, `group` | Accounts and groups | `present`, `absent` |
| `cmd` | Arbitrary commands, guarded | `run`, `script` |
| `cron` | Scheduled jobs | `present`, `absent` |
| `git` | Repository checkouts | `latest` |
| `archive` | Unpacking archives | `extracted` |
| `sysctl`, `sysrc`, `kmod` | Kernel parameters, rc settings, modules | `present` |
| `mount` | Filesystem mounts | `mounted`, `unmounted` |
| `host` | `/etc/hosts` entries | `present`, `absent` |
| `ssh_auth`, `ssh_known_hosts`, `sudo` | SSH keys and sudoers | `present`, `absent` |
| `firewall`, `iptables`, `nftables`, `firewalld` | Firewalls | `allowed`, `denied`, `enabled`, chains and rules |
| `x509` | Certificates and keys | per function |

The full list for your build is `halite-node call sys.list_state_modules
--local`, and [chapter 11](11-modules.md) describes the library as a
whole, the platform differences, and the evidence level of each module.

## Running states

There are two directions to run in. From a node's own command line
(`--local` for no hub):

| Command | Does |
|---|---|
| `halite-node state apply` | Apply the **highstate**: everything the top file assigns this node. |
| `halite-node state apply web.nginx common` | Apply only the named SLS files. |
| `halite-node state sls web.nginx` | Same, spelt as a state-only command. |
| `halite-node state highstate` | Alias for `apply` with no arguments. |
| `halite-node state apply --test` | Rehearse. |

and from the hub, at nodes, covered in [chapter 14](14-remote-execution.md):

```sh
halite-hub run 'web*' state.apply
halite-hub run 'web*' state.apply web.nginx --test
```

Pick the output with `--out`:

| `--out` | Gives |
|---|---|
| `nested` (default) | The readable block-per-state report. |
| `json` | The full return, one object keyed by `module_\|-id_\|-name_\|-function`. Stable. |
| `yaml` | The same, as YAML. |
| `txt` | One line per top-level key; a nested value is printed as compact JSON. |
| `quiet` | Nothing printed; only the exit status. |

## Looking inside the compiler

Applying a state goes through stages, and halite shows each. When a tree
does something you did not expect, work forward through them:

| Command | Shows |
|---|---|
| `halite-node lint FILE` | Whether a file *renders and parses*: templates and YAML only. Catches syntax, tabs, duplicate keys, YAML-boolean warnings. |
| `halite-node state show_top` | Which SLS files the top file assigns this node. |
| `halite-node state show_states` | The IDs that will run, in order. |
| `halite-node state show_highstate` | The assembled structure: every file rendered, includes merged, extends applied. |
| `halite-node state show_lowstate` | The final, flat, ordered list with all requisites resolved: **exactly what runs**. |
| `halite-node state show_sls NAME` | One SLS file's assembled content. |

`lint` does not know about modules, so it cannot catch `file.managd`;
`show_lowstate` (or `apply --test`) compiles the tree and can. Use both:
`lint` in an editor hook for fast feedback, `show_lowstate` in CI.

Give `halite-node lint` the roots (`--local --file-root DIR --pillar-root
DIR`) when the file imports another, as the example tree's
`{% from "webserver/map.jinja" import nginx with context %}` does; without
roots there is nothing to resolve the import against. `halite-hub lint`
takes any number of files (not a directory) and is the one to point at
files that import nothing, such as a top file or a policy. For auditing
an entire tree before migrating it, see
[the migration chapter](../migrating-from-salt.md).

To see what a template produced, ask for the rendered text with line
numbers pointing back at the source:

```sh
halite-node lint states/webserver/nginx.sls --rendered --local \
  --file-root states --pillar-root pillar
```

## Reading a failure

A state that fails prints `Result: False` and a comment that says why.
The run continues unless you set `failhard`, and states that depended on
the failed one report that they were not run:

```
          ID: slow
      Result: False
     Comment: The command could not be run: sleep 3 timed out and was killed
          ID: never_runs
      Result: False
     Comment: This state was not attempted: the run was aborted by failhard on fail_here.
```

The exit status is `1`. Run again with `--log-level debug --log-fmt
console` for the compiler's reasoning. If the output is long, `--out
json` plus a tool that filters on `result == false` finds the failures.

## Key ideas

- A state is **ID → module.function → arguments**; the ID is unique in
  the whole tree and the `name` defaults to it.
- SLS files are YAML. **Quote file modes**; **quote `yes`/`no`**; spaces
  only. halite refuses or warns about each trap.
- `include` pulls files in; `extend` adjusts what you included;
  `exclude` removes; the **top file** assigns SLS files to nodes.
- `unless`, `onlyif`, `creates`, `order`, `failhard`, `retry`, `timeout`
  are available on every state.
- `lint` checks syntax; `show_lowstate` shows exactly what will run;
  `--test` rehearses; the module reference is generated from the build.

Next: [requisites and ordering](06-requisites.md).
