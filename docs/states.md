# State module reference

All state functions accept `name` (defaulting to the state ID) and honor
`-test`. Boolean args accept `true/yes/1/on`.

## Universal gates

Every state (not just `cmd.*`) accepts three gate arguments, evaluated by
the engine before the state runs:

| Arg | Skips the state when... |
|---|---|
| `creates` | this path exists |
| `unless` | this shell command exits 0 |
| `onlyif` | this shell command exits non-zero |

A gated skip reports `Result: True` with no changes.

## file

### file.managed

Ensure a file exists with the given content and mode.

| Arg | Description |
|---|---|
| `name` | target path (default: state ID) |
| `contents` | inline content; a trailing newline is added if missing |
| `source` | path to a source file; relative paths resolve against the SLS file's directory |
| `mode` | octal string, e.g. `"0644"` (ignored on Windows) |
| `user` / `group` | owner by name or numeric ID (ignored on Windows) |
| `makedirs` | create parent directories |
| `show_diff` | include a line diff in Changes (default true) |

If neither `contents` nor `source` is given, the file is created empty if
absent (touch semantics). Double-quoted `contents` process `\n` and `\t`
escapes; single quotes are literal. Content drift is reported as a -/+
line diff (suppressed for binary or >128KB content, or with
`show_diff: false`).

```yaml
/usr/local/etc/app.conf:
  file.managed:
    - source: files/app.conf
    - mode: "0640"
    - makedirs: true
```

### file.directory

Ensure a directory exists. Args: `name`, `mode`, `user`, `group`.

### file.absent

Ensure a path does not exist (recursive). Args: `name`.

## pkg

Backend is auto-detected: FreeBSD pkg(8); apt, dnf, yum, zypper, pacman,
apk on Linux; Homebrew on macOS; Chocolatey then winget on Windows.

### pkg.installed

| Arg | Description |
|---|---|
| `name` | single package (default: state ID) |
| `pkgs` | list of packages |

```yaml
tools:
  pkg.installed:
    - pkgs:
      - tmux
      - git
      - htop
```

Version pinning and alternate repos land in P1.

### pkg.removed

Same args; ensures packages are absent.

## service

Backend is auto-detected: FreeBSD rc.d (uses `onestart`/`onestatus` so
states work before the service is enabled; `enable` uses sysrc), systemd,
sysvinit, launchd (start/stop only), Windows SCM.

### service.running

| Arg | Description |
|---|---|
| `name` | service name (default: state ID) |
| `enable` | also enable at boot |

If a `watch`ed state changed, the service is restarted.

```yaml
nginx:
  service.running:
    - enable: true
    - watch:
      - file: nginx_conf
```

### service.dead

Ensure a service is stopped. Args: `name`.

## cmd

### cmd.run

Run a command through the platform shell (`/bin/sh -c`, `cmd /C`). Runs on
every apply unless gated (see Universal gates above).

| Arg | Description |
|---|---|
| `name` | the command (default: state ID) |
| `cwd` | working directory |
| `env` | list of `KEY=value` strings |

Non-zero exit fails the state; stdout/stderr/rc are reported in Changes.

```yaml
build_cache:
  cmd.run:
    - name: make cache
    - cwd: /srv/app
    - creates: /srv/app/.cache
```

### cmd.wait

Identical to `cmd.run` but only fires when a `watch`ed state changed.

```yaml
reload_pf:
  cmd.wait:
    - name: pfctl -f /etc/pf.conf
    - watch:
      - file: /etc/pf.conf
```

## user

Backends: pw(8) on FreeBSD, useradd/usermod on Linux, sysadminctl on
macOS (create/delete only), `net user` on Windows (existence only).

### user.present

| Arg | Description |
|---|---|
| `name` | username (default: state ID) |
| `uid` | numeric uid |
| `shell` | login shell |
| `home` | home directory |
| `gecos` | comment / full name |
| `groups` | supplementary groups; membership is additive |
| `createhome` | create the home directory (default true) |
| `system` | system account (Linux `-r`) |

On FreeBSD and Linux, uid/shell/home/gecos drift is detected from
/etc/passwd and repaired with pw usermod / usermod. Group membership is
merged (listed groups are added; unlisted memberships are kept).

```yaml
deploy:
  user.present:
    - uid: 1050
    - shell: /bin/sh
    - groups:
      - wheel
```

### user.absent

Args: `name`, `purge` (also remove the home directory).

## group

### group.present / group.absent

Args: `name`, `gid` (present only). Backends: pw(8), groupadd/groupdel,
dseditgroup, `net localgroup`.

## cron

Entries are identified by a `# halite: <identifier>` marker line, so the
command or schedule can change without orphaning the entry. Not supported
on Windows (scheduled tasks: planned).

### cron.present

| Arg | Description |
|---|---|
| `name` | the command (default: state ID) |
| `minute` `hour` `daymonth` `month` `dayweek` | schedule fields (default `*`) |
| `user` | crontab owner (default: invoking user) |
| `identifier` | marker identity (default: state ID) |

```yaml
converge:
  cron.present:
    - name: /usr/local/bin/halite apply /usr/local/etc/halite/base.sls
    - minute: "*/30"
    - user: root
```

### cron.absent

Removes the marker and its entry. Args: `name`/`identifier`, `user`.

## sysctl

### sysctl.present

Sets a sysctl at runtime and persists it.

| Arg | Description |
|---|---|
| `name` | sysctl key (default: state ID) |
| `value` | desired value (required) |
| `persist` | also write the config file (default true) |
| `config` | persist target; defaults to /etc/sysctl.conf (FreeBSD) or /etc/sysctl.d/99-halite.conf (Linux) |

macOS: runtime only. Windows: unsupported.

```yaml
kern.ipc.somaxconn:
  sysctl.present:
    - value: 1024
```
