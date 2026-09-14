# The tool and provider versions this lab was actually run against.
#
# Pinned rather than floated for the same reason `go.mod` pins a
# toolchain: a lab that provisions a different machine this month than it
# did last month cannot be used to say what a distribution does, which is
# the only thing it exists for.

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    vultr = {
      source  = "vultr/vultr"
      version = "~> 2.23"
    }
  }
}

# The API key is never a variable and never written to a file here. The
# provider reads VULTR_API_KEY from the environment, which is where
# vultr-cli already keeps it, so the token lives in exactly one place and
# no state file, plan file or shell history in this repository can carry
# it.
#
# `make` will load it from $HOME/.config/halite/lab.env when the
# environment has none -- outside the worktree, so the sentence above
# stays true. See LAB_ENV in the Makefile for why not a `.env` here and
# not a `.tfvars`.
provider "vultr" {
  rate_limit  = 700
  retry_limit = 3
}
