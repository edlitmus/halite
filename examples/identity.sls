# The settings a host has exactly one of. Everything here is idempotent
# and reports drift, so it doubles as a check that a host still is what
# the tree says it is.
#
#   halite apply examples/identity.sls -test

{{ .Grains.host }}:
  network.system:

America/Los_Angeles:
  timezone.system:

# Names for one address land on one line, and the rest of /etc/hosts —
# comments, localhost, whatever else put entries there — is left alone.
db1:
  host.present:
    - ip: 10.0.0.10
    - names:
      - db1
      - db1.internal

cache1:
  host.present:
    - ip: 10.0.0.11

# Keep what the host had before this tree started managing it.
/etc/resolv.conf.orig:
  file.copy:
    - source: /etc/resolv.conf
    - preserve: true
    - unless: test -f /etc/resolv.conf.orig

{{ if eq .Grains.kernel "Linux" }}
en_US.UTF-8:
  locale.system:

nf_conntrack:
  kmod.present:
    - persist: true
{{ end }}
