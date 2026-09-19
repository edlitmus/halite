# The Vultr lab

Nine machines that SPEC 27.1 names as supported and that nothing in this
project runs, raised on demand and destroyed afterwards: seven Linux
distributions nothing here has ever booted, and two FreeBSD releases
that are covered only by machines too close to home to trust.

## Why it exists

Between GitHub's runners, beastie and ref-salt1 this estate already
covers Ubuntu 24.04, Windows, macOS, FreeBSD and one arm64 Linux.
Everything else in SPEC 27.1's tier table is a claim with no machine
behind it. Two of them are worse than untested: `internal/builtin`'s
evidence table says in so many words that the **dnf/yum** and **apk**
package providers "have not been driven at all", and `selinux` is absent
from the build entirely because there has been no host to write it
against.

| Row | What it unlocks | Tier |
|---|---|---|
| `rocky9` | the dnf provider, `selinux`, `firewalld` | 1 |
| `alma8` | the older RHEL line and its yum-era tooling | 1 |
| `alpine` | the apk provider; the only musl and OpenRC machine here | 2 |
| `opensuse16` | zypper, which is not implemented yet | 2 |
| `debian13` | Debian 13 | 1 |
| `debian13sysv` | the sysvinit service provider, on a Debian converted to it | 1 |
| `ubuntu2204` | the oldest tier 1 Ubuntu | 1 |
| `ubuntu2604` | the newest tier 1 Ubuntu | 1 |

`distros.tf` carries the same table with the reasoning next to each row.

**One row changes the machine it was given.** `debian13sysv` is the same
image as `debian13` with `sysvinit-core` in place of `systemd-sysv`,
converted at first boot and rebooted into it, because Vultr's catalogue
has no Devuan and nothing else on it is not systemd. Its ready file is
written *after* the reboot by an init script the bootstrap installs and
which removes itself — a machine that has converted and not rebooted is
still running systemd, and is not the machine that row is for. The facts
say which one answered: `init_pid1`, `init_runlevel`,
`init_conversion`.

## What it deliberately does not cover

**arm64.** SPEC 27.1 asks for tier 1 on "amd64 and arm64" and Vultr sells
no ARM instance at all — `vultr-cli plans list` has no such row — so
every machine here is amd64 and the arm64 half of the tier stays with
ref-salt1. This is a gap in the lab, not one it closes.

**Amazon Linux 2023.** A tier 1 platform, not offered off AWS, and
therefore still untested anywhere in this estate.

**macOS.** Nothing in this estate runs the eight `mac_*` modules the
release gate names, and no cloud offers a Mac the way Vultr offers a
Linux box. EC2 Mac is the only rentable one and its dedicated hosts have
a minimum allocation of 24 hours. That gap is closed by a real Mac, not
by this lab.

## What the first run established

All seven were raised on 2026-09-13 and every one bootstrapped:

| Row | Reported itself as | Missing packages |
|---|---|---|
| `rocky9` | Rocky Linux 9.8 (Blue Onyx) | none |
| `alma8` | AlmaLinux 8.10 (Cerulean Leopard) | none |
| `alpine` | Alpine Linux v3.24 | none |
| `opensuse16` | openSUSE Leap 16.0 | none |
| `debian13` | Debian GNU/Linux 13 (trixie) | none |
| `ubuntu2204` | Ubuntu 22.04.5 LTS | none |
| `ubuntu2604` | Ubuntu 26.04.1 LTS | none |

Go 1.26.6 verified against its pinned checksum on all seven. Two things
that were assumptions are now facts: the unversioned "AlmaLinux x64" in
Vultr's catalogue **is** the 8 series, and every package name in
`distros.tf` — written from documentation, by somebody who had never
logged into five of these distributions — resolved on its distribution.
The per-package install loop reported nothing missing anywhere.

### And what the sweeps established

`make lab-test` across all seven, on the branch that added them: **0
failed anywhere**, 62 unit packages ok on each.

| Row | live |
|---|---|
| alma8 | 40 passed, 46 skipped |
| alpine | 28 passed, 58 skipped |
| debian13 | 48 passed, 38 skipped |
| opensuse16 | 42 passed, 44 skipped |
| rocky9 | 41 passed, 45 skipped |
| ubuntu2204 | 53 passed, 33 skipped |
| ubuntu2604 | 53 passed, 33 skipped |

The skip column is the one worth reading. Alpine skips most because it is
neither systemd nor glibc nor dpkg and its ps cannot answer a CPU sort;
Ubuntu 22.04 skips fewest because it is closest to the platform the suite
was written against. Every skip carries its reason, so a count that moves
can be chased.

It took three sweeps. The first two found fifteen defects between them --
see DIVERGENCE 5.74, 5.76 and 5.77 -- most of them one platform's
spelling assumed universal. **This is what the lab is for**, and the
figure to watch is not that they now pass but that they did not.

