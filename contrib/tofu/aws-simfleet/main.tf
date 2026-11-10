# One hub, N drivers and, optionally, a cluster placement group. Nothing
# else: no security group, no IAM, no network. Those exist already, and a
# module that made them would be one that could change them.

data "aws_ssm_parameter" "ami" {
  name = var.ami_ssm_parameter
}

data "aws_subnet" "run" {
  id = var.subnet_id
}

locals {
  # When the dead man's switch fires, as a tag, so that anybody finding
  # these instances in the console knows when they stop billing on their
  # own. plantimestamp() rather than timestamp(): the latter changes on
  # every plan and would make every apply replace every instance's tags.
  expires_at = timeadd(plantimestamp(), "${var.ttl_minutes}m")

  # On every resource and every root volume, so that nothing this module
  # raises is missed by a search on Project=halite-simfleet. Written out
  # here rather than as the provider's default_tags, because default_tags
  # are merged inside the provider, where tests/ cannot see them: a test
  # that the tags are there would be a test of the mock.
  tags = {
    Project     = "halite-simfleet"
    Environment = var.environment
    Owner       = var.owner
    ExpiresAt   = local.expires_at
  }

  user_data = { for role in ["hub", "driver"] : role => templatefile("${path.module}/user-data.sh.tftpl", {
    role        = role
    ttl_minutes = var.ttl_minutes
  }) }
}

resource "aws_placement_group" "run" {
  count    = var.placement_group ? 1 : 0
  name     = "halite-simfleet-${formatdate("YYYYMMDDhhmm", plantimestamp())}"
  strategy = "cluster"
  tags     = local.tags

  lifecycle {
    ignore_changes = [name, tags]
  }
}

# The settings below are the ones RunInstances was dry-run with on
# 2026-11-08 (docs/simfleet-aws-plan.md §3.1): this instance profile,
# subnet and security group, no public address, IMDSv2 required,
# terminate on shutdown, gp3 root deleted with the instance, and tags on
# instance and volume. A change here is a change to what was checked.
resource "aws_instance" "hub" {
  ami                                  = data.aws_ssm_parameter.ami.insecure_value
  instance_type                        = var.hub_type
  subnet_id                            = var.subnet_id
  vpc_security_group_ids               = [var.security_group_id]
  iam_instance_profile                 = var.instance_profile
  associate_public_ip_address          = false
  instance_initiated_shutdown_behavior = "terminate"
  placement_group                      = var.placement_group ? aws_placement_group.run[0].name : null
  user_data                            = local.user_data["hub"]

  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.hub_volume_gb
    delete_on_termination = true
    tags                  = merge(local.tags, { Name = "simfleet-hub" })
  }

  tags = merge(local.tags, { Name = "simfleet-hub", Role = "hub" })

  lifecycle {
    # A new AMI published mid-run, or a later plan's timestamp, is not a
    # reason to replace a hub that is being measured.
    ignore_changes = [ami, tags["ExpiresAt"], root_block_device[0].tags]
  }
}

resource "aws_instance" "driver" {
  count = var.driver_count

  ami                                  = data.aws_ssm_parameter.ami.insecure_value
  instance_type                        = var.driver_type
  subnet_id                            = var.subnet_id
  vpc_security_group_ids               = [var.security_group_id]
  iam_instance_profile                 = var.instance_profile
  associate_public_ip_address          = false
  instance_initiated_shutdown_behavior = "terminate"
  placement_group                      = var.placement_group ? aws_placement_group.run[0].name : null
  user_data                            = local.user_data["driver"]

  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_type           = "gp3"
    volume_size           = var.driver_volume_gb
    delete_on_termination = true
    tags                  = merge(local.tags, { Name = "simfleet-driver-${count.index + 1}" })
  }

  tags = merge(local.tags, { Name = "simfleet-driver-${count.index + 1}", Role = "driver" })

  lifecycle {
    ignore_changes = [ami, tags["ExpiresAt"], root_block_device[0].tags]
  }
}
