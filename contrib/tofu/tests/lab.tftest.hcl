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

run "internal_ports_between_instances_when_asked" {
  command = apply

  variables {
    internal_ports = [22, 2525]
  }

  assert {
    condition     = length(vultr_firewall_rule.internal) == 4
    error_message = "internal_ports did not make one rule per instance and port"
  }
  assert {
    condition = alltrue([for k, r in vultr_firewall_rule.internal :
      r.protocol == "tcp" && r.subnet_size == 32 &&
      r.subnet == vultr_instance.node[split(":", k)[0]].main_ip &&
    r.port == split(":", k)[1]])
    error_message = "an internal_ports rule is not that TCP port from exactly one instance's own address"
  }
  assert {
    condition = alltrue([for r in vultr_firewall_rule.ssh :
    r.port == "22" && r.subnet == "198.51.100.7"])
    error_message = "internal_ports changed who may reach SSH from outside"
  }
}

run "no_internal_ports_by_default" {
  command = apply

  assert {
    condition     = length(vultr_firewall_rule.internal) == 0
    error_message = "a port is open between instances although internal_ports was empty"
  }
}

run "refuses_user_data_for_a_freebsd_row" {
  command = plan

  variables {
    user_data_files = { freebsd15 = "./tests/operator.pub" }
  }

  expect_failures = [var.user_data_files]
}

run "user_data_file_replaces_the_bootstrap" {
  command = apply

  variables {
    user_data_files = { ubuntu2604 = "./tests/operator.pub" }
  }

  assert {
    condition     = vultr_instance.node["ubuntu2604"].user_data == file("./tests/operator.pub")
    error_message = "the row's user_data is not the named file"
  }
  assert {
    condition     = vultr_instance.node["freebsd15"].script_id != null
    error_message = "naming one row's user-data changed another row's boot"
  }
}
