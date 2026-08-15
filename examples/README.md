# Examples

Every file here compiles: `halite parse examples/<file>` reports no
errors, and `halite show examples/<file>` prints the plan it would run.
The ones guarded on a grain compile on the platform they are for —

```sh
HALITE_GRAINS=examples/grains halite show -root examples/tree
halite parse examples/linux-hardening.sls      # on a Linux host
```

Removal states — `file.absent`, `pkg.removed`, `service.dead`,
`host.absent`, `user.absent`, and the rest — take the same arguments as
the state they undo and are documented beside it in
[docs/states.md](../docs/states.md), so they are not repeated here.

## Masterless state files

| File | Shows |
|---|---|
| `base.sls` | a baseline: packages, a templated motd, a stale file removed |
| `webserver.sls` | the shape of a real state file — package, config, service, requisites |
| `sshd.sls` | editing a config the OS package owns: `file.replace`, `file.line`, `file.blockreplace`, `ssh_auth`, and `watch_in` |
| `identity.sls` | the settings a host has one of: hostname, timezone, `/etc/hosts`, locale, kernel modules |
| `pyapp.sls` | deploying an application: `file.recurse`, `virtualenv.managed`, `pip.installed`, `file.symlink` |
| `repo.sls` | a third-party repository and a held package version |
| `tls.sls` | an internal CA and a certificate signed by it, renewed inside a window |
| `provisioning.sls` | getting content onto a host: `git.latest`, `archive.extracted`, `mount.mounted`, `cron.present` |
| `linux-hardening.sls` | Linux-only settings behind a grain guard: `selinux`, `service.enabled`/`disabled`, `kmod`, `alternatives` |
| `jail.sls` | a FreeBSD jail: its configuration, a watch that restarts it, and one removed |
| `container.sls` | an OCI container through docker or podman, with drift detected by a spec label |
| `windows.sls` | Windows-only, behind the same kind of guard: `reg.*`, and `cron` as a scheduled task |
| `pf-reload.sls` | `cmd.wait`, fired only by a watch |

## A state tree

`tree/` is what `-root` points at: a top file, includes, an external
module, and templated sources.

```sh
HALITE_GRAINS=examples/grains halite show -root examples/tree
```

| Path | Shows |
|---|---|
| `tree/top.sls` | targeting: a grain match, a glob, and `and`/`not` |
| `tree/common.sls` | `names:` expansion, one state per package |
| `tree/web/init.sls` | `include:`, a templated source, `require` and `watch` |
| `tree/web/tls.sls` | `watch_in`, reaching a state another file declares |
| `tree/_modules/motd` | an external module: an executable that speaks JSON |
| `grains` | static custom grains, so `role:web` targeting has something to match |
| `pillar/` | a pillar tree with its own top file |

## Fleet configuration

These are read by the daemons, not applied as states.

| File | Read by |
|---|---|
| `beacons.sls` | `halite agent -beacons` — watches that raise events |
| `schedule.sls` | `halite agent -schedule` — work the agent runs on its own clock |
| `mine.sls` | `halite agent -mine` — facts published for the rest of the fleet |
| `reactor.sls` | `halite master -reactor` — rules turning events into jobs |
| `orch/deploy.sls` | `halite orchestrate deploy` — ordered fleet-wide steps |
