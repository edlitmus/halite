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
| `template` | `true` renders the source through text/template with grains |
| `makedirs` | create parent directories |
| `show_diff` | include a line diff in Changes (default true) |

If neither `contents` nor `source` is given, the file is created empty if
absent (touch semantics). Double-quoted `contents` process `\n` and `\t`
escapes; single quotes are literal. Content drift is reported as a -/+
line diff (suppressed for binary or >128KB content, or with
`show_diff: false`). Set `show_diff: false` on any file carrying a pillar
secret — the diff travels into job results, returners, and logs (see
[pillar-security.md](pillar-security.md)).

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

### file.recurse

Copy a directory from the state tree onto the host.

| Arg | Description |
|---|---|
| `name` | destination directory (default: state ID) |
| `source` | source directory, relative to the SLS file |
| `file_mode` | mode for copied files |
| `dir_mode` | mode for directories (default `0755` on creation) |
| `user`, `group` | ownership, applied to every managed path |
| `template` | `true` renders each file with Go `text/template` |
| `clean` | `true` removes paths under the destination the source does not have |

```yaml
/usr/local/etc/nginx/conf.d:
  file.recurse:
    - source: files/nginx/conf.d
    - file_mode: "0644"
    - dir_mode: "0755"
    - user: www
    - clean: true
```

Content, ownership, and modes are each checked, so a tree that is
byte-identical still reports drift if its permissions moved. Without
`clean`, files the source does not know about are left alone — a
destination that also holds hand-written entries is a normal setup.

The changes report is capped at ten paths per category, with a count for
the rest: a first run over a large tree should not bury the rest of the
output.

### file.symlink

Ensure a symbolic link points where it should. Args: `name` (the link),
`target`, `force`, `makedirs`, `user`, `group`.

```yaml
/usr/local/etc/nginx/sites-enabled/site:
  file.symlink:
    - target: /usr/local/etc/nginx/sites-available/site
    - makedirs: true
```

A link pointing elsewhere is repointed. A real file or directory in the
way fails the state unless `force: true` — deleting something that was
not a link is not a thing a run should do on its own. Ownership applies to
the link itself, not its target. Not implemented on Windows, where
symlinks need a privilege most services do not hold.

### file.copy

