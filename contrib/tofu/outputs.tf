# What a run hands back, shaped for the scripts that consume it.

# One line per instance: name, address, family, and what the row is for.
# `make lab-test` reads this rather than talking to the API itself, so
# there is one place that knows what exists.
output "hosts" {
  description = "The instances this run raised, keyed by distro name."
  value = {
    for name, inst in vultr_instance.node : name => {
      ipv4   = inst.main_ip
      label  = inst.label
      family = local.selected[name].family
      closes = local.selected[name].closes
      os     = local.selected[name].os_name
    }
  }
}

# The same thing as `name<TAB>address` lines, which is what a shell loop
# wants and what avoids a JSON parser in the Makefile.
output "ssh_targets" {
  description = "Tab-separated distro and address, one per line, for `make lab-test`."
  value = join("\n", [
    for name, inst in vultr_instance.node : "${name}\t${inst.main_ip}"
  ])
}

# Roughly what leaving this running costs, so the number is in front of
# whoever forgot to run `make lab-down`. Vultr bills hourly against the
# monthly cap, and these are the monthly caps.
output "monthly_cost_if_left_running" {
  description = "USD per month if these instances are never destroyed."
  value       = "${length(vultr_instance.node)} instance(s) on ${var.plan}; see `vultr-cli plans list` for the per-plan cap"
}
