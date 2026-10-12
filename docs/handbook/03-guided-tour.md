# A guided tour

<!-- abstract: Ten minutes, one machine, no server: describe a machine, rehearse the change, apply it, apply it again, and watch drift being repaired. -->
<!-- kind: tutorial -->
<!-- index: tutorial; playground; local mode; test mode; exit codes; drift; convergence; halite-node state apply -->

In this chapter you will configure a machine with halite for the first
time, using no server and no special privileges. By the end you will
have seen every idea in Part II in action: a described state, a
rehearsal, an apply, a second apply that does nothing, drift being
repaired, a template, pillar data, and a dependency between two states.

You need `halite-node` on your path and a shell. Installing it from a
release archive or from source takes a minute and is covered in
[the installation runbook](31-install-nodes.md); the short version is
`make build` and then copy `bin/halite-node` somewhere on your `PATH`.

Nothing here needs root. The tour only writes into
`/tmp/halite-playground`, which is why it is called the playground.

## What "local mode" is

`halite-node` can work completely alone. With `--local` it reads states
and pillar from directories on the machine it is running on, instead of
asking a hub for them. This is the quickest way to learn halite, and it
is a real mode you will use in production for bootstrapping, for
air-gapped machines, and for testing a tree before it is committed.

The playground tree ships with halite, in `contrib/examples/handbook/playground`
in the source and `examples/handbook/playground` in a release archive.
Copy it somewhere you own:

```sh
mkdir -p ~/halite-tour
cp -R examples/handbook/playground/* ~/halite-tour/
cd ~/halite-tour
find . -type f
```

```
./states/top.sls
./states/playground.sls
./pillar/top.sls
./pillar/playground.sls
```

Every command below passes the same three flags. Put them in a variable
so the commands stay short. In bash or zsh:

```sh
T="--local --file-root $PWD/states --pillar-root $PWD/pillar"
```

In fish, use `set T --local --file-root $PWD/states --pillar-root $PWD/pillar`.
The rest of the tour writes `$T`.

## Step 1: Read the tree

A tree has two halves. The **states** half says what should be true. It
starts with a **top file**, which says which states apply to which
machines:

<!-- example: handbook/playground/states/top.sls -->
```yaml
base:
  '*':
    - playground
```

`base` is the **environment** (the default one). `'*'` is a **target**
matching every node. `playground` is the name of an SLS file, which is
`states/playground.sls`. So: *every node gets the `playground` states*.

Here is that file:

<!-- example: handbook/playground/states/playground.sls -->
```yaml
{% set root = pillar['playground']['dir'] %}

playground_dir:
  file.directory:
    - name: {{ root }}
    - mode: '0755'

playground_motd:
  file.managed:
    - name: {{ root }}/motd
    - mode: '0644'
    - require:
      - file: playground_dir
    - contents: |
        This directory is managed by halite.
        Host:  {{ grains['id'] }}
        OS:    {{ grains['os'] }} {{ grains['osrelease'] }}
        Owner: {{ pillar['playground']['owner'] }}

playground_changelog:
  cmd.run:
    - name: echo "motd changed" >> {{ root }}/changes.log
    - onchanges:
      - file: playground_motd
```

Read it slowly, because it contains most of the vocabulary.

- The first line is a **template** statement. `{% set ... %}` stores a
  value from **pillar** in a variable. Anything in `{{ }}` or `{% %}` is
  Jinja, evaluated before the YAML is read.
- `playground_dir:` is an **ID**: a name you choose, unique across the
  whole tree. Below it, `file.directory:` is the **state function** (module
  `file`, function `directory`), followed by its **arguments** as a list.
  This state says: *the directory exists, with mode 0755*.
- `playground_motd` says: *the file `motd` exists in that directory with
  this content and mode*. Its content uses `{{ grains['id'] }}` — a
  **grain**, a fact about the machine — and the owner from pillar.
- `require: - file: playground_dir` is a **requisite**: this state runs
  *after* the directory state, and only if it succeeded.
- `playground_changelog` runs a command, but only `onchanges` of
  `playground_motd`: when the motd actually changed. This is how you make
  something happen *because of a change*, rather than every time.

The pillar half provides the data:

<!-- example: handbook/playground/pillar/playground.sls -->
```yaml
playground:
  dir: /tmp/halite-playground
  owner: the documentation team
```

and its own top file (`pillar/top.sls`) says every node receives it.

## Step 2: Look at the machine

halite has already collected facts about the machine you are on. These
are the **grains**:

```sh
halite-node grains get os --local
halite-node grains item os osrelease --local
```

```
system76-pc: Ubuntu
system76-pc:
    os: Ubuntu
    osrelease: 24.04
```

`halite-node grains items --local` prints all of them, a few hundred
lines. The first column of every answer is the **node ID**, which here
defaults to the host name. Look at the pillar the node will receive:

```sh
halite-node pillar items $T
```

