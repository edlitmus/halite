# The module library

<!-- abstract: What halite can manage: execution and state modules, how to discover them, virtual modules and providers, the evidence levels, and where the library ends. -->
<!-- kind: concept -->
<!-- index: modules; module library; execution module; state module; virtual module; provider; sys.doc; sys.list_modules; sys.state_doc; sys.argspec; sys.evidence; evidence level; assumed; captured; hardware; cmd.run; cmd_default_shell; arbitrary_code; platform modules; extension -->

Everything halite does to a machine, it does through a **module**: a
named group of functions written in Go, compiled into the node. There is
no code fetched from the hub at run time and no plugin directory.

This chapter is about the library as a whole: how it is organised, how
you find what you need, and how much you can trust a given module.

## Two kinds of function, one name

A module name such as `file` or `service` appears in two places, and the
difference is the most important thing to understand about the library.

| | Execution function | State function |
|---|---|---|
| Question it answers | "Do this **now**." | "Make this **so**." |
| Run by | `halite-hub run 'web*' service.restart nginx` | a state in an SLS file: `service.running` |
| Idempotent? | Not necessarily. `service.restart` restarts every time. | Always: it checks first and acts only on a difference ([chapter 4](04-idempotency.md)). |
| Result | A value you read (`True`, a list, a mapping). | The `result`/`changes`/`comment` structure. |
| Test mode | None; it just does it. | Rehearses; changes nothing. |

State functions are built on execution functions, but they are separate
and named separately (`file.managed` is a state; `file.write` and
`file.append` are execution functions). Use a **state** in your tree and an
**execution function** for ad-hoc work and for asking questions
(`pkg.version curl`, `status.uptime`, `file.stats /etc/passwd`).

This build ships 694 execution functions across 98 modules, and 152 state
functions across 55. Which exist on a given node depends on its platform;
the numbers above are for the build as a whole.

## Finding what exists

You never need to remember a function. Ask the binary, on any node, with no
hub:

```sh
halite-node call sys.list_modules --local             # execution modules
halite-node call sys.list_functions file --local      # functions in one module
halite-node call sys.doc cmd.run --local              # what one function does
halite-node call sys.argspec cmd.run --local          # its parameters, machine-readable
halite-node call sys.list_state_modules --local       # state modules
halite-node call sys.state_doc file.managed --local   # a state function's signature
halite-node call sys.evidence --local                 # how much each module is trusted
```

A signature reads like this:

```
cmd.run(name: string, args: list, shell: any, cwd: path = , runas: string = ,
        umask: string = , env: map, stdin: string = , timeout: duration,
        ignore_retcode: bool = false, bg: bool = false)
  Run a command and return its stdout.
  - changes the system
  - runs arbitrary code; must be granted by name
  - test mode is unreliable
  - needs: whatever the command needs
```

The first line is the parameters: `name: type = default`. A parameter
without a default is required. Below it, the description and the **flags**
that matter:

| Flag | Means |
|---|---|
| changes the system | It mutates the machine. |
| honours `--test` | Its state form rehearses reliably. |
| test mode is unreliable | It cannot predict honestly (`cmd.run`); take care with `prereq`. |
| runs arbitrary code | It can run anything; the authorization policy never grants it by wildcard ([chapter 26](26-authorization.md)). |
| needs: … | The privilege or tool it requires. |

The same information is the generated [module reference](../modules.md),
which lists every function with its parameters in a table, and which a test
keeps in step with the build. The API serves it too, as a schema, so
tooling can validate a request before sending it.

Arguments are **strings unless the signature says otherwise**. `1.0` stays
the string `1.0`; it does not become a float, and `no` does not become
`false`. Structured values are written as JSON on the command line:
`halite-hub run web1 cmd.run uname args='["-a"]'`.

## Virtual modules and providers

Some modules have one name everywhere and several implementations. You write
`pkg` or `service` and halite picks the right provider for the node:

| Module | Providers |
|---|---|
| `pkg` | `aptpkg` (Debian, Ubuntu), `dnfpkg` and `yumpkg` (RHEL family, Amazon Linux), `zypperpkg` (SUSE), `pkgng` (FreeBSD), `mac_brew_pkg` (macOS), `chocolatey` (Windows) |
| `service` | `systemd_service` (Linux), `freebsd_service` (rc.d), `mac_service` (launchd) |
| `firewall` | `pf` (BSD), `ufw` (Ubuntu) |
| `sysctl` | Linux and FreeBSD variants |

