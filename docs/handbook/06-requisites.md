# Requisites and ordering

<!-- abstract: How states depend on each other: require, watch, onchanges, onfail, prereq, use, listen, the _in forms, and the rules that decide the final order. -->
<!-- kind: concept -->
<!-- index: requisite; require; watch; onchanges; onfail; prereq; use; listen; require_in; watch_in; require_any; order; failhard; dependency; ordering; cycle; cmd.wait; mod_watch; rollback -->

States are independent statements: "this package is installed", "this
file looks like that". But real machines have *relationships*. The
configuration file needs the package installed first. The service should
restart when the file changes. A cleanup should happen only if a deploy
failed.

A **requisite** is a line inside a state that names another state and says
how the two are related. This chapter covers all of them. Every example
runs in the test module, so you can try each yourself; the file is
`contrib/examples/handbook/requisites/states/requisites.sls`.

## How the order is decided

Without any requisites, states run **in the order they are written**: the
order within a file, and files in the order the top file lists them, with
included files running before the file that includes them.

Requisites then adjust that. The final order is a **stable topological
sort**: halite builds the graph of dependencies, and where two states
have no constraint between them, they keep their written order. Two
consequences worth knowing:

- Adding a requisite never reshuffles unrelated states.
- An explicit `order` option is honoured before the tie-break: `order: 1`
  runs first, `order: last` runs last.

`halite-node state show_states` prints the final order, and
`show_lowstate` prints each state with its requisites resolved. Look there
first when something ran in an order you did not expect.

## The requisites

Here is the whole set, grouped by what they are for:

| You want | Use | One-line meaning |
|---|---|---|
| "do this after that" | `require` | After the target, and only if it succeeded. |
| "...and react when it changed" | `watch` | `require`, plus the state's reaction if the target changed. |
| "only if that changed" | `onchanges` | Run only if the target reported changes. |
| "only if that failed" | `onfail` | Run only if the target failed. |
| "before that, if it will change" | `prereq` | Run before the target, and only if the target *would* change. |
| "same arguments as that" | `use` | Inherit the target's arguments. |
| "react at the end" | `listen` | Like `watch`, but the reaction is deferred to the end of the run. |

Each has an `_in` form that writes the dependency from the other side,
and most have an `_any` form that is satisfied by any one target
instead of all of them. We will take them in turn. The output shown is
from running the example file; the exit status was `1` because the file
contains a state that fails on purpose.

### `require`

```yaml
needs_quiet:
  test.succeed_without_changes:
    - require:
      - test: quiet
```

`require` means: run **after** the named state, and **only if it
succeeded**. If the target failed, this state is not run, and says why:

```
          ID: needs_breaker
      Result: False
     Comment: This state was skipped because breaker (test.fail_without_changes) failed.
```

Note `Result: False`. A state that could not run because a dependency
failed is itself a failure, which is what makes a failure early in a
chain visible at the end. Nothing else is touched: the run continues
with states that do not depend on the failed one, unless `failhard` is
set.

A real example from the web-server tree: the configuration file needs the
package, because the package creates the directory the file goes in.

```yaml
nginx_conf:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - require:
      - pkg: nginx
```

### How a requisite names its target

```yaml
    - require:
      - pkg: nginx          # the state with ID "nginx" whose module is pkg
      - sls: other          # every state in the SLS file "other"
      - nginx_conf          # by ID alone, if that ID is unambiguous
```

The target is the **ID** (not the `name`), qualified by the **module**
name (`pkg`, `file`, `service`, `cmd`). Qualifying with the module is
clearer, and necessary when two states of different modules share an ID.
`sls:` depends on every state in an SLS file at once.

A requisite whose target does not exist is a compile error that names
the state and the target, and all such errors in a tree are reported
together:

```
req.sls:34:14 [either]: require_any names `test: breaker_missing_ok`,
which is not declared in this run
```

### `watch`

`watch` is `require` plus a reaction. When the target **changed**, the
state is told, and a state that knows how to react does so. What
"react" means depends on the function:

| State | Reaction to a watched change |
|---|---|
| `service.running` | restart (or reload, with `reload: True`) the service |
| `cmd.wait` | run the command |
| `module.wait` | call the execution function it names |
| `kernelpkg.latest_wait` | act as `kernelpkg.latest_active` |

(The authoritative list is the states whose entry in
[the module reference](../modules.md) mentions a watch requisite.)

