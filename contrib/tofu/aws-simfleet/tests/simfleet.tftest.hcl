# The module against a mocked provider: no credentials, no AWS calls.
# What these hold is the configuration RunInstances was dry-run with on
# 2026-11-08 (docs/simfleet-aws-plan.md §3.1), so that an edit that
# drops the dead man's switch, a tag, IMDSv2 or the no-public-address
# rule fails here rather than in an account. They do not show that AWS
# accepts the result; the DryRun and `tofu plan` against the account do.

mock_provider "aws" {
  mock_data "aws_ssm_parameter" {
    defaults = { insecure_value = "ami-0123456789abcdef0" }
  }
  mock_data "aws_subnet" {
    defaults = { availability_zone = "us-east-1a" }
  }
}

variables {
  subnet_id         = "subnet-0000000000000000a"
  security_group_id = "sg-0000000000000000a"
  instance_profile  = "test-profile"
  owner             = "test-owner"
}

run "one_hub_and_drivers_with_the_checked_settings" {
  command = plan

  variables {
    driver_count = 3
  }

  assert {
    condition     = length(aws_instance.driver) == 3
    error_message = "driver_count = 3 did not plan three drivers"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
    i.instance_initiated_shutdown_behavior == "terminate"])
    error_message = "an instance does not terminate when it halts, so the dead man's switch would only stop it, and it would go on billing for its volume"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
    i.associate_public_ip_address == false])
    error_message = "an instance asks for a public address"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
    i.metadata_options[0].http_tokens == "required"])
    error_message = "an instance allows IMDSv1"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
    i.root_block_device[0].delete_on_termination && i.root_block_device[0].volume_type == "gp3"])
    error_message = "a root volume would outlive its instance, or is not gp3"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
    i.vpc_security_group_ids == toset(["sg-0000000000000000a"]) && i.subnet_id == "subnet-0000000000000000a"])
    error_message = "an instance is not in the given subnet and security group alone"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
    can(regex("shutdown -h \\+240 ", i.user_data))])
    error_message = "the user data does not halt the instance after ttl_minutes"
  }
  assert {
    condition = alltrue([for i in concat([aws_instance.hub], aws_instance.driver) :
      can(i.tags["ExpiresAt"]) && i.tags["Project"] == "halite-simfleet" && i.tags["Owner"] == "test-owner" &&
    i.root_block_device[0].tags["Project"] == "halite-simfleet" && can(i.root_block_device[0].tags["ExpiresAt"])])
    error_message = "an instance or its root volume is missing ExpiresAt, Project or Owner"
  }
  assert {
    condition     = aws_instance.hub.instance_type == "c7g.4xlarge" && aws_instance.driver[0].instance_type == "c7g.xlarge"
    error_message = "the default sizes are not the plan's"
  }
  assert {
    condition     = length(aws_placement_group.run) == 0
    error_message = "a placement group was planned without being asked for"
  }
}

run "placement_group_when_asked" {
  command = plan

  variables {
    placement_group = true
    ttl_minutes     = 60
  }

  assert {
    condition     = length(aws_placement_group.run) == 1 && aws_placement_group.run[0].strategy == "cluster"
    error_message = "placement_group = true did not plan one cluster placement group"
  }
  assert {
    condition     = can(regex("shutdown -h \\+60 ", aws_instance.hub.user_data))
    error_message = "ttl_minutes = 60 did not reach the user data"
  }
}

run "a_long_ttl_is_refused" {
  command = plan

  variables {
    ttl_minutes = 600
  }

  expect_failures = [var.ttl_minutes]
}
