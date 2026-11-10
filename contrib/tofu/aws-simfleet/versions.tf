# The tool and provider versions this module was written and planned
# against. Pinned for the same reason the Vultr lab beside it pins its
# provider: a module that raises a different machine next month cannot
# be used to compare one run with the next, and comparing runs is what
# it is for.

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.72"
    }
  }
}

# No credentials here, and none as variables. The provider reads them
# from the environment: `AWS_PROFILE=<profile>` after `aws sso login`.
# A key in a variable is a key in every saved plan and in `tofu console`.
provider "aws" {
  region = var.region
}