The provider-specific names are also valid and mean "this module, and the
platform must match":

```sh
halite-node call aptpkg.version curl          # works on a Debian node
halite-node call freebsdpkg.version curl
```

On the wrong platform the second does not guess; it tells you what to do:

```
freebsdpkg.version: this is the pkgng spelling of `pkg`, and this node's
package manager is aptpkg; call `pkg` instead, which picks the right one
```

`halite-node call sys.list_aliases` prints the whole table for the node
and which spellings are usable there. In a tree, always write the generic
name; the provider-specific ones are for the rare state that is meant for
one platform only.

## What is in the library

A tour by purpose. This is not every function; the reference is. The
module names are the ones to search for.

| To manage | Modules |
|---|---|
| **Files and their contents** | `file`, `archive`, `hashutil`, `data`, `acl`, `chattr`, `tmpfs` |
| **Packages and repositories** | `pkg`, `pkgrepo`, `dpkg`, `rpm`, `debconf`, `snap`, `pro` (Ubuntu Pro), `dnf_module` |
| **Language packages** | `pip`, `virtualenv`, `npm`, `gem`, `cargo`, `go`, `composer`, `cpan`, `maven` |
| **Services and boot** | `service`, `journald`, `sysrc` (FreeBSD rc), `kernelpkg`, `reboot` |
| **Accounts and access** | `user`, `group`, `shadow`, `sudo`, `ssh_auth`, `ssh_known_hosts`, `pam`, `authselect` |
| **Scheduled work** | `cron`, `at`, `schedule` |
| **Storage** | `mount`, `disk`, `lvm`, `mdadm`, `quota`, `swap`, `zfs`, `zpool` |
| **Network and firewalls** | `network`, `hosts`, `dnsutil`, `hostname`, `iptables`, `nftables`, `firewall`, `firewalld`, `netplan` |
| **Kernel and system** | `sysctl`, `kmod`, `modprobe`, `udev`, `timezone`, `locale`, `system`, `status`, `ps` |
| **Security** | `selinux`, `apparmor`, `x509`, `openssl_cert`, `tls`, `random` |
| **FreeBSD** | `jail`, `sysrc`, and the `pkgng` provider |
| **macOS** | `mac_user`, `mac_group`, `mac_shadow`, `mac_defaults`, `mac_power`, `mac_keychain`, `mac_softwareupdate` |
| **Windows** | `win_dacl`, `win_registry`, `win_service`, `win_task` |
| **Running things** | `cmd`, `module`, `environ`, `http`, `test` |
| **The halite runtime itself** | `sys`, `config`, `grains`, `pillar`, `mine`, `event`, `beacons`, `schedule`, `saltutil` |

A few modules do much of the day-to-day work. A short orientation to each:

**`file`** is the largest module. As a state: `managed` (a file's content,
mode and owner), `directory`, `recurse`, `symlink`, `absent`, and the edit
family — `line`, `replace`, `blockreplace`, `append`, `prepend`, `comment`,
`uncomment` — for changing part of a file you do not own entirely. All
writes are atomic: a temporary file in the same directory, `fsync`,
`rename`. A crash never leaves half a file.

**`pkg`** installs, removes, upgrades and queries packages whatever the
manager. State functions: `installed` (with an optional `version` or
`pkgs` list), `latest`, `removed`, `purged`. It drives the package
manager's own binary in non-interactive mode, never a library.

**`service`** starts, stops, enables and disables. `service.running` with
`enable: True` is two guarantees in one. With `watch`, it restarts when a
dependency changes ([chapter 6](06-requisites.md)).

**`user` and `group`** create and reconcile accounts. One behaviour to know:
`groups:` on a user means *these groups the account must be in*, and
leaves other memberships alone; add `remove_groups: true` to make it the
complete set.

**`cmd`** is the escape hatch, and has a safety setting you should
understand.

### `cmd.run` and the shell

`cmd.run` runs a command. By default it is run **through a shell**, so
`echo a >> f` and pipes and `&&` work, as in the older tool, so existing
trees mean what they meant. A shell reads its input a second time, which is
how most injection bugs in configuration management begin: a value
containing `; rm -rf` that a template pasted into a command line.

The hardened alternative is an **argument vector**: no shell, `name` is the
program and `args` its arguments, so nothing can be reinterpreted.

```yaml
list_etc:
  cmd.run:
    - name: /bin/ls
    - args: [-l, /etc]
    - shell: false
```

An estate moves to it gradually: `cmd_default_shell: false` in the node's
configuration makes it the default, and `shell: true` on one state opts
that state back in. `halite-hub migrate --no-cmd-default-shell` audits a
tree and counts the lines that would need converting. Every run through a
shell is logged as such.

`runas` changes user with `setuid` and the target's full group set, never
`su -c`.

Anything in the `cmd` family can run arbitrary code, so it is a
separate permission that a wildcard never grants
([chapter 26](26-authorization.md)).

## How much to trust a module

A library this broad cannot have every module equally proven, and halite
says so. Every module carries an **evidence level**:

| Level | Meaning |
|---|---|
| `assumed` | Tests were written from documentation. Nothing has watched it change a real machine. |
| `captured` | Tests hold output captured from the real tool, so *parsing* is checked. |
| `hardware` | Its changing path has been run against the real tool on a real system, and the note says which and when. |

```sh
halite-node call sys.evidence --local --out json
```

returns, for each module: the level, whether it was demonstrated, whether
it needs root, and a **note** of what was run, on what, and what was *not*
covered. The notes are long on purpose. At three in the morning, "runs on
Linux" is worth little; "driven on Debian 13 with acl 2.3.2, not covered:
a FreeBSD 14 host" is worth a lot.

The rule that stands behind the levels: **a release is not made while any
module that changes the system as root is still `assumed`.** The release
gate fails the build, and the only ways past are to demonstrate the module
on a real machine or to take it out of the build. At the time of writing,
no root-mutating module is `assumed`; the modules at `assumed` are the
ones that do not need root or only compose other modules. The
[generated evidence report](../evidence.md) lists each module's level and
note, is shipped in every release archive, and is checked against the code
by a test, so it cannot say a module was run when it was not.

Practical advice: for a new module in your tree, **rehearse with `--test`
on a throwaway machine first**, read its note, and apply to one canary node
before a fleet. This advice applies to any system; halite just tells you
which modules most deserve it.

## What is not in the library

halite ships the modules a FreeBSD, Linux, macOS and Windows estate actually
applies, not every module its predecessor had. Notable absences:

- **Cloud provisioning, virtualization and network-device modules.**
  Creating instances, driving a hypervisor, configuring a switch. Use a
  tool built for that; halite configures what boots.
- **Most Windows modules.** Four are built; the rest of the Windows set is
  listed as pending in the [ledger](../DIVERGENCE.md) (section 2.3).
- **Container and orchestrator modules** (`docker`, `kubernetes`, `helm`).
  They are in the specification's "extended" tier and not built.
- **Python and Python-based renderers**, in any form.
- **Anything written as a custom module in `_modules/`.** That mechanism is
  gone by design. Its replacement is below.

If your estate needs one of these, the answers are, in order of effort:

1. Do it with `cmd.run` guarded by `creates`/`unless`/`onlyif` (not elegant,
   fine for a one-off).
2. Write an **extension**: a separate signed program that adds a module,
   pillar source, grain provider or returner, running in its own process
   ([chapter 29](29-extensions.md)). Existing custom Python modules can be
   ported: `halite-hub migrate --bridge-skeleton DIR` writes a Go skeleton
   for each one it finds.

## Key ideas

- A module name has an **execution** side ("do now") and a **state** side
  ("make so"). Trees use states; operators use execution functions.
- `sys.list_modules`, `sys.doc`, `sys.state_doc`, `sys.argspec` document the
  library from the binary; the module reference is the same data.
- `pkg`, `service` and similar are **virtual**: write the generic name and
  halite picks the provider for the platform.
- `cmd.run` goes through a shell by default; `shell: false` plus `args` is
  the hardened form.
- Every module has an **evidence level** and a note; use `sys.evidence`,
  and rehearse and canary what you have not seen run.
- What is missing is routed to guarded commands or **extensions**.

Next, Part III: how the fleet is reached.
[The hub and its nodes](12-hub-and-nodes.md).
