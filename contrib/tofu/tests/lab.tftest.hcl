# The lab module against a mocked provider: no API key, no Vultr calls.
# These hold the firewall's shape: SSH from the operator only, and the
# hub port opened only between this run's own instances, and only when
# asked. They do not show that Vultr accepts the rules; a `make lab-up`
# with open_hub_port does.
#
#   cd contrib/tofu && tofu init && tofu test

mock_provider "vultr" {
  mock_data "vultr_os" {
    defaults = { id = 1 }
  }
  mock_resource "vultr_instance" {
    defaults = { main_ip = "203.0.113.10" }
  }
}

variables {
  allowed_ssh_cidrs   = ["198.51.100.7/32"]
  ssh_public_key_path = "./tests/operator.pub"
  distros             = ["freebsd15", "ubuntu2604"]
}

run "ssh_only_by_default" {
  command = apply

  assert {
    condition     = length(vultr_firewall_rule.hub) == 0
    error_message = "the hub port is open although open_hub_port was not set"
  }
  assert {
    condition = alltrue([for r in vultr_firewall_rule.ssh :
    r.port == "22" && r.subnet == "198.51.100.7" && r.subnet_size == 32])
    error_message = "SSH is open to something other than the operator's address"
  }
}

run "hub_port_between_instances_when_asked" {
  command = apply

  variables {
    open_hub_port = true
  }

  assert {
    condition     = length(vultr_firewall_rule.hub) == 2
    error_message = "open_hub_port did not make one rule per instance"
  }
  assert {
    condition = alltrue([for k, r in vultr_firewall_rule.hub :
      r.port == "4510" && r.protocol == "tcp" && r.subnet_size == 32 &&
    r.subnet == vultr_instance.node[k].main_ip])
    error_message = "a hub-port rule is not TCP 4510 from exactly one instance's own address"
  }
  assert {
    condition = alltrue([for r in vultr_firewall_rule.ssh :
    r.port == "22" && r.subnet == "198.51.100.7"])
    error_message = "opening the hub port changed who may reach SSH"
  }
}

run "refuses_port_22" {
  command = plan

  variables {
    open_hub_port = true
    hub_port      = 22
  }

  expect_failures = [var.hub_port]
}
