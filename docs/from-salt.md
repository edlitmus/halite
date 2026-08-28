# Coming from Salt: a migration walkthrough

For an estate that looks like this today:

- one `salt-master`, serving `/srv/salt` and `/srv/pillar`; <!-- lexicon:allow -->
- a few dozen minions, enrolled and accepted; <!-- lexicon:allow -->
- a highstate on a schedule, from `schedule:` or from cron.

You will end up with the same shape: one hub, the same tree, the same
machines, the same nightly convergence. Nothing here asks you to rewrite
the tree first, and nothing here turns Salt off until the last step.

[migrating-from-salt.md](migrating-from-salt.md) is the reference for
*what differs*. This is the order to do it in.

If you have never used Salt, read [getting-started.md](getting-started.md)
instead; it builds a tree from nothing.

## The shape, side by side

| Salt | halite |
|---|---|
| `salt-master` | `halite-hub serve` | <!-- lexicon:allow -->
| `salt-minion` | `halite-node connect` | <!-- lexicon:allow -->
| `/etc/salt/master` | `<config root>/hub.yaml` | <!-- lexicon:allow -->
| `/etc/salt/minion` | `<config root>/node.yaml` | <!-- lexicon:allow -->
| `salt-key -A` | `halite-hub keys accept` — and there is no `auto_accept` |
| `salt '*' state.apply` | `halite-hub run '*' state.apply` |
| `salt-call --local state.apply` | `halite-node state apply --local` |
| `publisher_acl`, `external_auth`, `peer`, `peer_run`, `client_acl` | one `policy.yaml` |
| a minion's `schedule:` | a node's `schedule:`, same keys | <!-- lexicon:allow -->

Two ports become one. The tree does not move.

## Step 0: audit the tree

Do this before installing anything. It reads the tree and reports what
will not work, and it executes nothing:

```sh
halite-hub migrate /srv/salt
```

The pillar tree is found automatically when it sits beside the states as
`pillar/`; pass `--pillar-root /srv/pillar` when it does not. The report
says which it read.

A real estate's report looks like this:

```
Migration report for /srv/salt
  17 state files, 8 pillar files
  pillar read from /srv/salt/pillar

Renderer inventory
  jinja|yaml               20 file(s)
  yaml|gpg                  5 file(s)

Findings

  REVIEW (10)
    [pillar_grain] top.sls:9
      pillar targets on the grain "nodename", which a node controls and
      which is not trusted by default
      -> Add it to pillar_trusted_grains as a recorded decision, or move
         the attribute to a hub-authoritative node attribute.
    [state] state/plex.sls:10
      cmd.run names a program with arguments in it: "bastille start plex".
      halite runs a command without a shell, so this is one program name
      -> Put the program in `name` and the rest in `args`, or set
         `shell: true` on this state.

Effort estimate
  pillar_grain     4
  state            7
  TOTAL           11
  BLOCKING         0
```

**BLOCKING** means it will not run. **REVIEW** means it will run and may
not do what the tree meant. **NOTE** means nothing breaks.

Fix the blocking items now and the review items before you trust the
first real apply. The two above are the ones almost every Salt tree
produces, and both are worth understanding rather than silencing:

- **`cmd.run` with arguments.** halite runs a command without a shell,
  so `bastille start plex` is one program name with spaces in it. Split
  it into `name` and `args`, which is better anyway, or set
  `shell: true` on the state to keep Salt's behaviour. `cmd_default_shell:
  true` restores it everywhere, as a transition.
- **Pillar targeting on an untrusted grain.** A node controls its own
  grains, so a node that can name any grain in a pillar top file can ask
  for another node's secrets. Grains used for pillar targeting have to be
  named in `pillar_trusted_grains`. Salt does not enforce this and your
  tree probably relies on it; adding the grain to the list is a one-line
  decision, and it is now a recorded one.

## Step 1: the hub, beside what you already run

The hub does not use Salt's ports and does not read Salt's
configuration. It can run on the same machine.

One difference to settle before anything else: **the Salt daemon you are
replacing runs as root and the hub does not.** <!-- lexicon:allow -->
The hub runs as an unprivileged account, because it serves files and
compiles pillar and needs no more than that. Every directory Salt could
read by being root, the hub reads by being given access — and the
commonest migration failure is a directory it cannot use.

```sh
pw useradd halite -c "halite service account" -d /nonexistent -s /usr/sbin/nologin
sudo make install
```

`make install` puts the binaries, the service files, and those
directories in place for the platform it runs on, owned by that account.
Create the account first: it says so if you have not, rather than
leaving root-owned directories behind quietly.

Resist running `halite-hub` by hand as root before that. It creates
whatever is missing as root, and the service then cannot use it — with symptoms that name
neither the directory nor the account. `daemon: open: Permission denied`
and a service that will not start is one; a hub that starts and then
matches no node against any target is another. The
[operations guide](operations.md#accounts-and-permissions) has the full
table and a `find` that reports anything owned by the wrong account.

`<config root>/hub.yaml`:

```yaml
listen: 0.0.0.0:4510