The canonical use is the one in the web-server tree: restart nginx when
its configuration, or any site file, changes.

```yaml
nginx_service:
  service.running:
    - name: nginx
    - enable: True
    - require:
      - pkg: nginx
    - watch:
      - file: nginx_conf
```

On a converged machine, nothing changed, so nothing restarts. The first
run restarts nothing either, if the service was only just started.
`cmd.wait` makes the mechanism visible without a service manager:

```yaml
react_to_changer:
  cmd.wait:
    - name: echo reacted
    - watch:
      - test: changer

react_to_quiet:
  cmd.wait:
    - name: echo reacted
    - watch:
      - test: quiet
```

```
          ID: react_to_changer
      Result: True
     Comment: The command "echo reacted" ran.
          ID: react_to_quiet
      Result: True
     Comment: This command waits for a watch requisite to fire, and none did.
```

> **Warning:** `watch` on a state that has no reaction behaves exactly
> like `require`. In particular **`cmd.run` with a `watch` runs every
> time**, changed or not — the example file showed it. To run a command
> only when something changed, use `cmd.wait`, or (better, and more
> general) `cmd.run` with `onchanges`. A `service.running` with `watch`
> is the one you want for restarts.

### `onchanges`

Run this state **only if** a target reported changes; otherwise skip it,
successfully:

```yaml
only_if_changer_changed:
  test.succeed_with_changes:
    - onchanges:
      - test: changer

only_if_quiet_changed:
  test.succeed_with_changes:
    - onchanges:
      - test: quiet
```

```
          ID: only_if_changer_changed
      Result: True
     Comment: This state reported a synthetic change.
          ID: only_if_quiet_changed
      Result: True
     Comment: This state was skipped because none of its onchanges requisites reported changes.
```

`onchanges` is the most general "act because of a change". It works with
*any* state function (unlike `watch`, which needs a reaction built in),
so it is the right tool for "when the certificate changes, run the
command that rebuilds the bundle", "when the config changes, validate
it", and "when the tree changes, tell someone".

### `onfail`

Run only if a target **failed**. This is how a rollback or an alert is
expressed:

```yaml
rollback:
  test.succeed_with_changes:
    - onfail:
      - test: breaker

no_rollback_needed:
  test.succeed_with_changes:
    - onfail:
      - test: quiet
```

```
          ID: rollback
      Result: True
     Comment: This state reported a synthetic change.
          ID: no_rollback_needed
      Result: True
     Comment: This state was skipped because none of its onfail requisites failed.
```

`onfail_any` and `onfail_all` choose between "any one of the targets
failed" and "all of them did".

### `prereq`

`prereq` is the unusual one. It means: run **before** the target, and
only if the target **would** change. Its use is preparing for a change
that is about to happen — taking a node out of a load balancer before the
service on it is restarted:

```yaml
drain:
  cmd.run:
    - name: /usr/local/bin/lb-drain
    - prereq:
      - file: nginx_conf
```

To know whether `nginx_conf` would change, halite runs it in test mode
first. That works when the target's test mode is honest, and is why
`prereq` on `cmd.run` is flagged:

```
[before_change]: prereq names cmd.run, whose test mode is unreliable;
the changes prediction this prereq depends on may be wrong
```

On a converged machine, the target would not change, and the prereq state
is skipped (`skipped because app_env (file.line) would make no changes`).
Use `prereq` only with targets that have a trustworthy test mode —
`file.*`, `pkg.*`, `service.*` and the other declarative modules.

### `use`

`use` copies the **arguments** of another state of the same kind into this
one, except those this one sets itself:

```yaml
defaults_holder:
  file.managed:
    - name: /tmp/halite-use-a
    - mode: '0600'
    - contents: a

inherits:
  file.managed:
    - name: /tmp/halite-use-b
    - contents: b
    - use:
      - file: defaults_holder
```

The compiled form shows the inherited `mode: '0600'` on `inherits`. It
is useful for "all these files share ownership and mode", but a shared
`context` or a template macro is usually clearer than a chain of `use`.

### `listen`

`listen` is `watch` with the reaction deferred. Where `watch` reacts at
once, `listen` waits until every other state has run:

```yaml
at_the_end:
  test.succeed_with_changes:
    - listen:
      - test: changer
```

```
          ID: at_the_end
     Comment: Listening reaction, run at the end of the state run. This state reported a synthetic change.
```

