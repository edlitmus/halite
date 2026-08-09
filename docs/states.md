# State module reference

All state functions accept `name` (defaulting to the state ID) and honor
`-test`. Boolean args accept `true/yes/1/on`.

## file

### file.managed

Ensure a file exists with the given content and mode.

| Arg | Description |
|---|---|
| `name` | target path (default: state ID) |
| `contents` | inline content; a trailing newline is added if missing |
| `source` | path to a source file; relative paths resolve against the SLS file's directory |
| `mode` | octal string, e.g. `"0644"` (ignored on Windows) |
| `makedirs` | create parent directories |

If neither `contents` nor `source` is given, the file is created empty if
absent (touch semantics). Ownership (`user`/`group`) lands in P1.

```yaml
/usr/local/etc/app.conf:
  file.managed:
    - source: files/app.conf
    - mode: "0640"
    - makedirs: true
```

### file.directory

Ensure a directory exists. Args: `name`, `mode`.

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
every apply unless gated.

| Arg | Description |
|---|---|
| `name` | the command (default: state ID) |
| `cwd` | working directory |
| `creates` | skip if this path exists |
| `unless` | skip if this shell command exits 0 |
| `onlyif` | skip unless this shell command exits 0 |
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
