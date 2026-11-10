# What a run script needs to address the hosts by role, without the
# console: instance IDs for SSM, private addresses for the hub's
# --listen and the drivers' -hub.

output "hub" {
  value = {
    id         = aws_instance.hub.id
    private_ip = aws_instance.hub.private_ip
    type       = aws_instance.hub.instance_type
  }
}

output "drivers" {
  value = [for d in aws_instance.driver : {
    id         = d.id
    private_ip = d.private_ip
    type       = d.instance_type
  }]
}

output "availability_zone" {
  value = data.aws_subnet.run.availability_zone
}

output "expires_at" {
  description = "When the dead man's switch terminates every instance, if nobody has run `tofu destroy` first."
  value       = local.expires_at
}

output "ami" {
  value     = data.aws_ssm_parameter.ami.insecure_value
  sensitive = false
}
