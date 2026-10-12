# The life of a highstate

<!-- abstract: A walk through the source from `halite-node state apply` to the exit status: set up, resolve the top file, render, include, extend, expand, resolve requisites, order, validate, execute, report. -->
<!-- kind: concept -->
<!-- index: life of a highstate; compiler; CompileHighstate; resolveTop; loadSLS; applyExtends; finish; buildChunks; resolveRequisites; order; validate; Runner.Run; evaluateRequisites; evaluateGates; high state; low state; Chunk; render pipeline; Highstate formatting; exit code -->

The state compiler is the heart of the project's first goal: that real trees keep
working. This is the path of

```sh
halite-node state apply --local --file-root states --pillar-root pillar
```

through the source, from files on disk to an exit status. Ten stages, in the order
they happen, each of which you can stop after with a `show_*` command
([chapter 5](05-states.md)).

```
 files ─▶ top ─▶ render ─▶ parse ─▶ include ─▶ extend/exclude ─▶ names ─▶ requisites ─▶ order ─▶ validate ─▶ RUN ─▶ report
         show_top      (lint)       show_highstate                       show_lowstate (ordered)        show_states        exit 0/2/1
```

## 0. Setting up: `cmd/halite-node/commands.go`

`runState` dispatches the subcommand; `runStateFunction` builds the node (`setup`),
compiles pillar (`n.compilePillar`, from the hub, or locally with `--local`), and
makes a `state.Compiler` with `n.stateCompiler(p, jobID)`, which passes the
environment, grains, pillar, the `salt` dispatcher, the strictness of undefined
names, the renderer defaults and the sandbox engine. The same compiler serves a
hub-dispatched `state.apply`; only where the tree and pillar come from differs.

## 1. The top file: `internal/state/top.go`

`CompileHighstate` begins with `resolveTop`, which reads `top.sls` from each
environment (`top_file_merging_strategy`), renders it like any SLS file, and
evaluates each target expression against *this node* with `internal/target`: the same
matchers a command-line target uses, as `compileTopTarget`/`matchTop`. The result is,
per environment, the ordered list of SLS names this node gets. `state show_top` prints
exactly this.

## 2. Render: `internal/render`

For each SLS name, `loadSLS` obtains the source through the `Loader` (the file
server on a node with a hub, a directory loader for `--local`) and renders it:
`render.Render(src, opts)` parses the optional shebang (`#!jinja|yaml`), runs the
**template stage** (`internal/template`, with `grains`, `pillar`, `salt`, `opts`, `id`,
`sls`, the path helpers and strict undefined) and then the **serializer stage**
(`internal/yaml`), with the source-position mapping that lets an error in a templated
file point at the line the person wrote. By default this runs in the **render
sandbox**: an unprivileged child process (`internal/rendersandbox`) that returns
data, never a callable. `lint` stops here.

## 3. Parse into declarations: `internal/state/highstate.go`

The rendered `value.Map` becomes `Decl`s, one per state ID, each holding `FuncDecl`s
(`module.function` and its argument list), by `parseSLS`/`parseDecl`/`parseFuncDecl`.
`include`s are collected (`parseIncludes`), `exclude`s (`parseExcludes`), and `extend`
blocks are held aside. A duplicate ID across two SLS files is an error naming both
(`HighState.add`).

## 4. Includes: `loadSLS` again

`loadSLS` is depth-first: an included file is loaded, and its own includes, before
the file that included it, which is why included states run first. It keeps the stack
so that a cycle is reported as a path, not a recursion depth. `state show_highstate`
prints the assembled `HighState` after all includes.

## 5. Extend and exclude: `applyExtends`, `applyExcludes`

After every file is loaded, the pending `extend`s are applied over the assembled
high state (requisites are added to, other arguments replaced; extending an ID that
does not exist is an error), and `exclude`s remove IDs or whole SLS files.

## 6. The low state: `finish` and `buildChunks`

`finish` (`compile.go`) is the rest of the pipeline, in this order:

