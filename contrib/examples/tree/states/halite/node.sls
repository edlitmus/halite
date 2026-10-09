# Every node's own configuration, from pillar (halite:node:config).
#
# A node does not restart itself in the middle of the job that changed its
# configuration: that job would die with it. A changed node.yaml schedules
# a restart a little later, from outside the agent, and the run that wrote
# it finishes first. The restart command is per platform and has not been
# run by this project; read it before you rely on it.
{%- from "halite/map.jinja" import halite with context %}
{%- set node = pillar['halite']['node'] %}
{%- set cfg = node['config'] %}
{%- if node.get('separate_dirs') %}
{%-   do cfg.update({
        'pki_dir': halite.conf ~ '/node-pki',
        'state_dir': halite.state ~ '/halite-node',
      }) %}
{%- endif %}

halite node configuration:
  file.serialize:
    - name: {{ halite.conf }}/node.yaml
    - dataset: {{ cfg | json }}
    - serializer: yaml
    - mode: '0640'
    - group: {{ halite.group }}

halite node restart when its configuration changes:
  cmd.run:
{%- if grains['kernel'] == 'FreeBSD' %}
    # daemon(8) detaches it from the agent, which the restart stops.
    - name: daemon -f /bin/sh -c 'sleep 30; service {{ halite.service.node }} restart'
{%- else %}
    # A transient unit of its own, outside halite-node's cgroup, which
    # systemd stops with the service.
    - name: systemd-run --on-active=30 --unit=halite-node-restart systemctl restart {{ halite.service.node }}
{%- endif %}
    - onchanges:
      - file: halite node configuration

halite node agent:
  service.running:
    - name: {{ halite.service.node }}
    - enable: true
