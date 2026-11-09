# aws-simfleet: throwaway AWS hosts for the simfleet scale run

One hub and N drivers in one private subnet, for the steps of
[`docs/simfleet-aws-plan.md`](../../../docs/simfleet-aws-plan.md) that
need more than the two existing hosts step 0 used. It is separate from
the Vultr lab in `contrib/tofu`, which uses another provider, another
account and another network.

**It bills from `tofu apply` until `tofu destroy`.** At the plan's sizes
that is about $1.30 an hour for a hub and five drivers (plan §6). Raise
it only with the go-ahead the plan asks for, and destroy it in the same
session.

## What it creates, and what it does not

- One hub (`hub_type`, default c7g.4xlarge: 16 cores, which is SPEC 30's
  "one 16-core hub") and `driver_count` drivers (`driver_type`, default
  c7g.xlarge, 5,000 simulated nodes each).
- Optionally, one cluster placement group.
- **No security group, no IAM, no network.** The subnet, security group
  and instance profile are inputs, and must exist. A module that made
  them could change them.

Every instance has no public address, requires IMDSv2, and is reached
only through SSM. Its root volume is gp3 and is deleted with it. Every
resource and volume is tagged `Project=halite-simfleet`, `Environment`,
`Owner` and `ExpiresAt`. These are the settings `RunInstances` was
dry-run with on 2026-11-08 (plan §3.1). `tests/` holds them against a
mocked provider, so an edit that drops one fails `tofu test`.

**The dead man's switch.** Each instance runs `shutdown -h +ttl_minutes`
at first boot (default 240), and halting terminates it
(`instance_initiated_shutdown_behavior = "terminate"`). A forgotten run
stops billing on its own after four hours. That limits what forgetting
costs; it does not replace `tofu destroy`.

## Use

Credentials come from the environment, never from a variable:

```sh
aws sso login --profile <profile>
export AWS_PROFILE=<profile>
cd contrib/tofu/aws-simfleet
```

Write a local `terraform.tfvars` (the repository ignores `*.tfvars`):

```hcl
subnet_id         = "subnet-…"   # private, NAT egress, one AZ
security_group_id = "sg-…"       # opens the hub's port between instances
instance_profile  = "…"          # has AmazonSSMManagedInstanceCore
owner             = "…"
driver_count      = 1            # step 1: 1; step 2: 2; step 3: 5
```

Then:

```sh
tofu init
tofu plan -out run.tfplan      # reads the account, creates nothing
tofu apply run.tfplan          # bills from here
tofu output -json              # the hub's and drivers' ids and private addresses
tofu destroy                   # in the same session
```

After `destroy`, check that nothing is left with the tag:

```sh
aws resourcegroupstaggingapi get-resources --tag-filters Key=Project,Values=halite-simfleet
```

## The run kit

`kit/sampler.sh NAME OUT [SECONDS]` is H6 of the plan: every few seconds
it appends the RSS, descriptors, threads and CPU of the newest process
named NAME, the host's established and TIME_WAIT sockets, and the ENA
allowance counters. Run it on the hub for `halite-hub` and on each driver
for `simfleet`. It reads `/proc`, so it is Linux only; a FreeBSD hub
(step 3F) needs its own.

## What has and has not been run

- `tofu validate` and `tofu test` (mocked provider) pass with OpenTofu
  1.13.0 and hashicorp/aws 6.72.
- `tofu plan` against a test account (us-east-1) on 2026-11-09, with
  `driver_count = 1`: **2 to add, 0 to change, 0 to destroy**, and
  nothing else. It resolved the current Canonical 24.04 arm64 image and
  the subnet's availability zone, and showed IMDSv2 required, no public address,
  terminate on shutdown, and the tags on both instances and both
  volumes. A plan only reads, so it shows the account accepts the
  references; it does not show `RunInstances` succeeds.
  **Nothing has been applied.** The DryRun of 2026-11-08 is still the
  only check that the API would launch these settings.
- The user data (sysctl, chrony against Amazon Time Sync, the halt) has
  not run on an instance.
- `kit/sampler.sh` ran on an existing test host (Ubuntu 24.04 arm64, kernel
  `6.8.0-1067-aws-fips`) on 2026-11-09, against `systemd-journal`. Its
  RSS, thread and descriptor columns matched a separate read of `/proc`
  for the same PID, and the ENA columns matched `ethtool -S`
  (`conntrack_allowance_available` is not exposed by that driver, and
  reads `-`). The first run, against `amazon-ssm-agent`, wrote only
  dashes: `pgrep -x` matches the kernel's 15-character process name, and
  that name has 16. The script now refuses a name that long.
