# Grains

<!-- abstract: The facts a node knows about itself, how to read them, how to add your own, and why you must not trust them with secrets. -->
<!-- kind: concept -->
<!-- index: grains; grain; custom grains; grains.d; static grains; grains.get; grains.items; grains.item; grains.filter_by; os_family; fips_mode; cloud_grains; refresh; stale; node ID -->

A **grain** is one fact a node has discovered about itself: its operating
system, how many CPUs it has, what addresses it holds, whether it is a
virtual machine. Grains are collected automatically by the node, as read-
only data, each time it starts and on a timer afterwards.

You use grains in two ways:

1. **In templates**, to make a description fit the machine it is applied
   to — `{{ grains['os_family'] }}` ([chapter 7](07-templates.md)).
2. **In targeting**, to choose which machines a command or a state
   applies to — "every machine whose `os_family` is `Debian`"
   ([chapter 13](13-targeting.md)).

> Grains are **what a machine says about itself**. That is exactly why they
> are the right input for "which package manager do I have?" and exactly
> why they are the wrong input for "which secrets may I receive?". Pillar,
> the next chapter, is for the second question.

## Reading grains

On a node (no hub needed):

```sh
halite-node grains get os --local
halite-node grains item os osrelease --local
halite-node grains ls --local
halite-node grains items --local
```

```
system76-pc: Ubuntu
system76-pc:
    os: Ubuntu
    osrelease: 24.04
```

`get` returns one value, `item` returns several as a mapping, `ls` lists
the names, and `items` returns everything. The first column is always the
node ID.

From the hub, against any set of nodes, using the execution functions of
the same names:

```sh
halite-hub run '*' grains.get os_family
halite-hub run 'web*' grains.item osrelease
halite-hub run -G 'os:Ubuntu' grains.items
```

`grains.get` walks nested grains with a colon: `grains.get
ip4_interfaces:enp6s0` or `grains.get systemd:version`. Without the
colon form you would be indexing into dictionaries by hand.

From inside a template, a state or a reactor, use the `grains`
dictionary: `grains['os']`, `grains.get('virtual', 'unknown')`, or the
function form `salt['grains.get']('os_family')`.

## What is collected

The grain names a node reports on Linux, verified on a real machine, are
below. Which exist depends on the platform; macOS, FreeBSD and Windows
report the same families of facts from their own sources.

| Group | Grains |
|---|---|
| Identity | `id`, `host`, `nodename`, `localhost`, `fqdn`, `domain` |
| Operating system | `os`, `os_family`, `osfullname`, `osrelease`, `osrelease_info`, `osmajorrelease`, `oscodename`, `osfinger`, `osarch`, `lsb_distrib_id`, `lsb_distrib_release`, `lsb_distrib_codename` |
| Kernel | `kernel`, `kernelrelease`, `kernelversion`, `kernelparams` |
| CPU and memory | `cpuarch`, `num_cpus`, `cpu_model`, `cpu_flags`, `mem_total` (in megabytes), `swap_total` |
| Virtualization | `virtual`, `virtual_subtype`, `container` |
| Network | `ipv4`, `ipv6`, `ip_interfaces`, `ip4_interfaces`, `ip6_interfaces`, `hwaddr_interfaces`, `dns` |
| Hardware | `manufacturer`, `productname`, `serialnumber`, `biosversion`, `biosreleasedate`, `uuid`, `chassis`, `efi`, `disks`, `ssds` |
| Platform | `init`, `systemd`, `shell`, `path`, `systempath`, `locale_info`, `haliteversion`, `haliteversioninfo`, `saltversion`, `saltversioninfo` |
| Security | `selinux`, `apparmor`, `fips_enabled`, `fips_mode`, `fips_build`, `fips_module`, `secure_boot`, `tpm`, `lockdown` |

A few that are worth knowing by heart:

| Grain | Typical values | Use |
|---|---|---|
| `os` | `Ubuntu`, `Debian`, `RedHat`, `FreeBSD`, `MacOS`, `Windows` | Distribution-specific choices. |
| `os_family` | `Debian`, `RedHat`, `FreeBSD`, … | The right grain for most "which package/service layout?" decisions. Ubuntu and Debian are both `Debian`. |
| `osrelease`, `osmajorrelease` | `24.04`, `24` | Version gates. |
| `kernel` | `Linux`, `FreeBSD`, `Darwin`, `Windows` | The coarsest platform test. |
| `virtual` | `physical`, `kvm`, `VMware`, … | Different tuning for VMs. |
| `num_cpus`, `mem_total` | integers | Sizing: `worker_processes {{ grains['num_cpus'] }}`. |
| `ipv4` | a list | The node's addresses, loopback included. |
| `fips_mode` | true/false | Whether this build and host are in FIPS mode. |

`id` is the node ID. It is chosen once, at enrollment, in this order:
`node_id` in the configuration file, the `HALITE_NODE_ID` environment
variable, the contents of the node's `node_id` file, then (if configured)
a cloud instance ID, then the fully qualified domain name, then the host
name. After enrollment it is written down and stays fixed, so a change of
DHCP lease or DNS cannot make the same machine look like a new one.

## Custom grains

You often want facts halite cannot discover: which rack, which role,
which team owns the box. There are four ways to supply them, from least
to most dynamic.

**A `grains:` block in the node's configuration** (`node.yaml`):

```yaml
grains:
  role: webserver
  datacenter: ams1
```

**A `grains` file** next to the configuration, in YAML:

```yaml
rack: r12
roles:
  - web
  - cache
```

**A directory `grains.d/`** next to the configuration. A file in it that
is **not executable** is read as YAML. A file that **is executable** is
run, and must print one JSON object:

```sh
#!/bin/sh
echo '{"uptime_bucket": "long", "scripted": true}'
```

