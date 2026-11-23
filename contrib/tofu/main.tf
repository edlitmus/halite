# The instances themselves, the key that reaches them, and the firewall
# that stops anybody else doing the same.

# The operator's public key, uploaded once and shared by every instance.
#
# Vultr's alternative is mailing a root password, which is both a secret
# in somebody's inbox and a password on an internet-facing root account.
# The bootstrap turns password authentication off entirely once it has
# this key installed.
resource "vultr_ssh_key" "operator" {
  name    = "${var.label_prefix}-operator"
  ssh_key = trimspace(file(pathexpand(var.ssh_public_key_path)))
}

# SSH is the only thing reachable, and only from the addresses named.
#
# These machines are handed this repository and told to run its live
# tests as root -- the tests that make filesystems, load kernel modules
# and reconfigure packet filters. Anything that can reach this port can
# do all of that. The instances are also short-lived, which cuts the
# window but not the consequence, so the rule is an allow-list rather
# than a rate limit or a fail2ban.
resource "vultr_firewall_group" "lab" {
  description = var.open_hub_port ? "${var.label_prefix}: SSH from the operator, the hub port between instances" : "${var.label_prefix}: SSH from the operator only"
}

resource "vultr_firewall_rule" "ssh" {
  for_each = toset(var.allowed_ssh_cidrs)

  firewall_group_id = vultr_firewall_group.lab.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = split("/", each.value)[0]
  subnet_size       = tonumber(split("/", each.value)[1])
  port              = "22"
  notes             = "operator SSH"
}

# The hub port, between the lab's own instances and from nowhere else.
#
# Off by default. A run that puts a hub on one row and nodes on others
# otherwise has only SSH between them, and every lab run of the install
# runbooks so far reached the hub through SSH tunnels: TLS, pinning and
# enrolment were end to end, the network path was not (issue 733, item
# 9). Turned on, each instance's own address may reach the port on every
# instance in the group, and nothing else may. The address is the
# instance's `main_ip` as a /32, so the rule names exactly the machines
# this run raised; a wider subnet would be the region's other tenants.
resource "vultr_firewall_rule" "hub" {
  for_each = var.open_hub_port ? local.selected : {}

  firewall_group_id = vultr_firewall_group.lab.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = vultr_instance.node[each.key].main_ip
  subnet_size       = 32
  port              = tostring(var.hub_port)
  notes             = "hub port from ${each.key}"
}

# The internal_ports, between the lab's own instances, the same way.
#
# One rule per instance and port. No rules unless internal_ports names
# something; variables.tf says what each is for.
resource "vultr_firewall_rule" "internal" {
  for_each = {
    for pair in setproduct(keys(local.selected), var.internal_ports) :
    "${pair[0]}:${pair[1]}" => { name = pair[0], port = pair[1] }
  }

  firewall_group_id = vultr_firewall_group.lab.id
  protocol          = "tcp"
  ip_type           = "v4"
  subnet            = vultr_instance.node[each.value.name].main_ip
  subnet_size       = 32
  port              = tostring(each.value.port)
  notes             = "port ${each.value.port} from ${each.value.name}"
}

# One instance per selected row of the matrix.
#
# `activation_email` is off because the mail Vultr would send carries the
# root password for a machine that is about to stop accepting passwords,
# and it would be sent for every instance of every run.
resource "vultr_instance" "node" {
  for_each = local.selected

  label    = "${var.label_prefix}-${each.key}"
  hostname = "${var.label_prefix}-${each.key}"
  region   = var.region
  plan     = var.plan
  os_id    = data.vultr_os.distro[each.key].id

  ssh_key_ids       = [vultr_ssh_key.operator.id]
  firewall_group_id = vultr_firewall_group.lab.id

  enable_ipv6      = false
  backups          = "disabled"
  activation_email = false

  tags = [var.label_prefix, each.key, each.value.family]

  # Two delivery mechanisms, because Vultr has two.
  #
  # cloud-init runs on the Linux images and **not on the BSD ones**,
  # where the equivalent is a Vultr startup script attached to the
  # instance. Both carry the same rendered script from local.bootstrap;
  # only the route differs, so a row gets exactly one of these and null
  # for the other.
  user_data = each.value.family == "freebsd" ? null : local.first_boot[each.key]
  script_id = each.value.family == "freebsd" ? vultr_startup_script.bootstrap[each.key].id : null

  # An edited bootstrap has to mean a rebuilt machine.
  #
  # Changing `user_data` on its own is an in-place update: the attribute
  # is written and the running instance, which ran cloud-init once at
  # first boot, never sees it. That would leave `lab-up` reporting
  # success over a machine still provisioned the old way -- a lie of
  # exactly the kind an ephemeral lab exists to avoid, since the
  # cheapest correct answer here is always to throw the instance away
  # and boot another. Keying a `terraform_data` on the rendered script
  # makes the change a replacement instead.
  lifecycle {
    replace_triggered_by = [terraform_data.bootstrap[each.key]]
  }
}

# The startup script a BSD row boots from.
#
# Only BSD rows have one. `script` is base64 per the API -- the provider
# does not encode it -- and plain text here would be run as a shell
# script consisting of one very long unknown command.
resource "vultr_startup_script" "bootstrap" {
  for_each = { for name, d in local.selected : name => d if d.family == "freebsd" }

  name   = "${var.label_prefix}-${each.key}"
  type   = "boot"
  script = base64encode(local.bootstrap[each.key])
}

locals {
  # One script per row, from the template its family boots. The FreeBSD
  # template is a separate file rather than a branch; its header says
  # why.
  bootstrap = {
    for name, d in local.selected : name => templatefile(
      d.family == "freebsd" ? "${path.module}/bootstrap-freebsd.sh.tftpl" : "${path.module}/bootstrap.sh.tftpl",
      {
        distro       = name
        family       = d.family
        packages     = d.packages
        closes       = d.closes
        convert_init = d.convert_init
        go_version   = var.go_version
        # Same toolchain, different tarball, so a different checksum.
        # Pinning one and downloading the other is how a lab silently
        # stops verifying anything.
        go_sha256 = d.family == "freebsd" ? var.go_sha256_freebsd : var.go_sha256
      }
    )
  }
}

# What each row boots: the lab bootstrap, or the operator's own user-data
# for a row named in user_data_files.
locals {
  first_boot = {
    for name, d in local.selected : name => (
      contains(keys(var.user_data_files), name) ? file(pathexpand(var.user_data_files[name])) : local.bootstrap[name]
    )
  }
}

resource "terraform_data" "bootstrap" {
  for_each = local.selected

  input = local.first_boot[each.key]
}
