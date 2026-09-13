# The matrix: which machines this lab can raise, and what each one is for.
#
# # Why these seven
#
# SPEC 27.1 names the platforms this project claims to support. Between
# GitHub's runners, beastie and ref-salt1, the estate already covers
# Ubuntu 24.04, Windows, macOS, FreeBSD and one arm64 Linux. Every row
# below is a platform SPEC 27.1 lists and **nothing here has ever run
# on**, ordered by what it unlocks in the code rather than by how easy it
# is to boot:
#
#   - The RHEL family is the largest single gap in the build. The
#     dnf/yum provider is written and has never been driven -- evidence.go
#     says so in those words -- and `selinux` and `firewalld` cannot be
#     implemented at all without a machine that has them. SPEC 27.1 puts
#     RHEL, Rocky and Alma 8 and 9 in tier 1, which is the tier that
#     promises functional tests.
#   - Alpine is the only row here that is neither glibc nor systemd. The
#     `apk` provider has never been driven either, and the service layer's
#     non-systemd path has no other machine to prove it on.
#   - openSUSE is the one row that is not about verifying written code:
#     there is no zypper provider yet, and `pkg` cannot claim SUSE until
#     somebody writes one against a real zypper.
#   - Debian 13 and the two other Ubuntu LTS releases are the cheapest
#     rows and the lowest yield, because apt is the best-verified
#     provider in the build. They are here for version drift, which a
#     single ubuntu-24.04 runner cannot show.
#
# # What is not here, and why
#
# **arm64 is absent because Vultr does not sell it.** `vultr-cli plans
# list` offers no ARM or Ampere row at all, so every instance below is
# amd64. SPEC 27.1 asks for tier 1 on "amd64 and arm64", and the arm64
# half stays with ref-salt1 until some other provider covers it. This is
# a gap in the lab, not a gap that the lab closes.
#
# **Amazon Linux 2023 is absent because it is not offered off AWS.** It is
# a tier 1 platform with no machine anywhere in this estate.
#
# # The OS ids are looked up, not written down
#
# Vultr's numeric ids are stable but meaningless, and a wrong one is a
# different distribution rather than an error. The names below are what
# `vultr-cli os list` prints, matched exactly, so a name that stops
# existing fails at plan time with the name in the message instead of
# quietly raising something else.

locals {
  distro_catalog = {
    rocky9 = {
      os_name = "Rocky Linux 9 x64"
      family  = "rhel"
      # dnf, plus the SELinux userland that `selinux` will need to exist
      # before it can be written.
      packages = "at quota lvm2 mdadm nftables iptables-nft policycoreutils policycoreutils-python-utils selinux-policy-targeted firewalld rsync tar git curl e2fsprogs util-linux"
      closes   = "dnf provider (never driven), selinux, firewalld; SPEC 27.1 tier 1"
    }

    # The unversioned row in Vultr's catalogue is the 8 series; `Rocky
    # Linux 9 x64` and `Rocky Linux 10 x64` are listed separately. That
    # was an assumption about somebody else's naming when this was
    # written, which is why the bootstrap records /etc/os-release into
    # the facts file rather than trusting the label. The first run
    # settled it: this image reports itself as "AlmaLinux 8.10 (Cerulean
    # Leopard)". The check stays, because the catalogue entry is still
    # unversioned and can be repointed without the name changing.
    alma8 = {
      os_name  = "AlmaLinux x64"
      family   = "rhel"
      packages = "at quota lvm2 mdadm nftables iptables policycoreutils policycoreutils-python-utils selinux-policy-targeted firewalld rsync tar git curl e2fsprogs util-linux"
      closes   = "yum-era RHEL 8, dnf provider on the older line; SPEC 27.1 tier 1"
    }

    alpine = {
      os_name = "Alpine Linux x64"
      family  = "alpine"
      # musl and OpenRC. `quota-tools` rather than `quota`, which is one
      # of the reasons the bootstrap installs package by package and
      # records what it could not find.
      packages = "at quota-tools lvm2 mdadm iptables nftables rsync tar git curl e2fsprogs util-linux bash"
      closes   = "apk provider (never driven), the non-systemd service path; SPEC 27.1 tier 2"
    }

    opensuse16 = {
      os_name = "openSUSE Leap 16 x64"
      family  = "suse"
      # SPEC 27.1 names SUSE 15; Vultr carries Leap 16, which is the
      # current one. The zypper provider does not exist yet, so this row
      # is here to be written against rather than to verify anything.
      packages = "at quota lvm2 mdadm iptables nftables apparmor-utils rsync tar git curl e2fsprogs util-linux"
      closes   = "zypper (not yet implemented); SPEC 27.1 tier 2"
    }

    debian13 = {
      os_name  = "Debian 13 x64 (trixie)"
      family   = "debian"
      packages = "at quota lvm2 mdadm iptables nftables apparmor-utils rsync tar git curl e2fsprogs util-linux"
      closes   = "Debian 13; SPEC 27.1 tier 1"
    }

    ubuntu2204 = {
      os_name  = "Ubuntu 22.04 LTS x64"
      family   = "debian"
      packages = "at quota lvm2 mdadm iptables nftables apparmor-utils rsync tar git curl e2fsprogs util-linux"
      closes   = "Ubuntu 22.04, the oldest tier 1 Ubuntu; SPEC 27.1 tier 1"
    }

    ubuntu2604 = {
      os_name  = "Ubuntu 26.04 LTS x64"
      family   = "debian"
      packages = "at quota lvm2 mdadm iptables nftables apparmor-utils rsync tar git curl e2fsprogs util-linux"
      closes   = "Ubuntu 26.04, the newest tier 1 Ubuntu; SPEC 27.1 tier 1"
    }
  }

  # An empty `distros` means all of them; naming a subset builds only
  # those rows.
  selected = length(var.distros) == 0 ? local.distro_catalog : {
    for name in var.distros : name => local.distro_catalog[name]
  }
}

# One lookup per selected row. The filter matches the catalogue name
# exactly, so a rename upstream is a planning error naming the string
# that no longer resolves.
data "vultr_os" "distro" {
  for_each = local.selected

  filter {
    name   = "name"
    values = [each.value.os_name]
  }
}
