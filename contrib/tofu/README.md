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

**Nothing has been run yet.** This configuration has been validated
against the real provider schema and every OS name in it resolves
against Vultr's live catalogue, but no instance has been raised from it.
The package lists in `distros.tf` were written from documentation, and
the first `make lab-test` is what establishes whether the names are
right — see "package names are recorded, not assumed" below.

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
Rocky 9. Nobody here has logged into five of these distributions, so the
bootstrap installs packages **one at a time** and records any name that
does not resolve as `packages_missing` in `/var/lib/halite-lab/facts`
instead of aborting the boot. `make lab-facts` prints it. A wrong guess
costs a line in a report rather than a machine, and the first run is how
the real names get learnt.

The same file records what the machine actually is —
`/etc/os-release`, kernel, architecture — because one row, `alma8`, is
an assumption about Vultr's naming (their catalogue lists an unversioned
"AlmaLinux x64" beside "AlmaLinux 9" and "AlmaLinux 10"). If it turns
out not to be 8, the facts say so rather than the lab reporting an 8
result from a 9 machine.

**A half-provisioned host is refused.** `/var/lib/halite-lab/ready` is
written last and only on success. `lab.sh` will not ship a tree to a host
that lacks it, because a test result from a machine with a partial
package set and no Go is worse than no result at all. A failed Go
checksum stops the bootstrap for the same reason.

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
