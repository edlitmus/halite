# Facts this host publishes for the rest of the fleet to use in their
# states, read as {{ .Mine.<function>.<agent> }}.
#
#   halite agent -master master.example.com -mine mine.sls
#
# Publishable: grains, and the read-only execution modules
# (disk.usage, status.uptime, status.loadavg, network.interfaces).

grains:
  interval: 5m

network.interfaces:
  interval: 60s

disk.usage:
