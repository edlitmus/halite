# Pillar top file. Same targeting as a state top file. This tree sits
# beside examples/tree, so `halite apply -root examples/tree` finds it
# without a -pillar-root flag.
#
# The targets here are id globs on purpose: under a control plane the id
# comes from the agent's certificate, while every other grain is what the
# host says about itself. See docs/pillar-security.md.
base:
  '*':
    - common
  'web*':
    - web