A script has ten seconds. One that times out, exits non-zero or prints
something that is not JSON is **skipped with a warning**, and the rest of
the grains are still collected — one bad script never costs the node its
identity:

```
grains.d/30-slow.sh: timed out and was skipped
grains.d/40-bad.sh: exited non-zero and was skipped: … exec format error
```

**A signed extension** of kind `grains`, for grains that need real logic
and that you want to ship through the tree ([chapter 29](29-extensions.md)).

All four were run together on one machine, and this is what the node
reported:

```
role: webserver              # from node.yaml
datacenter: ams1             # from node.yaml
rack: r12                    # from the grains file
roles: [web, cache]          # from the grains file
owner: platform              # from a YAML file in grains.d
uptime_bucket: long          # from a script in grains.d
scripted: true               # from a script in grains.d
```

When the same name appears in more than one place, the **last** source
wins, in this order of increasing precedence:

1. the `grains:` block in the configuration file;
2. files in `grains.d/`, in lexical order (so `20-x.sh` overrides
   `10-x.yaml`);
3. the `grains` file.

Custom grains can override core ones: setting `os: ConfigOS` in the
configuration replaced the real `os` grain. That is occasionally what you
want, for testing a template for another platform. (It is how this
handbook compiles the example tree "as FreeBSD" on a Linux machine.) It
is also a reason never to base security decisions on a grain.

> **Not built:** the runtime functions `grains.setval`, `grains.append`
> and `grains.delval`, which edit the grains file from the command line,
> do not exist in this build; they are refused with `"grains.setval" is not
> a function this build ships`. Manage the `grains` file with a
> `file.managed` state instead, which is better anyway: the file is then
> in your tree, reviewed and versioned. Then run
> `halite-hub run <target> saltutil.refresh_grains`.

### Putting grains in the tree

Because the grains file is just a file, the declarative way to set a
custom grain is a state:

```yaml
/etc/halite/grains:
  file.managed:
    - mode: '0644'
    - contents: |
        role: webserver
        rack: {{ pillar['rack'] }}
```

(On FreeBSD the path is `/usr/local/etc/halite/grains`; the example tree
uses a map for this.) Applying it and refreshing makes the grain
available for targeting.

## When grains are collected

- At node start.
- Every `grains_refresh_interval` (default 30 minutes), when the node
  re-collects and pushes the result to the hub.
- On demand: `halite-hub run <target> saltutil.refresh_grains`.

The hub keeps the **last grains each node reported**, and evaluates
grain-based targeting against that cache ([chapter 13](13-targeting.md)).
That is why targeting on a grain you have just changed can still use the
old value: until the node refreshes, the hub does not know. A node
whose grains are older than `grain_stale_after` (default one hour) is
still targeted but is marked stale in the results. After changing a
custom grain, refresh it before targeting on it.

## Cloud grains

On a cloud instance, `cloud_grains: true` makes the node also collect the
instance's metadata (instance ID, type, region, tags and so on) from the
provider's metadata service, using the more secure token-based IMDSv2
only. It is off by default because the lookup costs a network round trip
to a link-local address on every collection, which on a machine that is
not in a cloud is a timeout.

## Grains and per-platform logic

The most common use of grains is a **lookup**: pick a value by platform.
`grains.filter_by` does it in one line and is what a `map.jinja` is built
on ([chapter 7](07-templates.md)):

```jinja
{% set pkgname = salt['grains.filter_by']({
    'Debian': 'apache2',
    'RedHat': 'httpd',
    'FreeBSD': 'apache24',
}, grain='os_family') %}
```

With `merge={...}` it overlays your own values; with `default='Debian'`
it supplies an answer for platforms you did not list.

The other grain functions: `grains.has_value` (is it set?),
`grains.equals` (compare), and `grains.items`.

## Using grains for targeting

```sh
halite-hub run -G 'os:Ubuntu' test.ping
halite-hub run -G 'osmajorrelease:24' status.uptime
halite-hub run -C 'G@os_family:Debian and not G@virtual:kvm' test.ping
```

The first form matches a grain by glob against its value; the compound
form combines matchers with `and`, `or`, `not`. All of the target types
are in [chapter 13](13-targeting.md). The same expressions go in the **top
file** to choose which states a machine gets
([chapter 10](10-top-and-files.md)):

```yaml
base:
  'G@os_family:FreeBSD and web*':
    - match: compound
    - webserver.bsd_tuning
```

## Grains and trust

A grain is whatever the node says it is. A node that has been
compromised, or simply misconfigured, can claim `role: database`. For
**selecting states**, that is acceptable: the node can only change what
it applies to itself, and the hub still decides what it serves.

For **selecting pillar** (and so secrets), it would not be: a node
claiming to be a database could request the database passwords. So the
hub only lets pillar top files match on a short allowlist of grains,
`pillar_trusted_grains`: `id`, `os`, `os_family`, `osrelease`, `kernel`,
`cpuarch`, `virtual`, `fips_mode` by default. A pillar top file that
names any other grain is refused by name, and adding a grain to the list
is a recorded decision ([chapter 9](09-pillar.md)). Custom grains such as
`role` are not trusted by default.

## Key ideas

- Grains are facts a node discovers about itself; they are **read-only
  data** used in templates and targeting.
- Read them with `grains get/item/ls/items`, `grains.get` from the hub,
  or `grains[...]` in templates; use `:` to walk nested ones.
- Add your own with a `grains:` block, a `grains` file, or files and
  scripts in `grains.d/`; precedence is block < `grains.d` < file; a bad
  script is skipped, never fatal.
- The hub targets on **cached** grains: refresh after changing one.
- **Never trust a grain with a secret.** That is what pillar and its
  trusted-grain allowlist are for.

Next: [pillar](09-pillar.md).
