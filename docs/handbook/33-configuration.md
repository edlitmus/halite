# Runbook: configuring halite

<!-- abstract: Where configuration lives on each platform, how files and drop-ins combine, the settings you will actually change, how to change them safely, and how to manage halite's own configuration with halite. -->
<!-- kind: runbook -->
<!-- index: configuration; hub.yaml; node.yaml; api.yaml; drop-in; node.d; hub.d; config root; --root; --config; pki_dir; state_dir; cache_dir; socket_dir; log_dir; config.get; unrecognised key; restart; no reload; settings cheat sheet; file modes; managing halite with halite -->

**Use this runbook to** find, read, change and verify halite's configuration:
on one machine, on a fleet, and as part of the tree.

**You will end with** a clear picture of which file controls what, a safe way
to change a setting and prove it took effect, and your own configuration
kept in version control.

## Before you start

- The exact **settings** are in [the configuration reference](../configuration.md),
  generated from the code so it cannot disagree with the build. This runbook
  is how to *use* it: layout, precedence, procedure. Do not keep a second list
  of settings in your head or your wiki.
- Anything that changes configuration is a **restart**. Neither the hub, the
  node nor the API reloads: nothing handles `SIGHUP`, so the signal terminates
  the process, and the service files deliberately carry no `ExecReload`
  (`systemctl reload` once turned into an outage). Plan restarts.

## Procedure

### 1. Know where things are

| Thing | Linux | FreeBSD and the other BSDs |
|---|---|---|
| Configuration root (`<config root>`) | `/etc/halite` | `/usr/local/etc/halite` |
| State (`state_dir`) | `/var/lib/halite` | `/var/db/halite` |
| Cache (`cache_dir`) | `/var/cache/halite` | `/var/cache/halite` |
| Sockets (`socket_dir`) | `/run/halite` | `/var/run/halite` |
| Logs | `/var/log/halite` (or the journal) | `/var/log/halite` |
| Binaries | `/usr/local/bin` | `/usr/local/bin` |
| Man pages | `/usr/local/share/man/man8` | `/usr/local/share/man/man8` |

macOS uses the Linux paths (Homebrew's prefix is not fixed, so `/etc` is the
honest default), and Windows `%PROGRAMDATA%\Halite`.

In the configuration root:

| File | Read by | Holds |
|---|---|---|
| `node.yaml` | `halite-node` | The agent's settings, `schedule:`, `beacons:` |
| `hub.yaml` | `halite-hub` | The control plane's settings, `reactor:`, `nodegroups:` |
| `api.yaml` | `halite-api` | The API's settings, `hooks:`, login methods |
| `policy.yaml` | hub and API | Authorization ([chapter 26](26-authorization.md)) |
| `accounts.yaml` | `halite-api` | Local accounts (mode `0600`) |
| `grains`, `grains.d/` | the node | Custom grains ([chapter 8](08-grains.md)) |
| `beacons.d/`, `schedule.d/` | the node | Fragments |
| `node.d/`, `hub.d/`, `api.d/` | each program | Drop-in fragments |
| `pki/` | all | Keys and certificates (`0700`) |

### 2. Know how files combine

For each program: the **primary file**, then **every file in its `.d/`
directory in lexical order**, deep-merged (a later file can add or change a key
without restating the rest), then command-line overrides. Mappings merge key
by key; lists and scalars are replaced.

A lab run with this layout:

```
node.yaml           node_id: base-id    log_level: info    grains: {role: web, tier: one}
node.d/10-override  log_level: debug                       grains: {tier: two}
node.d/20-last      node_id: last-wins
```

reads as

```
$ halite-node call config.get node_id   →  last-wins
$ halite-node call config.get log_level →  debug
```

and `grains` becomes `{role: web, tier: two}`. Use the drop-in directory for
what *differs by role or site* so that the primary file stays the same
everywhere, and so a configuration-management state can own one fragment
without clobbering the rest.

