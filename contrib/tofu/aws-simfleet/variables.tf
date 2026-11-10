# Where the run goes. The four network and identity inputs have no
# defaults on purpose: they name one account's resources, and a default
# would raise instances into whichever account the caller's credentials
# happen to reach. Put the values for your account in a local
# `terraform.tfvars`, which the repository ignores.

variable "region" {
  description = "The AWS region. The plan's checks were all made in us-east-1."
  type        = string
  default     = "us-east-1"
}

variable "subnet_id" {
  description = "A private subnet with NAT egress, in one availability zone. Every instance goes into it, so traffic between them stays in one AZ and is not billed."
  type        = string
}

variable "security_group_id" {
  description = "An existing security group that opens the hub's port between the instances. This module creates no security group and changes none."
  type        = string
}

variable "instance_profile" {
  description = "An existing instance profile with AmazonSSMManagedInstanceCore, so the instances are reached through SSM. This module creates no IAM."
  type        = string
}

variable "owner" {
  description = "Who raised the run, for the Owner tag."
  type        = string
}

variable "environment" {
  description = "The Environment tag."
  type        = string
  default     = "simfleet"
}

variable "hub_type" {
  description = "The hub's instance type. SPEC 30 names one 16-core hub; a Graviton vCPU is a core, so c7g.4xlarge is 16 cores."
  type        = string
  default     = "c7g.4xlarge"
}

variable "driver_type" {
  description = "Each driver's instance type. Step 0 measured about 200 KiB of driver memory per simulated node, so c7g.xlarge (8 GiB) holds the plan's 5,000 per driver with room."
  type        = string
  default     = "c7g.xlarge"
}

variable "driver_count" {
  description = "How many drivers, at 5,000 simulated nodes each: 1 for step 1, 2 for step 2, 5 for step 3 (four and a spare)."
  type        = number
  default     = 1

  validation {
    condition     = var.driver_count >= 0 && var.driver_count <= 12
    error_message = "driver_count is between 0 and 12. The plan's largest step needs 10; more than 12 is a typo, not a run."
  }
}

variable "placement_group" {
  description = "Put every instance in one cluster placement group, so the network between them is not the variable. Optional at 10,000 nodes or fewer."
  type        = bool
  default     = false
}

variable "ttl_minutes" {
  description = "Each instance halts itself this many minutes after boot, and halting terminates it. A dead man's switch for a forgotten run, not a replacement for `tofu destroy`."
  type        = number
  default     = 240

  validation {
    condition     = var.ttl_minutes >= 30 && var.ttl_minutes <= 480
    error_message = "ttl_minutes is between 30 and 480. A run that needs more than eight hours should be raised again, deliberately."
  }
}

variable "hub_volume_gb" {
  description = "The hub's gp3 root volume. The job cache and the key store live on it."
  type        = number
  default     = 50
}

variable "driver_volume_gb" {
  description = "Each driver's gp3 root volume."
  type        = number
  default     = 20
}

variable "ami_ssm_parameter" {
  description = "The public SSM parameter that names the AMI. Canonical's stock Ubuntu 24.04 arm64, rather than the estate's own image, which runs a configuration agent that could enrol a throwaway instance into the estate."
  type        = string
  default     = "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"
}