**What it has still not established**: the dnf and apk providers have
still never been driven. The only live `pkg` tests are dpkg-specific and
skip on RHEL and Alpine, so those rows prove the tree builds and behaves
there, not that their package providers work. `evidence.go` says so, and
it remains true.

## Running it

The API token is read from the environment and never written to a file
in this repository:

```sh
export VULTR_API_KEY=...      # same token vultr-cli uses
make lab-up                   # raise all seven
make lab-wait                 # block until each has provisioned
make lab-test                 # build, unit suite and live suite on each
make lab-down                 # destroy them
```

`make lab-down` is the one that matters. These bill hourly against their
plan's monthly cap for as long as they exist, and nothing destroys them
on a timer.

To avoid exporting it every session, put it in a file **outside the
worktree** and `make` will load it when the environment has none:

```sh
mkdir -p ~/.config/halite && chmod 700 ~/.config/halite
printf 'VULTR_API_KEY=...\n' > ~/.config/halite/lab.env
chmod 600 ~/.config/halite/lab.env
```

`LAB_ENV` in the Makefile points there and can be overridden. The
environment always wins, so an exported key needs no file.

Not a `.env` in the repository, and not a `.tfvars`. `.env` is the
filename `git add -A` sweeps up. `.tfvars` is ignored tree-wide and so
would survive that, but it would make the token a tofu *variable* --
which reaches saved plan files and `tofu console` -- and `vultr-cli`
reads the environment regardless, so the key would end up in two places.
A file under `$HOME` serves both and cannot be committed from here.

Useful variations:

```sh
make lab-up   LAB_DISTROS='["rocky9","alpine"]'   # a subset
make lab-test DISTRO=rocky9                       # one host
make lab-facts                                    # what each machine says it is
make lab-ssh  DISTRO=rocky9                       # a shell on one
make lab-distros                                  # the row names
make lab-plan                                     # a plan, changing nothing
```

`make lab-down` takes the same `LAB_DISTROS` as the `lab-up` that
created them.

## Cost

Nine `vc2-1c-2gb` instances. Vultr bills hourly against a monthly cap,
so a sweep that raises them, runs the suite and destroys them costs a
few cents; leaving them all running costs the sum of their monthly caps.
`vultr-cli plans list` has the current numbers, and
`tofu output monthly_cost_if_left_running` prints what is up.

## Security

These are internet-facing machines that accept key-only root SSH and
exist to be handed this source tree and told to run its live tests as
root — tests that make filesystems, load kernel modules and reconfigure
packet filters.

- SSH is the only port open, and only to the address `make lab-up`
  detected, or the one `LAB_SSH_CIDR` names. `variables.tf` refuses
  `0.0.0.0/0` outright and has no default, because a default here would
  eventually be the default somebody ran with.
- The bootstrap turns off password authentication once the key is in
  place. Vultr mails a root password for every instance; after this it
  opens nothing.
- `activation_email` is off, so that password is not mailed at all.
- State files and `*.tfvars` are ignored by git. `.terraform.lock.hcl` is
  **not** ignored: it pins the provider the way `go.sum` pins this
  project's one dependency.

## The FreeBSD rows, and why a covered platform is on this list

Every other row is here because nothing in this estate has booted it.
The two FreeBSD rows are here because of *how* FreeBSD is covered.

It became tier 1 on 2026-09-16, and it has exactly two machines: beastie,
which is the development host, and CI's leg, which is an emulated VM
booted inside an Ubuntu runner. Neither is a plain FreeBSD machine
somebody else installed, and the development host in particular makes
every FreeBSD leg *feel* covered. DIVERGENCE 5.77's `/sbin/shutdown`
failure passed on beastie and failed in CI for that reason.

The first boot, 2026-09-17, made the point on its own:

| Row | Reported itself as | Missing packages | live suite |
|---|---|---|---|
| `freebsd14` | 14.5-RELEASE | none | 28 passed, 59 skipped, **1 failed** |
| `freebsd15` | 15.1-RELEASE-p3 | none | 29 passed, 59 skipped, 0 failed |

The failure is DIVERGENCE 5.113: **`getfacl -s` was added in FreeBSD
15**, and `acl.is_extended` passes it unconditionally, so on 14 it
answers every path with `getfacl: illegal option -- s`. beastie is 15 and
so is the CI leg, so neither could ever have found it — and note that
`freebsd15` here reports 15.1, the same release CI emulates, and passes.
The row that earned its keep was the one running the version nothing
else in the estate runs.

Both rows carry almost no package list, which is the point rather than
an omission: `pf`, `jail`, `zfs`, `sysrc`, the rc.d scripts, UFS quotas,
`fetch` and `sha256` are all in base.

### Two things about FreeBSD that the Linux rows do not need

