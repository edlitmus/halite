# The Vultr lab

Seven Linux distributions that SPEC 27.1 names as supported and that
nothing in this project has ever run on, raised on demand and destroyed
afterwards.

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
| `ubuntu2204` | the oldest tier 1 Ubuntu | 1 |
| `ubuntu2604` | the newest tier 1 Ubuntu | 1 |

`distros.tf` carries the same table with the reasoning next to each row.

## What it deliberately does not cover

**arm64.** SPEC 27.1 asks for tier 1 on "amd64 and arm64" and Vultr sells
no ARM instance at all — `vultr-cli plans list` has no such row — so
every machine here is amd64 and the arm64 half of the tier stays with
ref-salt1. This is a gap in the lab, not one it closes.

**Amazon Linux 2023.** A tier 1 platform, not offered off AWS, and
therefore still untested anywhere in this estate.

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

The suite itself has not been run across them yet; `make lab-test` is
the next step.

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

Seven `vc2-1c-2gb` instances. Vultr bills hourly against a monthly cap,
so a sweep that raises them, runs the suite and destroys them costs a
few cents; leaving all seven running costs the sum of their monthly caps.
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