```
system76-pc:
    playground:
        dir: /tmp/halite-playground
        owner: the documentation team
```

## Step 3: Check the tree before using it

`lint` renders a file and parses it without running anything. It is how
you find syntax errors in an editor save, a commit hook or CI:

```sh
halite-node lint states/playground.sls $T
```

```
states/playground.sls: renders and parses; pipeline jinja|yaml
```

Now ask halite to show you the plan. These three commands each stop at a
different stage of compiling the tree:

```sh
halite-node state show_top $T
halite-node state show_states $T
halite-node state show_lowstate $T --out yaml
```

```
base:
    - playground
```

```
- playground.playground_dir
- playground.playground_motd
- playground.playground_changelog
```

The third prints the **low state**: the flat, fully ordered list of
every state with all templates resolved and every requisite worked out.
This is exactly what will run. It is the single most useful thing to
read when a tree surprises you:

```yaml
- __id__: playground_motd
  __sls__: playground
  __env__: base
  __run_num__: 1
  state: file
  fun: managed
  name: /tmp/halite-playground/motd
  mode: '0644'
  contents: "This directory is managed by halite.\nHost:  system76-pc\nOS:    Ubuntu 24.04\nOwner: the documentation team\n"
  __requisites__:
  - require: 'file: playground_dir'
```

(The output shows the first state's neighbours too; `__run_num__` is the
order in which they will run.)

## Step 4: Rehearse

`--test` runs every state in **test mode**. Nothing changes. Each state
reports what it *would* do:

```sh
halite-node state apply $T --test
```

```
local:
----------
          ID: playground_dir
    Function: file.directory
        Name: /tmp/halite-playground
      Result: None
     Comment: The directory /tmp/halite-playground would be created.
     Started: 11:31:35.614737
    Duration: 0.018 ms
     Changes:
              ----------
              directory:
                  ----------
                  new:
                      created
                  old:
                      None
----------
          ID: playground_motd
    Function: file.managed
        Name: /tmp/halite-playground/motd
      Result: None
     Comment: /tmp/halite-playground/motd would be created: its contents, its mode.
     Changes:
              ----------
              diff:
                  --- /tmp/halite-playground/motd (current)
                  +++ /tmp/halite-playground/motd (managed)
                  @@ -1,0 +1,4 @@
                  +This directory is managed by halite.
                  +Host:  system76-pc
                  +OS:    Ubuntu 24.04
                  +Owner: the documentation team
...
Summary for local
------------
Succeeded: 3 (unchanged=3, changed=3)
Failed:    0
```

(The output is shortened here; yours also lists the third state.)

Learn to read this block, because you will read it thousands of times:

| Field | Meaning |
|---|---|
| `ID` | The name you gave the state. |
| `Function` | Module and function. |
| `Name` | What it acts on: a path, a package, a service. |
| `Result` | `True` — satisfied (nothing wrong); `False` — failed; `None` — *in test mode, this would change something*. |
| `Comment` | A sentence in plain language. |
| `Changes` | The structured difference between old and new. Empty when nothing needed doing. |

In test mode the result is `None` precisely when a real run would make a
change. That is a promise that every state function is tested to keep
([chapter 4](04-idempotency.md)); it is what lets you rehearse a change on
production with confidence.

## Step 5: Apply

Drop `--test`:

```sh
halite-node state apply $T
echo "exit status: $?"
```

The output is the same shape as the rehearsal, with `Result: True` and
`was created` in place of `would be created`. Look at the result:

```sh
cat /tmp/halite-playground/motd
cat /tmp/halite-playground/changes.log
```

```
This directory is managed by halite.
Host:  system76-pc
OS:    Ubuntu 24.04
Owner: the documentation team
motd changed
```

The log has one line: the file changed, so the `onchanges` state ran.

## Step 6: Apply again — and nothing happens

```sh
halite-node state apply $T
echo "exit status: $?"
```

```
          ID: playground_motd
    Function: file.managed
        Name: /tmp/halite-playground/motd
      Result: True
     Comment: /tmp/halite-playground/motd is already in the requested state.
     Changes:
----------
          ID: playground_changelog
    Function: cmd.run
      Result: True
     Comment: This state was skipped because none of its onchanges requisites reported changes.

Summary for local
------------
Succeeded: 3
Failed:    0
exit status: 2
```

Every `Changes:` is empty. `changes.log` did not grow. The exit status is
`2`, which halite uses to mean *already converged, nothing to do*. That
run is **idempotent**: applying the same description to a machine that
already matches it does nothing. Everything else rests on this.

The exit statuses of `state apply` are:

| Status | Meaning |
|---|---|
| `0` | Success, and something changed. |
| `2` | Success, and nothing needed to change. |
| `1` | A state failed, or the tree did not compile. |
| `64` | The command line was not understood. |

A scheduled job that treats `2` as success will not alert you about a
machine that simply had nothing to do.

