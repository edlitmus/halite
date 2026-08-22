# Migrating from Salt

halite exists to run an estate's existing Salt tree. The compatibility is
deliberate and it is close, but it is not total, and the places it is not
are choices rather than omissions. This page is what to expect.

## Start with the audit

`halite-hub migrate` reads a Salt tree and reports what would happen,
without applying anything or needing a node:

```sh
halite-hub migrate /srv/salt --pillar-root /srv/pillar
```

```
Migration report for /srv/salt
  1 state files, 0 pillar files

Renderer inventory
  jinja|yaml               1 file(s)

Findings

  REVIEW (1)
    [yaml] bad.sls:3
      "0644" has a leading zero and is read as octal 420 under YAML 1.1; quote it to keep the string
      -> Quote the value; an unquoted 0644 is read as the decimal 420. SPEC section 10.1.3.

Effort estimate
  yaml             1
  TOTAL            1
  BLOCKING         0

No blocking items. This tree can be applied once the review items are understood.
```

Findings come at three levels, and the exit code follows whichever you
ask it to care about:

| Level | Meaning |
|---|---|
| BLOCKING | The tree will not compile until this is dealt with. |
| REVIEW | It will compile, and it may not mean what it meant before. |
| NOTE | Worth knowing; nothing breaks. |

```sh
halite-hub migrate /srv/salt --fail-on blocking   # default
halite-hub migrate /srv/salt --fail-on review     # stricter
halite-hub migrate /srv/salt --out json           # for a pipeline
```

Run it before anything else. It answers "how big is this?" in seconds,
and the answer is usually smaller than expected.

## The four differences that will actually bite

### Undefined names are an error

Salt renders `{{ pillar['missing'] }}` as an empty string, and the state
built from it silently does the wrong thing — a `file.managed` with no
contents, a `user.present` with no name. halite makes it an error naming
the file, the line, and the identifier.

Migrating: run with `undefined: permissive` first, which restores Salt's
behaviour and logs every resolution at warning level with its position.
Fix what the log names, then turn it off. This is the setting most worth
the trouble: the warnings are, almost every time, real defects that were
never visible.

### `cmd.run` takes an argument vector, not a shell line

```yaml
# Salt, and halite with cmd_default_shell: true
run_it:
  cmd.run:
    - name: systemctl restart nginx && systemctl status nginx

# halite
run_it:
  cmd.run:
    - name: /usr/bin/systemctl
    - args: [restart, nginx]
```

Salt's default of a shell for `cmd.run` is the root of most of its
injection findings: any pillar value that reaches a command line is a
shell injection waiting for the right input. halite inverts the default
and makes the shell explicit.

The transition is `cmd_default_shell: true`, which restores Salt's
reading of `name` as a shell line while a tree is converted. Audit with
`--cmd-default-shell` once it is set: the states stop being work to do,
and the report says how many of them the tree now depends on the setting
for, because a tree that reports no work and stops running the day
someone turns a setting off is not a tree with no work. A command
that fails with "no such file" and a path containing a space is this,
and halite says so in the error.

The setting applies only to a state that has not been converted. One
that gives `args` is already an argument vector and stays one, so a tree
can be converted a state at a time with the setting on, and each state
stops going through a shell the moment it is rewritten. Asking for both
— `shell: true` beside `args` — is refused rather than resolved in
either direction.

### Unquoted file modes

`mode: 0644` is the integer 420 in YAML. Salt accepts it and writes the
wrong permissions. halite refuses it and says to quote it. The audit
finds every one.

### `y`, `n`, `on`, `off`

halite reads YAML 1.1's boolean spellings, as PyYAML does, so `on:` is a
boolean key and not the string "on". Every coercion is reported by
`lint`, so a tree can be audited for them.

The exception: PyYAML does *not* treat the single letters `y`, `Y`, `n`,
and `N` as booleans, whatever the YAML 1.1 specification says, so neither
does halite. A bare `n` is the string. This is documented in
[DIVERGENCE.md](DIVERGENCE.md) section 1.1 because SPEC.md says otherwise
and SPEC.md is wrong.

## Encrypted pillar

A `#!yaml|gpg` pillar file works. halite drives the system `gpg` binary,
as Salt does, and links no OpenPGP library; the ciphertext goes to it on
standard input and never on a command line. Salt's `gpg_keydir` is read
as `gpg_home` through the compatibility shim, so an existing
configuration needs no edit.