# The tree Salt already serves. Nothing is copied.
file_roots:
  base:
    - /srv/salt
pillar_roots:
  base:
    - /srv/pillar

# The grains your pillar top targets on, from the Step 0 report.
pillar_trusted_grains:
  - id
  - os
  - os_family
  - osrelease
  - kernel
  - cpuarch
  - virtual
  - fips_mode
  - nodename

policy: /usr/local/etc/halite/policy.yaml
```

Start it. It creates its own enrollment CA on first run:

```sh
sudo halite-hub serve
```

Take the CA fingerprint — a node checks it before trusting anything:

```sh
halite-hub keys fingerprint
```

Then give yourself an operator certificate. This is your identity; it is
not a shared secret, and each operator gets their own:

```sh
sudo halite-hub keys operator create alice
```

## Step 2: authorization, before the first node

An empty policy grants nothing, to anybody, including root at the
console. That is deliberate and it will stop you if you skip this.

Start from [`contrib/examples/policy.yaml`](../contrib/examples/policy.yaml).
The smallest thing that works:

```yaml
roles:
  administrator:
    - target: '*'
      functions: ['*']
    - runners: ['*']
bindings:
  - principal: 'cert:CN=alice'
    roles: ['administrator']
```

Check it before you rely on it:

```sh
halite-hub policy show
halite-hub policy test 'cert:CN=alice' 'web1.example' state.apply
```

One thing to know now rather than at 3am: `functions: ['*']` does **not**
include `cmd.run`, `cmd.shell`, `cmd.script`, `module.run`, `file.write`,
or `file.replace`. Those run arbitrary code and have to be named. If your
tree applies states, you do not need them; if an operator runs ad-hoc
commands, give that role a rule naming exactly the ones it needs.

If you have a `publisher_acl` or an `external_auth` block, do not
translate it by hand yet — `halite-hub migrate --salt-config
/etc/salt/master` reports what was there, and the keys it could not <!-- lexicon:allow -->
translate are preserved under `legacy_acl` for you to read rather than
silently converted.

### If your pillar is encrypted

A `#!yaml|gpg` pillar file works unchanged, and Salt's `gpg_keydir` is
read as `gpg_home` through the compatibility shim. Decryption happens on
the hub, where pillar is compiled, so the setting belongs in `hub.yaml`
and the private key stays on the hub.

The part that catches people is the account. Salt decrypted as root and
read its keyring at `/usr/local/etc/salt/gpgkeys` by owning it; the hub
runs as `halite` and does not. Give it a keyring of its own rather than
opening Salt's:

```sh
sudo install -d -o halite -g halite -m 0700 /usr/local/etc/halite/gpgkeys
sudo cp /usr/local/etc/salt/gpgkeys/* /usr/local/etc/halite/gpgkeys/
sudo chown halite:halite /usr/local/etc/halite/gpgkeys/*
```

```yaml
# hub.yaml
gpg_home: /usr/local/etc/halite/gpgkeys
```

That private key now decrypts your whole pillar and is readable by the
service account, which is worth being deliberate about — it is the same
exposure Salt had as root, moved to a smaller account.

Check it before you point a node at it:

```sh
halite-hub runner pillar.show_pillar node=web1.example --as <operator>
```

Every value that will not decrypt is reported by the pillar key it sits
at, never by its contents. A hub that cannot decrypt fails the whole
compilation rather than serving a pillar with the secrets missing — and
a node whose hub cannot compile pillar refuses to render states that
read it, rather than rendering them against nothing.

## Step 3: one node, alongside the agent already on it

Pick one machine. Leave `salt-minion` running; the two do not <!-- lexicon:allow -->
interfere.

```sh
sudo install -m 0755 bin/halite-node /usr/local/bin/
```

`<config root>/node.yaml`:

```yaml
node_id: web1.example.com
hub: hub.example.com
hub_fingerprint: '6d:1f:8d:...'   # from Step 1
```

The fingerprint is all a new node needs: it fetches the hub's CA and
trusts it only if it matches, so there is no certificate to distribute.
It is also required — enrolling without one is refused, because the
fingerprint is the whole of the trust decision at first contact.

Enrol. The node asks; the hub holds the request until you accept it:

```sh
sudo halite-node enroll
```

On the hub, compare the fingerprint out of band and accept:

```sh
halite-hub keys list --state pending
halite-hub keys fingerprint web1.example.com
halite-hub keys accept web1.example.com
```

There is no `auto_accept`. This is the one manual step, and it is the
step that decides what is in your estate.

Then connect:

```sh
sudo halite-node connect
```

On the hub:

```sh
halite-hub run 'web1.example.com' test.ping
halite-hub run 'web1.example.com' grains.items
```

## Step 4: compare a highstate before applying one

This is the step worth not skipping. Render on both systems and compare
what each says it would do:

```sh
# What Salt says
salt 'web1.example.com' state.apply test=True > /tmp/salt.txt   # lexicon:allow

# What halite says
halite-hub run 'web1.example.com' state.apply --test --out json > /tmp/halite.json
```

They will not be byte-identical — the output formats differ — but the set
of states, their ordering, and which ones report changes should match.
Where they do not, the Step 0 report usually says why.

`halite-hub lint` renders and parses a file without executing it, which
is the fastest way to check one SLS while you are working through a
difference:

```sh
halite-hub lint /srv/salt/state/plex.sls
```

## Step 5: apply for real, on that one machine

```sh
halite-hub run 'web1.example.com' state.apply
```

Then run it again. The second run should report no changes. A state that
reports changes on every run is not converging, and finding that on one
machine is much cheaper than finding it on forty.

```sh
halite-hub jobs list
halite-hub jobs show <jid>
```

## Step 6: move the schedule

Only after Step 5 has converged twice. Turn off the Salt schedule for
this machine, and put it in `node.yaml`:

```yaml
schedule:
  nightly_highstate:
    function: state.apply
    cron: '17 3 * * *'
    splay: 900
    maxrunning: 1
```

The keys are Salt's: `cron`, `when`, `every`, `seconds`/`minutes`/`hours`/
`days`, `range`, `after`, `until`, `splay`, `maxrunning`, `catchup`.

Two settings worth having:

- `splay: 900` spreads the fleet over fifteen minutes. Without it every
  machine converges on the same second and hits the file server together.
- `maxrunning: 1` stops a slow run overlapping the next one, which is how
  a machine ends up in neither state.

Set `timezone:` explicitly if the fleet spans zones, or `17 3 * * *` means
a different moment on every machine.

If you would rather keep the schedule in the machine's own scheduler,
that is a legitimate choice and `contrib/systemd/halite-highstate.timer`
is ready to install. The built-in one is worth using when you want the
schedule to travel with the configuration, to change without a restart,
or to catch up after an outage.

## Step 7: widen

Repeat Steps 3 and 6 for the rest, in batches. `--batch` is a property of
the job here rather than of your terminal, so closing the terminal does
not abandon it half-done:

```sh
halite-hub run '*' state.apply --batch 10%
halite-hub jobs resume <jid>      # if it was interrupted
```

Watch it happen:

```sh
halite-hub event listen
halite-hub metrics | grep halite_jobs
```

## Step 8: turn Salt off

Once every machine has converged twice under halite and a scheduled run
has fired at least once:

```sh
sudo systemctl disable --now salt-minion   # lexicon:allow
```

Keep the master installed and stopped for a while. It costs nothing and <!-- lexicon:allow -->
it is the rollback.

## Rolling back

At every step before Step 8, Salt is still installed and still works.
Rolling back is stopping `halite-node` and starting `salt-minion`. <!-- lexicon:allow -->
Nothing in halite modifies Salt's configuration, its keys, or its cache,
and the tree is read-only to both.

The one thing that is not reversible is a state that has already been
applied — but that is true of Salt too, and it is why Step 4 and Step 5
happen on one machine.

## What will be different

Four things about the tree bite in practice, and all four are in the
Step 0 report; the fifth is about the estate rather than the tree, so
the audit cannot see it. [migrating-from-salt.md](migrating-from-salt.md)
covers them in full:

1. **`cmd.run` does not use a shell.** Pipes, redirections, and `&&`
   need `shell: true` or splitting into `name` and `args`.
2. **Pillar targeting is restricted to trusted grains.** A node controls
   its own grains, so this is the one place Salt trusts something it
   should not.
3. **`functions: ['*']` does not include the arbitrary-code functions.**
   Name them or do not get them.
4. **YAML 1.1 booleans are off.** `yes`, `no`, `on`, `off` are strings
   unless you set `yaml_bool_11: true`. A tree that says `enabled: no`
   and means `false` needs that setting, and it is worth grepping for
   before you turn it on.
5. **The hub runs unprivileged.** Salt's daemon ran as root and read
   whatever it liked. <!-- lexicon:allow --> Every directory the hub
   touches — its PKI, its state, its cache, its GPG keyring — has to be
   readable by the account it runs as, and the symptoms of one that is
   not name neither the directory nor the account. This is the one the
   Step 0 audit cannot see, because it is about the estate rather than
   the tree.

And two that are better rather than different:

- A converged run exits 2, not 0, so a timer can tell "nothing to do"
  from "did something".
- Template randomness is deterministic per node, so `--test` means
  something and a highstate does not report changes it is about to make
  differently next time.

## Where to go next

- [configuration.md](configuration.md) — every setting, grouped, with
  which of the three programs reads it.
- [operations.md](operations.md) — running it: keys, backups, metrics,
  relays, FIPS.
- [migrating-from-salt.md](migrating-from-salt.md) — the differences in
  full, and what is not there yet.
- [DIVERGENCE.md](DIVERGENCE.md) — what has been verified and what has
  not, which is worth reading before trusting any of it.