## Step 7: Drift

Now be the colleague who edits a managed file by hand:

```sh
echo "tampered" > /tmp/halite-playground/motd
halite-node state apply $T --test
```

```
          ID: playground_motd
      Result: None
     Comment: /tmp/halite-playground/motd would be updated: its contents.
     Changes:
              ----------
              diff:
                  --- /tmp/halite-playground/motd (current)
                  +++ /tmp/halite-playground/motd (managed)
                  @@ -1,1 +1,4 @@
                  -tampered
                  +This directory is managed by halite.
                  +Host:  system76-pc
                  +OS:    Ubuntu 24.04
                  +Owner: the documentation team
```

The rehearsal found the drift and shows you the exact difference.
Apply it:

```sh
halite-node state apply $T
cat /tmp/halite-playground/changes.log
```

The file is repaired, and `changes.log` has a second line, because the
file changed again and the dependent state fired again. Run a rehearsal
across a fleet on a schedule and you have **drift detection**; run an
apply and you have **drift repair**.

## Step 8: Change the data, not the state

Edit `pillar/playground.sls` and change the owner:

```sh
sed -i 's/the documentation team/the platform team/' pillar/playground.sls
halite-node state apply $T
```

(On BSD or macOS `sed -i ''` — or just open the file in an editor.)

Only the file state changes, and its diff is one line:

```
                  -Owner: the documentation team
                  +Owner: the platform team
```

The directory state stays quiet, and `changes.log` gets a third line.
The **state** said how; the **pillar** said what. Most day-to-day changes
in a real tree are pillar edits like this one.

## Step 9: See how errors look

halite catches whole classes of mistake that its predecessors let
through silently. Make two on purpose in a scratch copy:

```sh
cp -R ~/halite-tour ~/halite-tour-bad && cd ~/halite-tour-bad
T2="--local --file-root $PWD/states --pillar-root $PWD/pillar"

# 1. An unquoted file mode.
sed -i "s/mode: '0644'/mode: 0644/" states/playground.sls
halite-node state apply $T2 --test
```

```
halite: state compilation failed with 1 error(s):
  states/playground.sls:9:3 [playground_motd]: file.managed: argument "mode": must be a quoted string, found the integer 420; write it as '0420' if you meant that mode, or '0644' if you meant the number 420
```

YAML reads an unquoted `0644` as an octal number — decimal 420 — and
older tools then write the wrong permissions. halite refuses and
explains.

```sh
# 2. A misspelt pillar key.
cp ~/halite-tour/states/playground.sls states/
sed -i "s/\['owner'\]/['ownr']/" states/playground.sls
halite-node state apply $T2 --test
```

```
halite: state compilation failed with 1 error(s):
  states/playground.sls:18:39: ownr is undefined (mapping has no key ownr); use `| default(...)`, `is defined`, or `.get(name, fallback)` to make it optional
```

A value that does not exist is an error with a file and line, not an
empty string quietly written into a file. Both runs exited with status
`1` and changed nothing.

## Step 10: Ask a question

Everything so far was *configuration management*: describing an end state.
The other half is *asking*. `halite-node call` runs one function now:

```sh
halite-node call test.ping --local
halite-node call test.echo hello --local
halite-node call status.uptime --local
halite-node call cmd.run 'uname -a' --local
```

```
system76-pc: True
system76-pc: hello
system76-pc:
    seconds: 58095
    since: 2026-10-11T02:23:57Z
    human: 16h8m0s
```

On a fleet the same functions run as
`halite-hub run '*' status.uptime`: the same functions, aimed at many
machines ([chapter 14](14-remote-execution.md)). Ask halite what any
function does and what arguments it takes:

```sh
halite-node call sys.doc cmd.run --local
halite-node call sys.state_doc file.managed --local
halite-node call sys.list_modules --local
```

The reference for every function is generated from the same signatures
and is in [the module reference](../modules.md).

## Clean up

```sh
rm -rf /tmp/halite-playground ~/halite-tour ~/halite-tour-bad
```

## What you just did

| You | The idea | Chapter |
|---|---|---|
| Described a directory, a file and a command | **States** | [5](05-states.md) |
| Said one runs after another | **Requisites** | [6](06-requisites.md) |
| Wrote `{{ grains['id'] }}` | **Templates** and **grains** | [7](07-templates.md), [8](08-grains.md) |
| Moved the owner into `pillar/` | **Pillar** | [9](09-pillar.md) |
| Chose which nodes read which files | **The top file** | [10](10-top-and-files.md) |
| Rehearsed with `--test` | **Test mode** | [4](04-idempotency.md) |
| Applied twice | **Idempotency** | [4](04-idempotency.md) |
| Tampered and repaired | **Drift** | [4](04-idempotency.md) |
| Called `status.uptime` | **Execution functions** | [14](14-remote-execution.md) |

Next: [idempotency](04-idempotency.md), the idea that makes all of this safe.