Copy a file that is already on the host. Args: `name` (destination),
`source` (a **host path**, not a state-tree path), `force` (default true),
`makedirs`, `preserve` (copy the source's ownership), `mode`, `user`,
`group`, `show_diff`.

For a file that comes from the state tree, use `file.managed`; for a
directory, `file.recurse`.

## Editing part of a file

`file.managed` owns a whole file. These states change part of one, for the
files something else also writes to. All of them take `mode`, `user`,
`group`, `show_diff`, and `makedirs`, write atomically, and keep the
file's existing permissions and ownership unless told otherwise.

### file.append / file.prepend

Ensure lines are present, adding what is missing at the end (or the
start). Args: `name`, `text` (a string or a list of lines).

```yaml
/etc/rc.conf:
  file.append:
    - text:
      - 'nginx_enable="YES"'
      - 'sshd_enable="YES"'
```

A line already somewhere in the file is left where it is: these states are
about presence, not position. A missing file is created.

### file.line

Manage a single line.

| Arg | Description |
|---|---|
| `name` | the file (default: state ID) |
| `content` | the line |
| `match` | substring identifying the line to act on (default: `content`) |
| `mode` | `ensure` (default), `replace`, `delete`, `insert` |
| `location` | `start` or `end` (default) for an insert with no anchor |
| `before`, `after` | insert relative to the first/last line containing this substring |
| `create` | `false` refuses to create a missing file (default true) |

```yaml
/etc/ssh/sshd_config:
  file.line:
    - content: "PermitRootLogin no"
    - match: "PermitRootLogin"
```

`ensure` means present exactly once: matching lines are replaced and any
duplicates dropped; if nothing matches, the line is inserted. `replace`
never creates, `delete` removes every match, and `insert` adds the line
only when no line matches.

`match` is a **substring**, not a regular expression — that is what Salt's
`file.line` matches on, and `file.replace` is the regular expression
state. Note that `mode` here is Salt's line mode, not permission bits: a
`file.line` keeps whatever permissions the file already has.

### file.replace

Substitute a regular expression throughout a file.

| Arg | Description |
|---|---|
| `name` | the file (default: state ID) |
| `pattern` | Go regular expression |
| `repl` | replacement; `$1` expands a capture group |
| `count` | replace at most this many (default: all) |
| `append_if_not_found`, `prepend_if_not_found` | add `not_found_content` (default: `repl`) when nothing matches |
| `ignore_if_missing` | `true` makes a missing file a no-op instead of a failure |

```yaml
/etc/ssh/sshd_config:
  file.replace:
    - pattern: '^#?PermitRootLogin .*'
    - repl: 'PermitRootLogin no'
    - append_if_not_found: true
```

The pattern is [Go's regexp syntax](https://pkg.go.dev/regexp/syntax) and
the replacement uses `$1`, not Python's `\1`. `^` and `$` match at line
boundaries — Salt's `file.replace` defaults to MULTILINE and nearly every
pattern written for it anchors a line, so halite sets the same flag. Use
`\A` and `\z` for the whole file. Most Salt patterns port unchanged;
back-references in the *pattern* do not exist in Go's engine.

### file.blockreplace

Manage the text between two markers, leaving the rest of the file alone.

| Arg | Description |
|---|---|
| `name` | the file (default: state ID) |
| `marker_start`, `marker_end` | the lines delimiting the block |
| `content` | the block body |
| `source`, `template` | read the body from a file beside the SLS, optionally rendered |
| `append_if_not_found`, `prepend_if_not_found` | add the block when the markers are absent |

```yaml
/etc/hosts:
  file.blockreplace:
    - marker_start: '# BEGIN halite'
    - marker_end: '# END halite'
    - source: files/hosts-block
    - append_if_not_found: true
```

This is how one state owns its share of a file that other things also
write to. A `marker_start` with no `marker_end` after it fails the state
rather than guessing where the block ends.

A multi-line body comes from `source`, because the YAML subset has no
block scalars (`content: |`). A one-line body can be written inline as
`- content: "10.0.0.1 db1"`, and `\n` in a double-quoted string works for
a short block.

### file.comment / file.uncomment

Comment out lines matching a regular expression, or uncomment them. Args:
`name`, `regex`, `char` (default `#`).

```yaml
/etc/ssh/sshd_config:
  file.comment:
    - regex: ^PermitRootLogin yes
```

`file.comment` skips lines that are already commented, so it is idempotent;
`file.uncomment` matches the regex against the line with its comment
character removed.

## pkg

Backend is auto-detected: FreeBSD pkg(8); apt, dnf, yum, zypper, pacman,
apk on Linux; Homebrew on macOS; Chocolatey then winget on Windows.

### pkg.installed

| Arg | Description |
|---|---|
| `name` | single package (default: state ID) |
| `pkgs` | list of packages |
| `version` | install this exact version (one package per state) |
| `hold` | `true` pins the installed version against upgrades; `false` releases it |

```yaml
tools:
  pkg.installed:
    - pkgs:
      - tmux
      - git
      - htop

nginx:
  pkg.installed:
    - version: 1.24.0-1~bookworm
    - hold: true
```

`version` compares against the installed version and installs the pinned
spec when they differ, so a downgrade is a change like any other. `hold`
is separate: it is the package manager's own lock, and a state that says
nothing about `hold` never touches one.

Backend support, because these are the package manager's features and not
halite's:

| Backend | `version` | `hold` |
|---|---|---|
| pkg(8) | `pkg-1.2.3` | `pkg lock` |
| apt | `pkg=1.2.3` | `apt-mark hold` |
| dnf / yum | `pkg-1.2.3` | `versionlock` (needs the plugin) |
| zypper | `pkg=1.2.3` | `zypper addlock` |
| apk | `pkg=1.2.3` | — |
| brew | — | `brew pin` |
| choco | — | `choco pin` |
| pacman, winget | — | — |

A `version` or `hold` the backend cannot express fails the state. Silently
installing whatever is current would be the wrong package, quietly.

### pkg.removed

Same args; ensures packages are absent.

## pkgrepo

### pkgrepo.managed

Write a repository definition for the host's package manager, and refresh
its metadata when the file changes.

| Arg | Platform | Description |
|---|---|---|
| `name` | all | repository name; also the file name (default: state ID) |
| `url` | all | repository URL (`baseurl` for the RPM families) |
| `enabled` | all | `false` writes the definition disabled |
| `refresh` | all | `false` skips the metadata refresh after a change |
| `dist`, `comps`, `arch`, `signed_by`, `line`, `source` | apt | suite, components, architecture, keyring path; `line` is taken verbatim |
| `humanname`, `baseurl`, `metalink`, `mirrorlist`, `gpgkey`, `gpgcheck`, `priority`, `module_hotfixes` | dnf, yum, zypper | written into the `.repo` file as given |
| `mirror_type`, `signature_type`, `fingerprints`, `priority` | pkg(8) | written into the repo conf |

```yaml
nginx-upstream:
  pkgrepo.managed:
    - url: https://nginx.org/packages/debian
    - dist: bookworm
    - comps: nginx
    - signed_by: /etc/apt/keyrings/nginx.gpg
```

Files land in `/usr/local/etc/pkg/repos/<name>.conf` (FreeBSD),
`/etc/apt/sources.list.d/<name>.list`, `/etc/yum.repos.d/<name>.repo`,
`/etc/zypp/repos.d/<name>.repo`, or `/etc/apk/repositories.d/<name>`.
pacman, Homebrew, Chocolatey, and winget have no repository file to write,
and the state says so rather than doing nothing.

**halite does not fetch signing keys.** `signed_by` and `gpgkey` point at
a key that a `file.managed` (with `require`) puts there first. A
repository state that downloaded and trusted a key would be the wrong
default.

### pkgrepo.absent

Removes the definition and refreshes. Args: `name`, `refresh`.

## ssh_auth

### ssh_auth.present

Manage one entry in a user's `authorized_keys`.

| Arg | Description |
|---|---|
| `name` | the key: a bare base64 body, or a whole authorized_keys line (default: state ID) |
| `user` | the account whose file is managed (required) |
| `enc` | key type when `name` is a bare body (default `ssh-rsa`) |
| `comment` | trailing comment |
| `options` | list of sshd options (`no-pty`, `command="…"`) |
| `config` | override the file path (default `~user/.ssh/authorized_keys`) |

```yaml
ed@laptop:
  ssh_auth.present:
    - user: ed
    - enc: ssh-ed25519
    - name: AAAAC3NzaC1lZDI1NTE5AAAAIB6mFbT4tGvJv7nFqz0v0N0i0wKmrGV0i2Yh3nQeXamp
    - options:
      - no-agent-forwarding
```

The key body identifies the entry, so changing options, type, or comment
rewrites that line instead of adding a second copy of the same key. The
`.ssh` directory is created `0700` and the file `0600`, both owned by the
user — sshd ignores them otherwise.

### ssh_auth.absent

Removes the entry whose key body matches. Other keys in the file are left
untouched. Args: `name`, `user`, `config`.

## host

### host.present

Ensure a hostname resolves to an address in the hosts file
(`/etc/hosts`, or `config` to name another).

| Arg | Description |
|---|---|
| `name` | the hostname (default: state ID) |
| `names` | several hostnames for one address — the compiler's `names:` expansion, one state per name |
| `ip` | the address (required) |
| `clean` | `true` also removes these names from other addresses |
| `config` | override the hosts file path |

```yaml
db1:
  host.present:
    - ip: 10.0.0.1
    - names:
      - db1
      - db1.internal
```

`names:` is the state compiler's own expansion, as it is in Salt: it
declares the state once per name, and each adds its name to the line that
already carries the address. Names for one address therefore land on one
line, which is what the file's readers expect and what a second run has to
leave alone. Comments, blank lines,
and every address the state does not name are kept exactly as they were.

Without `clean`, a name that also appears on another address is left
there: two addresses for one name is usually a leftover, but removing one
is destructive enough to be asked for.

### host.absent

Remove a hostname. Args: `name`, `names` (the same expansion), `ip`
(restrict to one address), `config`. A line left with no names is removed.

## kmod

### kmod.present / kmod.absent

Load or unload a kernel module. Args: `name`, `persist`, `config`.

```yaml
nfs:
  kmod.present:
    - persist: true
```

| Platform | Loads with | Persists in |
|---|---|---|
| Linux | `modprobe` | `/etc/modules-load.d/halite.conf` |
| FreeBSD | `kldload` | `/boot/loader.conf` (`<name>_load="YES"`) |

Nothing else has kernel modules, and the state says so rather than
pretending. `persist` adds or removes one line and leaves the rest of the
file alone — `/boot/loader.conf` belongs to the host, not to halite.
Module names fold dashes to underscores, so `ip-tables` and `ip_tables`
are the same module.

## timezone

### timezone.system

Set the system timezone. Arg: `name` (a tzdata zone).

```yaml
America/Los_Angeles:
  timezone.system:
```

A zone with no file under `/usr/share/zoneinfo` fails the state: a typo
that quietly left the host on UTC would be worse. Where `timedatectl`
exists it owns the setting; elsewhere the zoneinfo file is installed as
`/etc/localtime` and the name recorded in `/var/db/zoneinfo` (FreeBSD) or
`/etc/timezone`, which is what `tzsetup` and `dpkg-reconfigure` come down
to. Not implemented on Windows.

## locale

### locale.system

Set the system locale. Args: `name`, `key` (default `LANG`).

```yaml
en_US.UTF-8:
  locale.system:
```

Linux only. `localectl` owns the setting where it exists; otherwise the
key is written to `/etc/default/locale` (Debian) or `/etc/locale.conf`,
keeping the other keys. FreeBSD has no single system-wide locale — it is
`login.conf` and shell profiles — so the state fails there rather than
writing a file nothing reads.

## alternatives

### alternatives.install / remove / set

Drive the alternatives system (`update-alternatives`, or `alternatives`
on RHEL).

| State | Args | Does |
|---|---|---|
| `alternatives.install` | `name`, `link`, `path`, `priority` | registers a candidate |
| `alternatives.remove` | `name`, `path` | withdraws a candidate |
| `alternatives.set` | `name`, `path` | points the link at one candidate, leaving automatic mode |

```yaml
editor:
  alternatives.install:
    - link: /usr/bin/editor
    - path: /usr/bin/vim
    - priority: 100

editor-choice:
  alternatives.set:
    - name: editor
    - path: /usr/bin/vim
    - require:
      - alternatives: editor
```

`alternatives.set` on a path that is not registered fails rather than
installing it: choosing something the system does not offer is a
different intent from adding it.

## pip

### pip.installed

Install Python packages, into a virtualenv when the state names one.

| Arg | Description |
|---|---|
| `name` | one requirement, e.g. `django==4.2` (default: state ID) |
| `pkgs` | several requirements |
| `requirements` | a requirements file to install from |
| `bin_env` | a virtualenv directory, or a path to a `pip` |
| `upgrade` | `true` passes `--upgrade` |

```yaml
app requirements:
  pip.installed:
    - bin_env: /opt/app/venv
    - requirements: /opt/app/requirements.txt

django:
  pip.installed:
    - name: django==4.2
```

An exact `==` pin is compared against what `pip freeze` reports, so a
downgrade is a change like any other. Anything looser — `>=4.0`, a marker,
an extra — is left for pip to judge, because reimplementing PEP 440 to
second-guess it would be worse than asking. Names fold the way pip folds
them, so `zope.interface` and `zope_interface` are one package.

A `requirements` file is pip's to read: the state runs pip and reports the
difference between the freeze before and after, which is also how it
reports the transitive installs a requirement pulled in.

### pip.removed

Uninstall Python packages. Args: `name`, `pkgs`, `bin_env`.

## virtualenv

### virtualenv.managed

Create a Python virtual environment, and install a requirements file into
it. Args: `name` (the directory), `python` (default `python3`),
`requirements`.

```yaml
/opt/app/venv:
  virtualenv.managed:
    - requirements: /opt/app/requirements.txt
```

The environment is created with `python3 -m venv`, and the requirements go
in through that environment's own pip — the same code path as
`pip.installed` with `bin_env`.

## selinux

Linux only, and only where the policycoreutils tools are installed.

### selinux.mode

Set the enforcement mode. Arg: `name` — `enforcing`, `permissive`, or
`disabled`.

```yaml
enforcing:
  selinux.mode:
```

The running mode and the configured one are set together: they can differ,
and a state that changed only one would report success for a host that
reverts on the next reboot. `enforcing` and `permissive` switch at run
time; crossing `disabled` in either direction cannot, so the state writes
the configuration and says a reboot is needed rather than pretending.

`SELINUXTYPE` and the comments in `/etc/selinux/config` — which explain
the very values being set — are left alone.

### selinux.boolean

Set an SELinux boolean. Args: `name`, `value` (default `true`), `persist`
(default `true`).

```yaml
httpd_can_network_connect:
  selinux.boolean:
    - value: true
```

`persist` is the default because `setsebool` without `-P` is lost on the
next reboot, which is rarely what a state file means.

## x509

Keys and certificates, from `crypto/x509` — the same standard library the
fleet CA uses, so what these states write is readable by openssl and
everything else. This is for a host's own TLS material; the fleet's own
PKI is [pki.md](pki.md).

### x509.private_key_managed

| Arg | Description |
|---|---|
| `name` | the key file (default: state ID) |
| `algo` | `ec` (P-256, the default) or `rsa` |
| `bits` | RSA size, at least 2048 (default 2048) |
| `new` | `true` rotates the key |
| `mode` | default `0600` |
| `user`, `group`, `makedirs` | as elsewhere |

```yaml
/usr/local/etc/ssl/site.key:
  x509.private_key_managed:
    - algo: ec
```

An existing key of the right kind is **left alone**: rotating one
invalidates every certificate signed from it, so it happens only on
`new: true` or when `algo`/`bits` no longer match. A key found with a
loose mode is chmodded back without being rotated.

### x509.certificate_managed

| Arg | Description |
|---|---|
| `name` | the certificate file (default: state ID) |
| `private_key` | the key to certify (required) |
| `CN` | common name (default: the file's base name) |
| `O`, `OU`, `C` | organisation, unit, country |
| `subject_alt_names` | `DNS:name`, `IP:address`, `email:address`; a bare entry is a DNS name |
| `days_valid` | lifetime, default 365 |
| `days_remaining` | renew when the certificate expires within this many days, default 28 |
| `ca` | `true` issues a signing certificate rather than a serving one |
| `signing_private_key`, `signing_cert` | sign with this CA instead of self-signing |

```yaml
/usr/local/etc/ssl/site.crt:
  x509.certificate_managed:
    - private_key: /usr/local/etc/ssl/site.key
    - CN: site.example.com
    - subject_alt_names:
      - DNS:site.example.com
      - IP:10.0.0.5
    - require:
      - x509: /usr/local/etc/ssl/site.key
```

The certificate is reissued when it is missing, inside the renewal
window, no longer matches its private key, or its common name, alternative
names, or `ca` flag differ from the state. `days_remaining` is what makes
a converging fleet renew itself: each run checks, and reissues in the
window rather than at expiry.

A server certificate with no `subject_alt_names` gets its common name as
one, because nothing modern accepts a certificate without. A `ca: true`
certificate does not, since a CA is identified by its subject.

Shortening `days_valid` does not reissue a certificate that is still
outside the renewal window — it applies to the next issuance. Reissuing
because the configured lifetime shrank would be churn, not convergence.

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

### service.enabled / service.disabled

Set whether a service starts at boot, without starting or stopping it now.
Args: `name`.

```yaml
pf:
  service.enabled:
```

`service.running` with `enable: true` covers the usual case; these exist
for the one it cannot express — a service that should come up on the next
boot but must not be started by this run, or one that should stop coming
up without being stopped now.

A backend that cannot report enablement (launchd, sysvinit) fails the
state rather than acting blindly: without a probe every run would report a
change, and being idempotent about boot configuration is the point.

### service.dead

Ensure a service is stopped. Args: `name`.

## network

### network.system

Set the host's own name.

```yaml
web1.example.com:
  network.system:

set-hostname:
  network.system:
    - hostname: web1.example.com
```

The name is applied now *and* recorded for the next boot —
`hostnamectl` where it exists, otherwise `hostname` plus `sysrc hostname=`
(FreeBSD), `scutil --set` (macOS), or `/etc/hostname`. A hostname that
reverts on the next reboot is the failure this state exists to prevent, so
it reports drift when the running name and the recorded one disagree.

Salt's `network.system` also writes the RHEL-era `/etc/sysconfig/network`
switches. halite sets the hostname and nothing else: interface
configuration is a stated non-goal, and half a state would be worse than
none. Not implemented on Windows.

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
command or schedule can change without orphaning the entry. On Windows
there is no crontab, so these states drive scheduled tasks instead — see
[cron on Windows](#cron-on-windows) below.

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

### cron on Windows

There is no crontab, so `cron.present` and `cron.absent` drive
`schtasks` instead, creating tasks under a `\halite\` folder named for
the state's `identifier`.

Cron's five fields are more expressive than the task scheduler's schedule
types, so only what maps cleanly is accepted:

| Cron | Becomes |
|---|---|
| `minute: "*/30"` | `/SC MINUTE /MO 30` |
| `minute: "30"`, `hour: "*/2"` | `/SC HOURLY /MO 2 /ST 00:30` |
| `minute: "5"` | `/SC HOURLY /MO 1 /ST 00:05` |
| `minute: "15"`, `hour: "3"` | `/SC DAILY /ST 03:15` |
| `+ dayweek: "1"` (or `mon`) | `/SC WEEKLY /D MON` |
| `+ daymonth: "14"` | `/SC MONTHLY /D 14` |

Anything else — a `month`, lists like `0,30`, ranges like `0-30`, both
`daymonth` and `dayweek` — is refused by name rather than approximated
into a schedule that is nearly right.

Two limitations to know:

* **Only the command is compared** when deciding whether a task has
  drifted. The scheduler does not report a schedule in a form worth
  parsing back, so changing only the schedule needs a `cron.absent`
  followed by a `cron.present`.
* **This path has not been exercised on a real Windows host.** The
  translation from cron fields to scheduler flags is unit tested and the
  rest is three `schtasks` invocations, but treat the first run on Windows
  as the real test.

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

## archive

### archive.extracted

Unpacks a tar, tar.gz, or zip archive into a directory.

| Arg | Description |
|---|---|
| `name` | destination directory (default: state ID) |
| `source` | archive path, or an http(s) URL |
| `source_hash` | `sha256=<hex>` (or a bare hex digest) |
| `archive_format` | `tar`, `tar.gz`, or `zip`; inferred from the extension otherwise |
| `if_missing` | skip entirely if this path exists |

A relative `source` resolves against the SLS file, like `file.managed`'s.
A remote `source` **must** carry a `source_hash`: downloading and
unpacking whatever the network returns is not something a state should do
quietly. The download is verified before anything is extracted, and a file
that fails the check is deleted rather than unpacked.

Extraction refuses entries that would land outside the destination and
anything that is not a regular file or directory, so a hostile archive
cannot write to `/etc` or drop a symlink.

Idempotency: the state compares the archive's top-level entries against
the destination and does nothing when they are all present. `if_missing`
is cheaper and more precise when you have a sentinel path.

```yaml
/opt/app:
  archive.extracted:
    - source: https://example.com/app-1.2.tar.gz
    - source_hash: sha256=4f3c9e...
    - if_missing: /opt/app/bin/app
```

## git

### git.latest

Clones a repository, or brings an existing checkout to the tip of a
branch, tag, or commit. Shells out to `git`.

| Arg | Description |
|---|---|
| `name` | repository URL (default: state ID) |
| `target` | checkout directory (required) |
| `rev` | branch, tag, or commit; defaults to origin's HEAD branch |
| `depth` | shallow clone depth |
| `force` | discard local modifications (default false) |

It refuses to act when the target is a non-empty directory that is not a
repository, when it is a checkout of a different remote, or when it has
uncommitted changes and `force` is not set — silently discarding someone's
work in progress is not a change a configuration run should make on its
own.

```yaml
/usr/local/src/app:
  git.latest:
    - name: https://github.com/example/app.git
    - target: /usr/local/src/app
    - rev: main
    - depth: 1
```

## mount

### mount.mounted

Mounts a filesystem and records it in `/etc/fstab`.

| Arg | Description |
|---|---|
| `name` | mount point (default: state ID) |
| `device` | device or remote path (required) |
| `fstype` | filesystem type (required) |
| `opts` | mount options (default `rw`) |
| `dump` / `pass` | fstab columns 5 and 6 (default `0`) |
| `mkmnt` | create the mount point (default true) |
| `persist` | write the fstab entry (default true) |

Mount options are enforced in fstab only, never against a filesystem that
is already mounted: the kernel reports options nobody asked for, so
comparing them would remount on every run. Unmount and mount again to
change a live filesystem's options.

If the mount point already holds a *different* device, the state fails
rather than unmounting it.

```yaml
/data:
  mount.mounted:
    - device: /dev/ada1p1
    - fstype: ufs
    - opts: rw,noatime
    - pass: "2"
```

### mount.unmounted

Unmounts a filesystem. `persist: true` also removes the fstab entry; it
defaults to false, since unmounting now usually does not mean "and never
mount it again".

Windows: both are unsupported.

# Execution modules

Execution modules answer questions instead of converging anything. They
are read-only, take no requisites, and are not usable in an SLS file —
they run ad hoc, locally or across the fleet:

```sh
halite call disk.usage
halite run '*' call disk.usage
halite run 'os_family:FreeBSD' call status.loadavg
```

| Module | Reports |
|---|---|
| `disk.usage` | space per mounted filesystem: total, used, free, capacity, device, fstype |
| `status.uptime` | seconds up, boot time, and a human form |
| `status.loadavg` | 1, 5, and 15 minute load averages |
| `network.interfaces` | index, MTU, flags, MAC, and addresses per interface |

`disk.usage` reads the mount table the same way `mount.mounted` does and
skips filesystems it cannot stat, so a dead NFS mount does not fail the
whole query. It is not implemented on Windows. `status.*` read `/proc` on
Linux and `sysctl` on the BSDs and macOS; `network.interfaces` is pure
stdlib and works everywhere.

## reg (Windows)

### reg.present

Ensures a registry value exists with the given data.

| Arg | Description |
|---|---|
| `name` | the key, `HIVE\Subkey` (default: state ID) |
| `vname` | the value name; omit for the key's default value |
| `vdata` | the data to write |
| `vtype` | `REG_SZ` (default), `REG_EXPAND_SZ`, `REG_MULTI_SZ`, `REG_DWORD`, `REG_QWORD`, `REG_BINARY` |

Hives may be short or long (`HKLM` or `HKEY_LOCAL_MACHINE`), and forward
slashes are accepted as separators.

```yaml
HKLM\SOFTWARE\Acme:
  reg.present:
    - vname: Timeout
    - vdata: "30"
    - vtype: REG_DWORD
```

Numbers are compared numerically, not as text: `reg query` prints a DWORD
in hex, so `30` and `0x1e` are correctly the same value and the state does
not report a change on every run. Strings are compared exactly, since case
matters in a path; `REG_BINARY` is compared case-insensitively, since it
is hex.

### reg.absent

Removes a value, or a whole key with `delete_key: true`. One of the two is
required — removing a key removes everything beneath it, so it cannot
happen by leaving `vname` out by accident.

```yaml
HKLM\SOFTWARE\Acme:
  reg.absent:
    - vname: Timeout
```

Both states fail on anything other than Windows rather than pretending to
work. Like scheduled tasks, **this path has not been exercised on a real
Windows host**: the key normalisation, `reg query` parsing, and value
comparison are unit tested, and the rest is `reg.exe` invocations.
