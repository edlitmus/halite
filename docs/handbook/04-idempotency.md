# Idempotency and convergence

<!-- abstract: The idea everything else rests on: declare an end state, change only what differs, do nothing when already correct. -->
<!-- kind: concept -->
<!-- index: idempotency; idempotent; convergence; drift; declarative; imperative; test mode; check-then-act; creates; unless; onlyif; changes; result; exit code 2; dry run; compliance; desired state -->

If you remember one chapter of this handbook, make it this one.

An operation is **idempotent** if doing it twice has the same effect as
doing it once. Setting a thermostat to 20°C is idempotent: set it again
and nothing changes. Pressing an elevator's "up" button is nearly so.
"Add one degree" is not: each press moves further from where you started.

halite is built so that the unit you write — a *state* — is the first
kind. A state does not say "install nginx". It says "nginx **is
installed**". If it is, halite does nothing. If it is not, halite installs
it. Run the same description a hundred times and the machine changes at
most once, on the first run that finds it different.

That sounds like a small convenience. It is the property that makes
everything else in the system possible.

## Why it matters

**You can run it continuously.** If a run could break a machine that was
already right, you would only run it when you meant to change something,
and in between the machine would drift. Because a run on a correct
machine does nothing, you can apply the whole description to every
machine every thirty minutes. Drift is then repaired within thirty
minutes of happening, and changes spread without anyone running anything.

**You can run it after a failure.** If a run stops halfway — the network
dropped, the disk filled, someone pressed Ctrl-C — you just run it again.
Whatever was done is recognised as done; whatever was not is done now.
There is no "how far did it get?" to untangle.

**You can rehearse it.** A state can answer "would you change anything?"
without changing anything, because it must already know how to tell
whether the machine matches. That answer *is* test mode. A fleet-wide
test run is a compliance report: it lists exactly which machines have
drifted and how.

**Reactions can be precise.** "Restart nginx **if its configuration
changed**" is only meaningful if a run that changes nothing reports
nothing. A tool that rewrites the file every time cannot tell you whether
it changed, so it restarts the service every time.

**You can reason about it.** The question "what will happen if I apply
this?" has a short answer: *the differences between this description and
the machine, and nothing else*.

## Declarative and imperative

The two styles, side by side, for the same goal:

```sh
# Imperative: a script of steps. Run it twice and see.
useradd deploy
mkdir /srv/app
echo "export APP_ENV=prod" >> /etc/profile
apt-get install -y nginx
```

On the second run `useradd` fails ("user exists"), `mkdir` fails, the
`echo` appends a **second copy** of the line, and `apt-get` happens to be
fine. Making the script safe means wrapping every step in a check:
`id deploy || useradd deploy`, `[ -d /srv/app ] || mkdir ...`,
`grep -q APP_ENV /etc/profile || echo ...`. You have written, by hand and
badly, what halite does for you.

```yaml
# Declarative: the end state. Run it as often as you like.
deploy:
  user.present: []

/srv/app:
  file.directory: []

app_env:
  file.line:
    - name: /etc/profile
    - content: export APP_ENV=prod

nginx:
  pkg.installed: []
```

Each of those functions begins with a question — *is the machine already
like this?* — and only acts if the answer is no. That is the pattern in
every state module in the library, and the name for it is
**check-then-act**. `file.line` with its default `mode: ensure`, for
instance, looks for the line, adds it if it is missing, and on the next run
reports `The line is already in the requested state` and changes nothing.

## The three answers a state can give

Whatever the state does, it returns the same small structure. Three
fields matter:

| Field | Values | Meaning |
|---|---|---|
| `result` | `True`, `False`, `None` | `True`: the state is satisfied. `False`: it could not be. `None`: only in test mode — a change *would* be made. |
| `changes` | a mapping, possibly empty | What was altered, as structured old/new data. **Empty means nothing was done.** |
| `comment` | text | A sentence for a person. |

So there are exactly three outcomes of a real run:

| Outcome | `result` | `changes` | Example comment |
|---|---|---|---|
| Already right | `True` | empty | `/tmp/x is already in the requested state.` |
| Made right | `True` | non-empty | `/tmp/x was updated: its contents.` |
| Could not | `False` | any | An error message. |

And one more in test mode: `result: None` with `changes` populated with
the *predicted* change.

You saw all of these in [the tour](03-guided-tour.md). In the machine-
readable form, which `--out json` prints, one state's return looks like
this (shortened):

```json
{
  "file_|-playground_motd_|-/tmp/halite-playground/motd_|-managed": {
    "__id__": "playground_motd",
    "__sls__": "playground",
    "__run_num__": 1,
    "name": "/tmp/halite-playground/motd",
    "result": true,
    "changes": {},
    "comment": "/tmp/halite-playground/motd is already in the requested state.",
    "duration": 0.036
  }
}
```

The key is `module_|-id_|-name_|-function`. It is ugly and it is stable;
every report and dashboard written against these returns relies on it.

