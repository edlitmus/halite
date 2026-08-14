# Targets: '*' for everything, a grain match, a glob on the host id, and
# boolean combinations of those. A host with no `role` grain never matches
# 'role:web' — see examples/grains for where that grain comes from.
base:
  '*':
    - common
  'os_family:FreeBSD':
    - freebsd-tuning
  'role:web and not L@web9':
    - web
