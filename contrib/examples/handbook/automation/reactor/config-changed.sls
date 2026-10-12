# Reaction to a watched file changing on a node.
#
# The context is `data`, `tag` and `id`. Nothing else: a reaction has no
# `salt` and no pillar. `data` is the body of the event, plus `id` (the node
# it concerns) and the envelope keys `_tag`, `_stamp` and `_correlation`.
# A filechanges event's body carries `path`, `hash`, `size` and `mode`.
{% set node = data['id'] %}

record_{{ node }}:
  local.cmd.run:
    - tgt: {{ node }}
    - arg:
      - echo "{{ data['path'] }} changed on {{ node }}" >> /tmp/halite-reactions.log

announce_{{ node }}:
  runner.event.send:
    - args:
        tag: halite/audit/config_changed
        data:
          node: {{ node }}
          path: {{ data['path'] }}