**Vultr does not run cloud-init on its BSD images.** `user_data` is
silently inert there; the supported mechanism is a Vultr *startup
script* attached to the instance, and `main.tf` creates one per BSD row.
The API takes it base64-encoded and the provider does not encode it for
you, so plain text would have been run as one very long unknown command.
It also fires *earlier* in boot than cloud-init does: `/etc/os-release`
is a symlink into `/var/run` that an rc service had not yet written when
the script ran, so no `os_*` fact is recorded on these rows and
`freebsd-version` is the fact that answers the question.

**root's login shell is tcsh.** `ssh host "cd dir && VAR=value cmd"` runs
through the login shell, and csh has no leading-assignment syntax, so
every command `lab.sh` sends would fail on these two rows and on no
others. The bootstrap runs `pw usermod root -s /bin/sh`, which is a
smaller and more honest fix than teaching `lab.sh` to quote for two
shells.

## How it is built, and the two traps in it

**`user_data` is not ForceNew.** The provider base64-encodes the script
and sends it only on *create*; it is never read back and never sent on
update. Editing `bootstrap.sh.tftpl` and re-applying would therefore
report success over a machine still provisioned the old way. A
`terraform_data` keyed on the rendered script, referenced by
`replace_triggered_by`, turns an edit into a replacement instead. Both
facts were read out of the provider's own source rather than assumed.

**Package names are recorded, not assumed.** `quota` on Debian is
`quota-tools` on Alpine; `iptables` on Alma 8 is `iptables-nft` on
Rocky 9. The lists were written from documentation by somebody who had
logged into two of these seven distributions, so the bootstrap installs
packages **one at a time** and records any name that does not resolve as
`packages_missing` in `/var/lib/halite-lab/facts` rather than aborting
the boot. `make lab-facts` prints it. A wrong guess costs a line in a
report instead of a machine.

They all resolved, on every row — see the first-run table above. The
mechanism stays anyway: the cost of it is one `install` call per package
instead of one per host, and what it buys is that the next row added to
the matrix, or the next release that renames something, reports the
problem rather than failing a boot.

The same file records what the machine actually is —
`/etc/os-release`, kernel, architecture. That is what settled `alma8`,
whose catalogue entry is an unversioned "AlmaLinux x64" listed beside
"AlmaLinux 9" and "AlmaLinux 10": it reports itself as AlmaLinux 8.10.
The entry is still unversioned and can be repointed without its name
changing, so the check earns its keep.

**A half-provisioned host is refused.** `/var/lib/halite-lab/ready` is
written last and only on success. `lab.sh` will not ship a tree to a host
that lacks it, because a test result from a machine with a partial
package set and no Go is worse than no result at all. A failed Go
checksum stops the bootstrap for the same reason.

## When `lab-up` fails with a backup schedule 404

```
Error: error getting backup schedule: {"error":"Invalid instance-id.","status":404}
  with vultr_instance.node["alma8"]
```

This happened on the first run and it is not a configuration error.
Vultr's API returns 404 from `GET /instances/<id>/backup-schedule` for an
instance it has created but not finished registering, and the provider
calls that endpoint unconditionally in Read, immediately after Create
(`resource_vultr_instance.go`, in `resourceVultrInstanceRead`). Nothing
in this repository can stop it, and `backups = "disabled"` does not:
the read happens whatever the setting.

What it leaves behind is worth understanding, because it is not obvious:

- **The instance is fine.** The one that failed was `active`, and was
  answering SSH and finishing its bootstrap a few minutes later. It was
  simply slower to boot than the other six.
- **It is marked tainted**, because terraform cannot tell a failed create
  from a failed read after a successful create. The next plain `apply`
  would destroy a healthy machine and build another.
- **The outputs are not written.** `ssh_targets` reads every instance's
  address, so one unfinished instance leaves the whole output unwritten,
  while `monthly_cost_if_left_running` — which only counts them —
  survives. The result is seven machines running and billing with no way
  for `lab.sh` to address them.

`make lab-repair` is the recovery: it untaints, then applies to converge
and write the outputs. Untainting wholesale is safe here because taint is
not what this lab trusts for health — `/var/lib/halite-lab/ready` is, and
`wait` and `test` both refuse a host that lacks it, so a genuinely broken
instance still fails loudly at the point it would have been tested.

If an instance really is broken, `make lab-down` and start again.

## What `make lab-test` runs

Per host, in order: `go build ./...`, then `go test ./...`, then the live
suite as root with `HALITE_SYSTEM_LIVE=1`. It keeps going after a host
fails and names which ones did.

`HALITE_REBOOT_LIVE` is deliberately **not** set. That gate schedules a
real reboot, and a machine that reboots mid-run takes its results with
it. A disposable lab instance is the right place to run it eventually —
it is disposable in a way beastie is not, which beastie demonstrated —
but it is a deliberate choice rather than something a sweep does on the
way past.

Live tests that skip are kept in the output. A test that skipped is not a
test that ran, and on these rows the skips are the report: they name what
this distribution could not be asked.