`--config <path>` names the primary file and `--root <dir>` moves the whole
configuration root. `--root` moves `pki_dir` and `policy` with it. It does
**not** move `state_dir`, `cache_dir`, `socket_dir` or the log directory;
set those explicitly to relocate them.

**A key halite does not read is reported, not ignored.** A misspelling, or a
setting that belongs to another program, is:

```
configuration key "node_idd" is not recognised and was ignored
```

and `doctor` repeats it as a warning (`loads, with 1 setting(s) this build does
not read`). Read your logs at startup.

Names from the predecessor are accepted, with a warning naming the new spelling
and a note that the compatibility shim is a defined, dated, removable surface.
Translate them when you see them.

### 3. The settings you will actually change

Defaults here are the build's, verified; the reference has the rest.

**Hub (`hub.yaml`)**

| Setting | Default | Change it to… |
|---|---|---|
| `listen` | `:4510` | Another port or interface |
| `file_roots`, `pillar_roots` | none on a hub (a node with no configuration at all reads `<config root>/state`, `/srv/halite/states` and `/srv/salt`) | Where your tree is. **Set these on a hub.** |
| `fileserver_backend`, `gitfs_*` | `roots` | `[roots, git]` to serve from git ([chapter 10](10-top-and-files.md)) |
| `pillar_trusted_grains` | the eight safe grains | Add a grain only deliberately |
| `pillar_source_merging_strategy` | `smart` | `aggregate` if you want lists concatenated |
| `enrollment_mode` | `manual` | `token` for automatic enrolment |
| `certificate_lifetime` | `2160h` (90 d) | Shorter for a smaller window |
| `job_cache_retention`, `job_cache_max_size` | `720h`, 10 GiB | Longer, for audit |
| `event_retention`, `event_max_size` | `720h`, 4 GiB | Sized for your slowest consumer |
| `nodegroups` | none | Named target expressions |
| `reactor` | none | Reactions ([chapter 20](20-reactors.md)) |
| `event_return`, `event_return_tags` | none | Ship events out |
| `metrics` | on | Leave it on |
| `undefined` | `strict` | Leave it strict |

**Node (`node.yaml`)**

| Setting | Default | Change it to… |
|---|---|---|
| `hub`, `hub_fingerprint` | none | The hub and its pinned CA |
| `node_id` | FQDN | An explicit, stable name |
| `hub_alive_interval` | `30s` | Rarely |
| `hub_tries` | `0` (forever) | A number only for an agent that should give up |
| `job_queue_depth` | `16` | Rarely |
| `schedule`, `beacons` | none | Your timetable and watchers |
| `grains_refresh_interval` | `30m` | Shorter if grains change often |
| `returner` | `local` | `webhook` etc. |
| `require_job_signature`, `job_signer_keys` | off | On, with keys, for sensitive nodes |
| `startup_states` | none | `highstate`, to converge at boot |
| `exec_path` | the process's `PATH` | Pin it so a service and a shell agree |
| `pillar_cache_disk` | on | `false` on a node that must not hold secrets at rest; it then cannot apply pillar-reading states during a hub outage |
| `extension_*` | none | Trust keys and pins |
| `cmd_default_shell` | on | `false` once your `cmd.run` call sites are converted |
| `metrics_listen` + `metrics_tls_*` | off | A node-side scrape endpoint |

**API (`api.yaml`)**: `listen` (`:4511`), `hub` (`host:port`), `api_operator`,
`tls_cert`, `tls_key`, `accounts`, `token_lifetime` (`12h`), `token_idle`
(`4h`), `hooks`, `ldap_*`, `oidc_*`.

### 4. Change a setting safely

The same procedure every time. It takes five minutes and is the difference
between an edit and an outage.

1. **Edit** the file (or better, the tree that writes it; step 6).
2. **Load it without running**: it is the program's own loader that matters.

   ```sh
   halite-node doctor            # "configuration validity" loads it and compares it with what is running
   halite-hub doctor
   halite-api doctor
   ```

   `doctor` reports a file that will not load, a setting nothing reads, and
   whether the file on disk differs from the running process. Better, for a
   single value:

   ```sh
   halite-node call config.get log_level
   ```