## Convergence and the exit code

A whole run is **converged** when every state came back "already right".
The command line tells a script which kind of run it was without parsing
anything:

| `halite-node state apply` exits | Meaning |
|---|---|
| `0` | Succeeded, and something changed. |
| `2` | Succeeded, and nothing needed to change — converged. |
| `1` | A state failed, or the tree would not compile. |

So the **first** run on a new machine exits `0` (lots changed) and the
**second** run exits `2`. If a second run *does not* exit `2` there is
something in the tree that is not idempotent, and that is the most
valuable test you can write:

```sh
#!/bin/sh
# idempotency check: apply twice; the second must find nothing to do.
set -u
halite-node state apply --local --out quiet "$@"
first=$?
halite-node state apply --local --out quiet "$@"
second=$?
if [ "$first" -eq 1 ] || [ "$second" -ne 2 ]; then
    echo "not idempotent: first run exited $first, second exited $second" >&2
    exit 1
fi
echo "idempotent: converged after one run"
```

Run it in a throwaway VM or container in CI against every change to your
tree. A tree that takes *two* applications to settle — because one state
creates what a later one needs and the order is wrong — fails this test,
and that is a bug to fix, not a quirk to live with.

## The check in each kind of state

You do not have to write the check. Every state module has one built in,
and it is worth knowing roughly what each compares:

| State | It asks | It acts when |
|---|---|---|
| `file.managed` | Is there a file at that path with this content hash, mode, owner and group? | Any differs. |
| `file.directory` | Does the directory exist with this mode and ownership? | Missing or wrong. |
| `pkg.installed` | Does the package manager report this package (and version) installed? | Absent or wrong version. |
| `service.running` | Does the service manager say it is running? `enabled` — is it enabled at boot? | Stopped, or not enabled when `enable: True`. |
| `user.present` | Does the account exist with this UID, shell, groups and home? | Missing or different. |
| `cron.present` | Is this job in the crontab? | Absent or different. |
| `sysctl.present` | Is this kernel parameter set to this value? | Different. |

The comparison is on the *content*, not on timestamps or the number of
times something ran. A file that has the right bytes but the wrong mode
gets its mode fixed and its contents left alone, and the `comment` says
which: `was updated: its mode`.

## Where idempotency is not free: `cmd.run`

`cmd.run` runs a command. halite cannot know what a command does, so it
cannot know whether running it is necessary. Unless you tell it,
`cmd.run` runs **every time** — and a state that always reports a change
never converges and never lets a scheduled job exit `2`. Watch it happen:

```yaml
naive_append:
  cmd.run:
    - name: echo "line" >> /tmp/halite-guard/naive.txt
```

Every apply runs the command and reports `changed=1`; the file grows by a
line each time. The fix is to give the state a way to tell whether the
work is already done. There are three guards, and their comments, as
halite prints them, show what each does:

```yaml
guarded_creates:
  cmd.run:
    - name: mkdir -p /tmp/halite-guard && echo built > /tmp/halite-guard/built
    - creates: /tmp/halite-guard/built

guarded_unless:
  cmd.run:
    - name: echo ran >> /tmp/halite-guard/unless.txt
    - unless: test -f /tmp/halite-guard/unless.txt

guarded_onlyif:
  cmd.run:
    - name: echo ran >> /tmp/halite-guard/onlyif.txt
    - onlyif: test -d /nonexistent
```

| Guard | Runs the command when | Skipped with the comment |
|---|---|---|
| `creates: <path>` | the path does **not** exist | `skipped because /tmp/halite-guard/built already exists.` |
| `unless: <command>` | the guard command **fails** (non-zero) | `skipped because its unless condition was met.` |
| `onlyif: <command>` | the guard command **succeeds** (zero) | `skipped because its onlyif condition was not met.` |

The wording is easy to mix up, so: `unless` and `onlyif` take a command and
test its exit status; `unless: X` means "run this *unless* X is true".
`creates` is the cheapest, and the best when a command produces a file.

A guarded state is skipped with `result: True` and **no changes**, so it
does not break convergence. The very best use of `cmd.run` is the third
tool, `onchanges` (from [the tour](03-guided-tour.md)): the command runs
only when another state reported a change.

```yaml
reload_after_config:
  cmd.run:
    - name: nginx -s reload
    - onchanges:
      - file: /etc/nginx/nginx.conf
```

When you can, prefer a dedicated state function to `cmd.run`. There is a
module for packages, services, users, groups, cron, mounts, firewall
rules, sysctls, kernel modules and several hundred other things, and each
one has its idempotency built in and a test mode that is checked
([chapter 11](11-modules.md)). `cmd.run` is the escape hatch for the rest.

> **Note:** because halite cannot know what a command would do,
> `cmd.run` in test mode is flagged **unreliable**:
> `halite-node call sys.doc cmd.run --local` says so in its output. Test
> mode reports that the command *would be run*, which is true, but it
> cannot say whether running it would change anything. Another reason to
> guard it.

