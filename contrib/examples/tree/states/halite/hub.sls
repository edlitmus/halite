# The hub's configuration and policy, from pillar (halite:hub), on the
# hub host. halite-hub reads both once, at startup, and has no reload, so
# a change to either restarts it (docs/operations.md). Nodes reconnect on
# their own; a job in flight carries on, and its returns wait in each
# node's queue until the hub is back.
{%- from "halite/map.jinja" import halite with context %}
{%- set hub = pillar['halite']['hub'] %}

halite hub configuration:
  file.serialize:
    - name: {{ halite.conf }}/hub.yaml
    - dataset: {{ hub['config'] | json }}
    - serializer: yaml
    - user: halite
    - group: {{ halite.group }}
    - mode: '0640'

halite hub policy:
  file.serialize:
    - name: {{ halite.conf }}/policy.yaml
    - dataset: {{ hub['policy'] | json }}
    - serializer: yaml
    - user: halite
    - group: {{ halite.group }}
    - mode: '0640'

halite hub service:
  service.running:
    - name: {{ halite.service.hub }}
    - enable: true
    - watch:
      - file: halite hub configuration
      - file: halite hub policy