3. **Restart** — one node first if it is a node setting:

   ```sh
   sudo systemctl restart halite-node         # FreeBSD: sudo service halite_node restart
   ```

4. **Verify the effect**, not the edit: the log line at start, `doctor`,
   `halite-hub runner manage.status`, or the behaviour you changed.
5. **Roll to the rest**, in batches, if it is a fleet setting.

A node whose configuration will not load does not start, and the unit exits `1`
for the two cases a restart cannot fix. Keep the previous file (`cp
node.yaml node.yaml.prev`, or version control) so that the way back is a copy
and a restart.

> **Warning:** do not automate step 3 with a `watch` on the agent's own
> service. `service.running` with `watch` restarts the service from inside
> the job, and when the service **is** the agent, the stop kills the process
> running the job. **On both platforms the job never returns**, so nothing
> reports whether the change took. What happens to the node then differs,
> and both were measured:
>
> | | The job | The node |
> |---|---|---|
> | FreeBSD | never returns | **stays down** — the restart runs as a child of the agent and is killed with it |
> | systemd | never returns | comes back — the restart is a job for pid 1, not work done by the caller |
>
> halite now refuses it, naming the recipe below, rather than reporting a
> restart that has not happened. A tree written before that refusal existed
> takes FreeBSD nodes down and leaves Linux ones silent. Step 6 has the form
> that works. The hub and the API need none of this: they are not the
> process running the state and take an ordinary `watch`.

### 5. File modes and ownership

halite never takes a mode from the umask. Keep it that way:

| Path | Mode | Owner |
|---|---|---|
| `pki/` and everything in it | `0700` (keys `0600`) | the program's account |
| `accounts.yaml` | `0600` | the API's account |
| `hub.yaml`, `node.yaml`, `policy.yaml` | `0640` is fine; they hold no secret unless you put one there | root or `halite` |
| `state_dir` | `0700` | the program's account |

Do not put secrets in the configuration files. Use `*_file` settings
(`oidc_client_secret_file`, `ldap_bind_password_file`,
`returner_webhook_secret_file`) pointing at files with tight modes.

### 6. Manage halite's configuration with halite