The point is *one* restart. If twenty files each change a service's
configuration, `watch` could restart the service twenty times;
`listen`, or one `watch` on a group, restarts it once, after the last
change. Use `listen` for services you do not want flapping.

## The `_in` forms: writing the dependency from the other side

Sometimes you cannot, or do not want to, edit the state that needs the
requisite — it is in a shared file, or you are generating states in a
loop. `require_in`, `watch_in`, `onchanges_in`, `onfail_in`, `prereq_in`
and `listen_in` add the requisite *to the target*:

```yaml
declared_first:
  test.succeed_without_changes:
    - require_in:
      - test: declared_second

declared_second:
  test.succeed_without_changes: []
```

compiles to the same thing as a `require` in `declared_second`:

```yaml
- __id__: declared_second
  __requisites__:
  - require: 'test: declared_first'
```

The example tree uses it for an elegant effect. Each site file is
generated in a loop, and each one says `watch_in` the service:

```yaml
site_www:
  file.managed:
    - name: /etc/nginx/sites/www.conf
    - watch_in:
      - service: nginx_service
```

so the service state needs no list of sites, and a site added to pillar
gets a restart for free. In the compiled run the service ends up with
`watch: file: nginx_conf, file: site_www, file: site_api`.

## The `_any` forms

By default a requisite on a list is satisfied only when **all** targets
are. `require_any`, `watch_any`, `onchanges_any` and `onfail_any` are
satisfied when **any one** is:

```yaml
either:
  test.succeed_without_changes:
    - require_any:
      - test: other_one
      - test: other_two
```

## Failure

What a failure does to the rest of a run:

- A state that **fails** is reported `Result: False`.
- A state that `require`s (or `watch`es) it is **skipped**, also
  `Result: False`, with a comment naming the failed dependency.
- States that do not depend on it **still run**.
- `onfail` states that named it **run**.
- With `failhard: True` on the failing state (or globally), the **run is
  aborted**: later states report `not attempted: the run was aborted by
  failhard on <ID>`.

The exit status is `1` for any run in which a state failed.

## Mistakes the compiler catches

**A cycle.** If `a` requires `b` and `b` requires `a`, halite refuses to
run and prints the loop as a path:

```
req.sls:20:3 [a]: requisite cycle: a -> require -> b -> require -> a
```

Compilation collects every problem in the tree and reports them
together, so you are not fixing one per run.

**A missing target**, shown above.

**An unreliable `prereq`**, shown above.

The things it does *not* catch are your logic: a `watch` on a state with
no reaction, or a `require` that points at the wrong ID. Read the
`show_lowstate` output for the state in question: its `__requisites__`
lists exactly what halite believes.

## A worked example

Putting the web-server tree's three states together, with everything
spelt out:

```yaml
nginx:
  pkg.installed: []

nginx_conf:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: salt://webserver/files/nginx.conf.jinja
    - template: jinja
    - require:
      - pkg: nginx

nginx_service:
  service.running:
    - name: nginx
    - enable: True
    - require:
      - pkg: nginx
    - watch:
      - file: nginx_conf
```

```
          nginx
            │ require
            ▼
        nginx_conf ──────────┐
            │                │ watch
            │ require        ▼
            ▼          nginx_service  (restarts if nginx_conf changed)
       nginx_service
```

- First run on a bare machine: install the package, write the config,
  start the service. The service was not running, so there is nothing to
  *re*start.
- You edit the template and apply: the config changes, so the service
  restarts. Nothing else happens.
- You apply again: nothing changes, nothing restarts, exit status `2`.
- The package repository is down: `nginx` fails, the config and service
  are skipped with a comment naming the package, and the run reports `1`.

## Key ideas

- States run in written order; requisites add a dependency graph; ties
  keep written order; `order` overrides.
- `require` — after and only if it succeeded. `watch` — plus react to a
  change. `onchanges` — only if it changed. `onfail` — only if it
  failed. `prereq` — before it, if it would change. `listen` — react at
  the end. `use` — borrow arguments.
- The `_in` forms write the dependency from the other end, which is how
  loops and shared files avoid editing each other.
- **`watch` only does something for states that react** (`service.running`,
  `cmd.wait`); otherwise it is `require`. For "do this because that
  changed", use `onchanges`.
- Cycles, missing targets and unreliable `prereq`s are caught before
  anything runs.

Next: [templates](07-templates.md), which make one description fit many
machines.
