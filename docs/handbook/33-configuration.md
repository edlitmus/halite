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
| `file_roots`, `pillar_roots` | none on a hub (a node with no configuration at all reads `/srv/halite/states` and `/srv/salt`) | Where your tree is. **Set these on a hub.** |
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
| `pillar_cache_disk` | off | On, to survive a hub outage |
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

The bootstrapping order matters, because nothing can configure a hub that does
not exist or enrol a node that has not been accepted: install the programs,
start the hub (it makes the CA), make yourself an operator, **then** apply the
tree locally on the hub host (`halite-node state apply --local --file-root … --id hub.example.com`),
enrol the hub's own node to itself using a separate `pki_dir` and `state_dir`
(sharing the hub's would mix its key with the CA's), and then every other node.

## Verify

Configuration is right when:

```sh
halite-hub doctor        # "configuration validity: … loads, and matches what this process is running"
halite-node doctor       # (and on each kind of node)
halite-api doctor
```

all pass, startup logs show no `not recognised` lines, and a deliberate probe
of the thing you changed behaves as intended. `matches what this process is
running` is the phrase to look for: it means the file on disk and the process
in memory agree, so no edit is waiting for a restart.

## If it goes wrong

| Symptom | Cause | Fix |
|---|---|---|
| `configuration key "x" is not recognised and was ignored` | Typo, or a setting for another program | Check the reference; keys are per-program |
| An edit has no effect | Not restarted; or a `.d/` fragment overrides it; or the wrong `--root` | `halite-node call config.get <key>` shows the effective value; `doctor` says whether disk and process differ |
| The agent will not start after an edit | The file does not parse, or a value is invalid (a duration, a size) | `halite-node doctor --config <file>`; restore the previous copy |
| A setting seems ignored on the hub but works on a node | It is read by the other program only | The reference lists `halite-node`, `halite-hub`, `halite-api` per setting |
| `--root /opt/x` still shows production | You are on a build before `--root` followed all defaults | Upgrade; or pass `--config` too |
| Two sources disagree | Primary file vs `.d/` vs flag | Later wins: file, then `.d/` in lexical order, then flags |

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

Next: [monitoring and alerting](34-monitoring.md).
