# Pillar top file. Same targeting as a state top file. This tree sits
# beside examples/tree, so `halite apply -root examples/tree` finds it
# without a -pillar-root flag.
base:
  '*':
    - common
  'web*':
    - web