Treat `node.yaml` like any other file in your tree: a template filled from
pillar, written by a state, with the agent restarted a little afterward so it
does not stop the very job that is changing it. The project ships exactly
this as `contrib/examples/tree` (`halite.node`, `halite.hub`, `halite.api`,
`halite.certs`, `halite.signer`), compiled for FreeBSD and Linux by a test on
every change. Copy `states/` and `pillar/` into your own tree, change the IDs
and values, and read its README first: it lists which parts have been
compiled but not run (the deferred restart, the hub restarting on a change, the
signer's push). A condensed shape:

```yaml
# states/halite/node.sls (shape; the shipped tree is the tested version)
{% from "halite/map.jinja" import halite with context %}

halite_node_config:
  file.serialize:
    - name: {{ halite.confdir }}/node.yaml
    - dataset: {{ pillar['halite']['node'] | tojson }}
    - serializer: yaml
    - mode: '0640'
```

The restart is the part that cannot be written the obvious way. `watch` is
refused on the agent's own service, so the shipped tree detaches it and
delays it, and leaves the `service.running` **watchless** so that the next
highstate starts an agent that failed to come back:

```yaml
# Rendered for FreeBSD; the shipped tree templates the service name and
# branches on grains['kernel'].
halite node restart when its configuration changes:
  cmd.run:
    # FreeBSD. On Linux:
    #   systemd-run --on-active=30 --unit=halite-node-restart systemctl restart halite-node
    - name: daemon -f /bin/sh -c 'sleep 30; service halite_node restart'
    - onchanges:
      - file: halite node configuration

halite node agent:
  service.running:
    - name: halite_node
    - enable: true
```

The delay lets the agent finish the job and send its return before it dies;
the detach puts the restart outside the process group the agent's own command
confinement kills when it shuts down. Both lines above were run verbatim
inside a real hub-dispatched job: the job returned at once and the agent
came back 11 s later on FreeBSD and 31 s later on Linux.

On Linux a transient timer's `AccuracySec` is one minute — `systemctl show
-p AccuracyUSec` on the timer says `1min` — so read the 30 as 30 to 90
seconds. And a `systemd-run --unit=` whose unit already exists fails, so a
second apply inside the window reports a failure for a restart that in fact
happened; put `systemctl stop <unit>.timer <unit>.service` first, or add
`--collect`, if that matters to you.

The bootstrapping order matters, because nothing can configure a hub that does
not exist or enrol a node that has not been accepted: install the programs,
start the hub (it makes the CA), make yourself an operator, **then** apply the
tree locally on the hub host (`halite-node state apply --local --file-root … --id hub.example.com`),
enrol the hub's own node to itself using a separate `pki_dir` and `state_dir`
(sharing the hub's would mix its key with the CA's), and then every other node.

### 7. Sign the state tree

`require_state_signature` ([chapter 28](28-hardening.md#signing-the-state-content))
makes a node refuse a tree that a signer off the hub did not publish. Turn it
on in this order, because a node that requires a signature on an unsigned
tree applies nothing.

1. **Make a key, off the hub.** Use the job signer's command, with a name of
   its own. A tree signer and a job signer are different authorities, so do
   not reuse the job key:

   ```sh
   mkdir signer
   halite-hub keys signer create tree-ci --out signer/tree-ci
   ```

   The directory must exist. If it does not, the command stops with `open
   signer/tree-ci.key: no such file or directory`. The command prints a line
   for `job_signer_keys`. For a tree, put the same line in `state_signer_keys`.

   Keep `signer/tree-ci.key` where CI can use it and the hub cannot. Or keep
   the key in AWS KMS behind `halite-ext-signer-kms`, as for jobs, so that
   nothing on disk can sign. Put the printed public line in a file, here
   `trusted-tree-keys`, one line per key.

2. **Sign in CI, on every change to a branch the hub serves.** Sign the tree
   as the hub serves it. For a git-backed tree, that is the commit as
   `git archive` writes it, not a checkout: `export-ignore` attributes can
   make the two differ. Pass the hub's configuration with `--config`, so
   that the signer applies the hub's own `file_ignore_glob` and
   `file_ignore_regex`:

   ```sh
   work=$(mktemp -d)
   git archive "$COMMIT" | tar -x -C "$work"
   halite-hub tree manifest "$work" --env base --source "commit $COMMIT" --config hub.yaml
   halite-hub tree sign "$work" --sign-key signer/tree-ci.key
   halite-hub tree verify "$work" --env base --keys trusted-tree-keys --config hub.yaml
   cp -R "$work/_halite" . && git add _halite && git commit -m "Sign $COMMIT"
   ```

   The manifest describes every file except `_halite/`, so the commit that
   adds `_halite/` on top of `$COMMIT` serves the content that was signed.
   Sign each environment the estate serves with its own `--env`, and sign
   again on a schedule well inside `state_manifest_max_age` (thirty days by
   default): nightly gives weeks of slack. A new signature over unchanged
   content is a new revision with a new `issued` time, and costs no
   transfer.

3. **Trust the key on the nodes, in `warn`.** In `node.yaml`, through the
   state that writes it (section 6):

   ```yaml
   require_state_signature: warn
   state_signer_keys:
     - 'tree-ci MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAE…'
   ```

   Restart the agent. Watch `halite_node_tree_refusals_total` over a week of
   real highstates. Each count is something `true` would refuse.

4. **Set `true`**, on one FreeBSD node and one Linux node first, then on the
   rest. If the node's own roots are applied with `--local` (as in section 6
   for the hub host), sign those too or set `state_signature_local: false`
   there, deliberately.

**To roll back** the tree, sign the old commit's content again. The new
`issued` time makes it the newest revision. A node refuses an older manifest
by design, so putting back an old `_halite/` does not work.

**To rotate the key:**

1. Add the new key's line to `state_signer_keys` on every node, through a
   state that the old key signed. Restart, so that both keys are trusted.
2. Switch CI to the new key, and sign.
3. When every node has accepted a revision signed by the new key, remove the
   old line. `halite-node doctor` names the signer of each accepted
   revision.

A withdrawn key's manifests are refused at the next run. The high-water mark
stays, so a node cannot be walked back to an older revision by a key change.

## Verify

Configuration is right when:

```sh
halite-hub doctor        # "configuration validity: … loads; not compared with what the service is running: …"
halite-node doctor       # "configuration validity: … loads, and matches what the agent recorded when it last started (…)"
halite-api doctor
```

all pass, startup logs show no `not recognised` lines, and a deliberate probe
of the thing you changed behaves as intended.

For a signed tree, `halite-node doctor`'s `state signatures` check names the
requirement, the number of trusted keys, and each accepted revision with its
age and its signer. `halite-hub doctor` fails the same check if a
`_halite/` holds anything but the two files, or if a hide rule hides them.
`halite-hub tree verify` checks a tree against its manifest file by file.

`doctor` is a separate process from the service. It cannot see what the
service read when it started. On a node, the agent writes a digest of its
configuration to the evidence record when it starts. `halite-node doctor`
compares the file with that digest. `matches what the agent recorded` means
that no edit needs a restart. `differs from what the agent recorded` means
that an edit needs a restart. The hub and the API record no digest, so their
`doctor` says `not compared`. After you edit the configuration of the hub or
the API, restart the service.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| `configuration key "x" is not recognised and was ignored` | Typo, or a setting for another program | Check the reference; keys are per-program |
| An edit has no effect | Not restarted; or a `.d/` fragment overrides it; or the wrong `--root` | `halite-node call config.get <key>` shows the effective value; `doctor` says whether disk and process differ |
| The agent will not start after an edit | The file does not parse, or a value is invalid (a duration, a size) | `halite-node doctor --config <file>`; restore the previous copy |
| A setting seems ignored on the hub but works on a node | It is read by the other program only | The reference lists `halite-node`, `halite-hub`, `halite-api` per setting |
| `--root /opt/x` still shows production | You are on a build before `--root` followed all defaults | Upgrade; or pass `--config` too |
| Two sources disagree | Primary file vs `.d/` vs flag | Later wins: file, then `.d/` in lexical order, then flags |
| Every state run refused: `serves no signed manifest` | The tree is not signed, a hide rule such as `file_ignore_glob: '_*'` hides `_halite/`, or the hub refuses the environment for a stray file in `_halite/` | Sign it; exempt `_halite/`; `halite-hub doctor` names the stray file |
| `refusing an older revision` after a good signing run | The signer's clock is behind the last `issued` | Fix the clock and sign again |
| `issued longer ago than … state_manifest_max_age` | CI stopped signing, or the hub has been unreachable longer than the bound | Sign again; during an outage that is the freeze bound working |
| `arrived with sha256 … says …; it was not used` | Content was pushed without a new signature | Sign and push. Nothing was applied |

## Key ideas

- One file per program, in the platform's **config root**, plus a lexical
  **`.d/` directory** merged over it; later wins; keys halite does not read
  are reported.
- **Everything is a restart.** There is no reload.
- `doctor` loads the file the program's own way and says whether the process
  matches the disk; `config.get` shows the effective value.
- Secrets live in files named by `*_file` settings, never in the YAML.
- Keep configuration in the tree and let halite write it; the example
  `halite.*` states are the tested start.
- **An agent cannot restart itself from inside a job**, so a `watch` on its
  own service is refused: the job never returns on either platform, and on
  FreeBSD the node stays down. Detach the restart, delay it, and leave the
  `service.running` watchless.

Next: [monitoring and alerting](34-monitoring.md).
