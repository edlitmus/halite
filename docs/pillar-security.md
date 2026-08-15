# Protecting pillar data

halite does not encrypt pillar data. Pillar files are plain text on disk,
and their confidentiality comes from filesystem permissions plus, when you
want them encrypted at rest, an external tool that decrypts into the pillar
tree before a run.

This is a deliberate choice, not a gap waiting to be filled — see ADR-9 in
[architecture.md](architecture.md). What follows is how to hold it safely.

## Secrets go in pillar, never in states

Pillar is targeted: a host receives only its own rendered data. The state
tree is not — under a control plane, **every enrolled agent fetches the
whole state tree**, including states written for other hosts. A credential
embedded in a state file is therefore readable by any single agent in the
fleet. Put the secret in pillar and template it into the state:

```yaml
# states/db.sls — safe: the value arrives per host, from pillar
password: {{ .Pillar.db.password }}
```

Two more places a pillar secret can leak, and how to close them:

* `file.managed` includes a line diff of content changes in its reported
  Changes by default; a templated secret shows up in job results and logs.
  Set `show_diff: false` on states that write secret-bearing files.
* A state's `Comment` and `Changes` flow to the control plane, returners,
  and `-json` output. Keep secrets out of `cmd.run` command lines too —
  the command line is the comment when it fails.

## Lock down the pillar tree

The pillar tree is the sensitive one. The state tree usually is not, but
treat it the same way if your states embed credentials — better, don't
embed them (above).

```sh
install -d -o root -g wheel -m 0700 /usr/local/etc/halite/pillar
chown -R root:wheel /usr/local/etc/halite/pillar
chmod -R go-rwx     /usr/local/etc/halite/pillar
```

`0700` on the directory is what actually protects you: it stops any
non-root user from reading files inside, whatever their own modes are. Use
`wheel` on FreeBSD and macOS, `root` on Linux.

halite warns on stderr when it opens a pillar tree that group or others can
read. The warning is not a failure — some setups deliberately run as a
non-root user with a dedicated group — but an unexpected one means the tree
is readable by every account on the box.

Where the tree lives depends on how you run:

| Mode | Pillar tree lives on | Who can read it |
|---|---|---|
| masterless | each managed host | that host |
| fleet | the control plane only | the control plane |
| ssh | your workstation only | you |

## What reaches a managed host

Only that host's own rendered pillar, never the tree:

* **Fleet.** The agent fetches `GET /v1/pillar` over mTLS and keeps the
  result in memory for the length of the run; it is never written to disk.
  The control plane renders it for the identity in that agent's
  certificate — an agent cannot ask for another host's pillar. **What it
  can do is claim a grain**; see below.
* **ssh.** Pillar is rendered on your workstation and shipped as a single
  JSON file into a `0700` working directory, written with `umask 077`, and
  removed when the run ends.
* **Masterless.** The whole tree is on the host, because there is nowhere
  else for it to be. If a host must not see other hosts' data, do not give
  it a shared pillar tree — use fleet or ssh mode.

## Target pillar on the id, not on a grain

In a pillar top file, **only the id is authenticated**. The control plane
takes it from the client certificate and overwrites whatever the agent
reported, so a host cannot answer to another host's name:

```yaml
base:
  'web*':          # id glob — safe
    - web
  'L@db1,db2':     # explicit ids — safe
    - db
  'E@^web[0-9]+$': # regular expression on the id — safe
    - web
```

Every **other** grain is self-reported. An agent sends its grains in
`POST /v1/hello`, and the control plane renders that agent's pillar
through them. A host with a shell on it can add `role: db` to its custom
grains file, restart the agent, and receive whatever `role:db` selects:

```yaml
base:
  'role:db':       # a claim, not a fact — do not gate secrets on this
    - db-credentials
  'G@role:db':     # the same thing, spelled Salt's way
    - db-credentials
  'P@role:^db$':   # and the same again
    - db-credentials
```

This is not a bug in targeting — grains are how a host describes itself,
and that is exactly what you want in a **state** top file, where every
agent fetches the whole tree anyway. It is only a boundary question in a
pillar top, where the target decides who sees a secret.

If you want role-based pillar, keep the roles where an operator controls
them — a `L@` list per role in the pillar top, generated from your
inventory — rather than trusting each host's answer.

## Encrypting at rest with an external tool

Keep encrypted files in version control and decrypt into the pillar tree
before halite reads it. `sops`, `age`, `git-crypt`, or a secrets manager
all work; nothing about the pattern is halite-specific.

With [sops](https://github.com/getsops/sops) and age, on the control plane:

```sh
# in the repository
sops --encrypt --age $AGE_RECIPIENT pillar/database.sls > pillar/database.sls.enc

# on deploy, before starting or reloading the control plane
umask 077
sops --decrypt pillar/database.sls.enc > /usr/local/etc/halite/pillar/database.sls
```

Two rules make this safe:

1. **`umask 077` before writing.** A decrypted file created under the
   default umask is world-readable for as long as it exists.
2. **Decrypt into the pillar tree, not into the repository.** A decrypted
   file inside a working copy gets committed sooner or later.

Because the control plane only ever ships *rendered* pillar, decrypting
there is enough: managed hosts need no keys.

## Keeping sensitive values out of results and logs

Pillar values reach files, and file changes are reported as diffs. A
`file.managed` state that writes a password prints that password in its
`Changes` — to your terminal, to `-json` output, and into whatever collects
it.

```yaml
/usr/local/etc/app/credentials:
  file.managed:
    - source: files/credentials.tmpl
    - template: true
    - mode: "0600"
    - show_diff: false     # report that it changed, not what it says
```

Use `show_diff: false` on every file whose contents are confidential.

What the rest of the system does with results:

* The control plane logs job outcomes — id, agent, counts — never the
  content of changes.
* Job results, including any diffs you did not suppress, sit in the control
  plane's memory until it restarts and are returned to operator
  certificates on request.
* `halite run -json` and `halite ssh -json` print whatever the agent
  reported. Redirecting that into a file or a CI log puts it wherever that
  log goes.

## Private keys

halite writes every private key it generates with mode `0600`: the CA key,
the control plane's server key, operator keys, and each agent's key. Agent
keys are generated on the agent and never travel — only the signing request
does. See [pki.md](pki.md).

The CA key matters most: anyone holding it can mint an operator certificate
and drive the whole fleet. Keep it on the control plane host, back it up
encrypted, and never copy it into a configuration repository.