## Test mode, precisely

`--test` is not a courtesy. It is a **contract** that every state
function must keep, and the project checks it with a shared test harness
that every function must pass:

1. It must **change nothing**.
2. It must return `result: None` if and only if a real run *would* make a
   change, and `result: True` if the machine is already right.
3. It must fill `changes` with the change it would make.
4. It must write a `comment` a person can read.

Compare this with its predecessor, where test mode was an advisory flag
that a fair number of modules ignored. Here, when a rehearsal says
`None`, a run will change it; when it says `True`, a run will not.

Functions that run arbitrary code (`cmd.run` and friends) cannot honour
point 2 honestly, and say so (`test mode is unreliable`). This matters for
one requisite, `prereq`, which depends on knowing whether a change *would*
happen; halite logs a warning when `prereq` names a state whose
prediction it cannot trust (`prereq names cmd.run, whose test mode is
unreliable`) — see [chapter 6](06-requisites.md).

You can ask for a rehearsal in three places:

```sh
halite-node state apply --test             # one machine, from the machine
halite-hub run 'web*' state.apply --test   # a group of machines, from the hub
halite-hub run '*' state.apply --test      # the whole fleet: a drift report
```

A rehearsal of a file state shows the unified diff of the content it
would change, as in the tour.

## Writing idempotent trees

A short list, drawn from the ways real trees go wrong:

1. **Say what you want, not what to do.** `user.present`, not `cmd.run:
   useradd`. `file.managed`, not `cmd.run: echo ... > file`.
2. **Guard every `cmd.run`** with `creates`, `unless`, `onlyif` or
   `onchanges`. Ask: "what would make this unnecessary?" and say it.
3. **Keep volatile values out of managed files.** A template that writes
   the current time, a build number that changes per run, or a random
   password regenerated each run makes the file differ every time. (halite
   seeds random template functions per node and template so that a test
   run and the real run agree, but the content still changes if the seed
   inputs do.)
4. **Prefer content to commands for files.** `file.managed` compares
   hashes; `cmd.run: sed -i` mutates without comparing.
5. **Order with requisites, not luck.** If a run needs two passes to
   converge, an ordering dependency is missing.
6. **Be careful with "latest".** `pkg.latest` changes whenever the
   repository does, by design; that is not idempotent in the sense of
   "converged after one run", it is "follows upstream". Use it on purpose.
7. **Test the second run.** The script above, in CI, catches the whole
   class.

## Drift, and what to do about it

**Drift** is any difference between a machine and its description that
appeared after the last apply. Causes are mundane: a hand edit, a package
upgrade that rewrote a config file, a script, a change on a neighbour.

halite gives you a spectrum, from observation to repair:

| Posture | How | Use when |
|---|---|---|
| Detect only | A scheduled `state.apply --test`; alert on any `None` | You must not change things unattended — a regulated change window. |
| Repair on a schedule | A scheduled `state.apply`, e.g. every 30 minutes with a random delay | The normal case. |
| Repair on demand | Operators run `halite-hub run ... state.apply` | You want changes only when a person asks. |
| Repair on event | A beacon notices a file change; a reactor applies the state | You want repair in seconds, not minutes ([chapter 24](24-scenarios.md)). |

Most fleets use the second as the foundation and add the fourth for the
few files that must never differ.

## Common questions

**"If everything is idempotent, why does the second run exit `2` and the
first `0`?"** The exit code does not mean "failure"; it distinguishes
"something changed" from "already right". Scheduled jobs should treat both
as success.

**"Does a restart count as a change?"** A `service.running` with a `watch`
restarts when the watched state reported changes; the restart is reported
as a change of the *service* state, but the watched file did not change
again. A converged system never restarts anything.

**"Can I make a state run every time on purpose?"** Yes — a `cmd.run`
with no guard, or `test.succeed_with_changes`. Do it deliberately and
name it so the next reader knows (`rotate_logs_always`). It will make
every run exit `0`, which matters if monitoring watches for `2`.

**"What if a state is idempotent but slow?"** Idempotent does not mean
cheap: the check still runs. Fleet-wide hourly runs are fine; if a tree
takes minutes, look at which states dominate with the per-state
`Duration`.

## Key ideas

- A state is a **description of an end state**, not a step.
- Every state function **checks first, acts only on a difference**, and
  reports `changes` that are empty when it did nothing.
- A run is **converged** when nothing changed; `halite-node state apply`
  tells you with **exit status 2**.
- **Test mode is a contract**: `None` means a real run would change
  something, and no state changes anything while in it.
- `cmd.run` is the exception: **guard it** with `creates`, `unless`,
  `onlyif` or `onchanges`.
- Because it is safe, you can **apply continuously**, which turns
  configuration management into drift repair.

Next: [states and SLS files](05-states.md), the language you write all
of this in.