```yaml
# /usr/local/etc/halite/node.yaml on a BSD, /etc/halite/node.yaml on Linux
gpg_home: /usr/local/etc/salt/gpgkeys
gpg_binary: gpg          # the default
gpg_timeout: 30s         # per value
```

If `gpg` is not installed, a tree that names the renderer fails at load
with that reason rather than at the first encrypted value. A value that
cannot be decrypted names the pillar key it sits at, and never its
contents.

The `crypt` renderer of SPEC section 12.5, which is halite's own, is not
built yet. Until it is, an encrypted tree stays on gpg.

## Every command, side by side

[The command reference](command-reference.md) is the table this section
would otherwise repeat: each Salt command, what to type instead, and
whether it works in this build or waits on a phase.

## Vocabulary

halite does not use the role names Salt uses. The audit translates a
configuration file; a tree needs the same edits:

| Salt | halite |
|---|---|
| master | hub | <!-- lexicon:allow -->
| minion | node | <!-- lexicon:allow -->
| `salt://` | works unchanged, and `halite://` is the same thing |
| `__grains__`, `__pillar__` | bound, for compatibility |
| `saltenv` | works, and `env` is the same thing |
| `saltversion` | reports the Salt compatibility level this build targets |

## What is not there

halite ships a subset of Salt's roughly 400 modules, chosen by what a
real estate applies. This build has 209 execution functions across 42
modules and 56 state functions across 20. The [module
reference](modules.md) lists all of them and
[DIVERGENCE.md](DIVERGENCE.md) lists what is missing, module by module,
with the reason.

The larger absences today:

- **The hub.** Driving a fleet from one place is phase 2 of SPEC section
  32. A node manages its own tree now, which is Salt's masterless mode.
- **Beacons, reactors, orchestration, the mine over the wire.** Phase 3.
- **The API.** Phase 4.
- **Windows and macOS modules.** Phase 5. The code cross-compiles; none
  of it has been run.
- **`file.accumulated`**, and the backup Salt's `backup:` option keeps. <!-- lexicon:allow -->

## What is better, and worth using

- **`--test` is a contract**, enforced by a harness every state module
  passes. Salt's `test=True` is unreliable for a fair number of modules
  and there is no way to tell which from the outside.
- **Errors carry a position in the file you wrote**, not in the rendered
  output. Diagnosing a templated highstate in Salt is a well-known
  misery; fixing it was a goal of the design rather than a nicety.
- **The compiler reports every error at once** rather than stopping at
  the first.
- **`x509` needs nothing installed.** Salt's needs M2Crypto or
  `cryptography` compiled against OpenSSL headers, which is the single
  most common reason a Salt install fails. And `certificate_managed` here
  converges: it re-issues only when the certificate is missing, no longer
  matches its key, was not signed by the configured CA, or has entered
  the renewal window. Salt's re-issues on every highstate.
- **Templates are deterministically seeded**, so `random` and `shuffle`
  give the same answer in a `--test` run and the real run that follows,
  and on the run after that. Salt's do not, which produces phantom
  diffs. The seed is the node and the template, so two machines differ
  and two files differ, and nothing else does. `random_seed:
  nondeterministic` restores Salt's behaviour.

## Running both at once

They can share a machine. halite's control plane does not use Salt's
port, its configuration lives in `/usr/local/etc/halite` on a BSD and
`/etc/halite` on Linux, and its state tree can be
the same directory Salt reads. A sensible order is:

1. `halite-hub migrate` the tree, and read the report.
2. Fix the blocking items, if any.
3. Apply with `--test` on one machine and compare it against what Salt
   says it would do.
4. Apply for real on that one machine.
5. Widen.

Step 3 is the one worth not skipping. It is, at estate scale, the
differential test SPEC section 31 calls the primary correctness gate.
A small version of it runs in this project's own test suite: nine trees
compiled by both halite and Salt, with the low state and the pillar
compared, against Salt 3006.25 and 3008.2. It found four defects on its
first run. What it does not do is compare what actually *changes* when a
tree is applied, and it does not run against a real estate's trees —
yours. See [DIVERGENCE.md](DIVERGENCE.md) section 5.7 for exactly where
the line is.
