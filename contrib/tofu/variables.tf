# What a run of this lab can be told, and what it refuses to be told.

variable "region" {
  description = "Vultr region code for every instance, e.g. ewr, ord, fra. `vultr-cli regions list` has the set."
  type        = string
  default     = "ewr"
}

# 2 GB rather than 1. A `go build ./...` of this tree does not fit
# comfortably in 1 GB, and an instance that OOMs halfway through a
# compile reports a failure that has nothing to do with the distribution
# under test -- which is the one kind of noise a lab like this cannot
# afford, because the whole point is to believe what it says about a
# platform nothing else here covers.
variable "plan" {
  description = "Vultr plan ID for every instance. `vultr-cli plans list` has the set and the prices."
  type        = string
  default     = "vc2-1c-2gb"
}

variable "ssh_public_key_path" {
  description = "Public key installed for root on every instance."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

# There is no default, and that is deliberate.
#
# These are internet-facing machines that accept key-only root SSH and
# exist to be handed a source tree and told to run it as root. A default
# here would eventually be the default somebody ran with. `make lab-up`
# fills it from the operator's current public address; setting it by hand
# is the documented alternative.
variable "allowed_ssh_cidrs" {
  description = "Addresses permitted to reach SSH. Set by `make lab-up`; see contrib/tofu/README.md to set it by hand."
  type        = list(string)

  validation {
    condition     = length(var.allowed_ssh_cidrs) > 0
    error_message = "allowed_ssh_cidrs is empty, which would create instances nothing can reach. `make lab-up` sets it from your current public address."
  }

  validation {
    condition     = !contains(var.allowed_ssh_cidrs, "0.0.0.0/0")
    error_message = "0.0.0.0/0 opens key-only root SSH on every instance to the whole internet. These hosts run a source tree as root; scope this to an address you control."
  }
}

# Which rows of the matrix in distros.tf to build.
#
# The default is every one, because the reason the lab exists is that no
# single machine here covers them. Naming a subset is for iterating on
# one distribution's defect without paying for the other six.
variable "distros" {
  description = "Subset of the distros.tf matrix to create. Empty means all of them."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for d in var.distros : contains(keys(local.distro_catalog), d)])
    error_message = "One or more names are not rows of the matrix in distros.tf. `make lab-distros` lists them."
  }
}

variable "go_version" {
  description = "Go toolchain installed on each instance. Keep this equal to the toolchain line in go.mod."
  type        = string
  default     = "1.26.6"
}

# The checksum of the tarball that version resolves to, so a machine that
# was handed something else stops rather than testing it.
#
# From https://go.dev/dl/?mode=json&include=all for
# go1.26.6.linux-amd64.tar.gz. It has to move whenever go_version does,
# and the bootstrap fails loudly when the two disagree -- an unverified
# toolchain quietly installed is how a lab starts reporting on a
# compiler nobody chose.
variable "go_sha256" {
  description = "SHA-256 of go<go_version>.linux-amd64.tar.gz."
  type        = string
  default     = "708effb774be8237570d0add163225abbdfaf4fca28b2611df167beba4feef89"
}

# The FreeBSD rows download a different tarball, so they check a
# different digest. One variable covering both would have meant the
# FreeBSD rows verifying nothing, which is worse than not verifying
# visibly.
#
# From https://go.dev/dl/?mode=json&include=all for
# go1.26.6.freebsd-amd64.tar.gz.
variable "go_sha256_freebsd" {
  description = "SHA-256 of go<go_version>.freebsd-amd64.tar.gz."
  type        = string
  default     = "9c805b762d9cd33c04c0dd414c1f4e86065a6ddce06e97e194e9bc806b120fc7"
}

variable "label_prefix" {
  description = "Prefix for instance labels and tags, so these are distinguishable from anything else on the account."
  type        = string
  default     = "halite-lab"
}