```go
c.resolveInverseRequisites(out)          // every require_in / watch_in … becomes the forward form
chunks = buildChunks(d, &out.Diags)      // lowstate.go: expand `names`, one Chunk per name
resolveRequisites(chunks, &out.Diags)    // requisite.go: refs -> chunk indexes; `sls:` expands to its members
applyUse(chunks, &out.Diags)             // `use` copies arguments
c.validate(chunks, out)                  // every module, function, argument and type exists and is right
out.Low = order(chunks, &out.Diags)      // order.go: stable topological sort
```

`buildChunks` makes each `Chunk`, the executable unit (`__id__`, `__sls__`, the
module and function, the name and arguments, the requisites, and the options
`order`, `failhard`, `retry`, `unless`, `onlyif`, `creates`, …). `order` is a stable
topological sort with declaration order as the tie-break and `order: first|last|N`
honoured first; a cycle is reported as a path (`reportCycle`, `renderCycle`:
`a -> require -> b -> require -> a`). `validate` collects **every** error before
reporting, so a large tree is not fixed one error per run.

This is the stage `show_lowstate` prints, and the one a CI check should run for each
platform ([chapter 16](16-fleet-operations.md)).

## 7. Execution: `internal/runner/runner.go`

`applyStates` (in `commands.go`) hands the ordered `[]*state.Chunk` to
`runner.Run`. For each chunk in order, `Run`:

1. **Evaluates the requisites** (`evaluateRequisites`): has a `require` target
   failed (then skip, naming it)? did an `onchanges` target change? did an `onfail`
   target fail? For `prereq`, `prereqWouldChange` runs the target in test mode first.
2. **Evaluates the gates** (`evaluateGates` → `evalCondition`): `unless`, `onlyif`,
   `creates`, as commands or structured function calls. A skipped state is
   `result: true` with no changes and the reason.
3. **Calls the state function** registered in `internal/states` (the implementations
   are in `internal/builtin`), with a `chunkContext` that carries the test flag,
   grains, pillar and the execution dispatcher. Each state function's contract is
   check-then-act and a test mode that changes nothing
   ([chapter 4](04-idempotency.md)), and `internal/states/conformance.go` is the
   shared harness that holds every one to it.
4. Applies `retry`, `timeout`, `failhard`, and the `watch` reaction
   (`ModWatch`) for states that define one; `listen` reactions run at the end.
5. Records a `StateResult` in the `module_|-id_|-name_|-function` shape.

A panic in a module is recovered into a failed result, and a state that returns an
error stops only itself (and what depends on it), unless `failhard`.

## 8. Reporting and the exit status

`RunResult` knows whether anything `Failed()`, `Changed()`, and its `RetCode()`.
`runner.Highstate` (`highstate.go`) renders the per-state text block in the format
people read, and `--out json|yaml|quiet` use the structured results. The exit status
is the one of [chapter 46](46-returns-and-exit-codes.md): `1` if any state failed,
`0` if something changed, `2` if nothing did. In a hub-dispatched run the same
`RunResult` becomes the job return, and the node emits the run's events.

## What to notice

- **One compiler, two places.** The node compiles its own states against pillar
  the hub made, so the hub never becomes the bottleneck, and `--local` is the *same
  code*, not a mode with different behaviour.
- **The stages are individually visible.** Every arrow above has a `show_*`
  command, which is the design's answer to "why did it do that?"
- **Errors are positions, and they are collected.** The value model carries source
  positions on mapping entries so that a diagnostic points at the file the person
  wrote, and the compiler reports everything it found at once.
- **Ordering is a graph problem solved once.** Execution never reasons about
  dependencies; the runner walks a list the compiler already proved acyclic.
- **The contract lives in the harness.** A new state function that does not honour
  test mode fails `internal/states`' conformance cases.

## Key ideas

- `state apply` = setup → **top** → **render** (sandboxed) → **parse** →
  **include** → **extend/exclude** → **inverse requisites, chunks, requisites,
  `use`, validate, order** → **run** → **report**.
- The low state (`[]*state.Chunk`) is the contract between compiler and runner.
- The runner evaluates requisites and gates per chunk and calls the registered state
  functions; test mode and idempotency are enforced by a shared harness.

Next: the generated package reference begins with
[the language](gen-code-language.md).
