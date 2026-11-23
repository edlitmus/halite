# Every node's own configuration, from pillar (halite:node:config).
#
# A node does not restart itself in the middle of the job that changed its
# configuration: that job would die with it. A changed node.yaml schedules
# a restart a little later, from outside the agent, and the run that wrote
# it finishes first. The restart command is per platform: daemon(8) on
# FreeBSD, systemd-run where systemd is running, and setsid with the init
# script elsewhere (OpenRC, sysvinit). The first two were run inside a real
# job on Ubuntu 26.04 and FreeBSD 15.1 (DIVERGENCE 5.482), and the third on
# Alpine and a sysvinit Debian (5.708), none of them from this file; under
# systemd read the 30 as 30 to 90 seconds.
#
# This shape is not a preference. A `watch` on the agent's own service --
# the obvious way to write it -- is refused by halite, because it reports a
# restart that has not happened and leaves the node down (5.663, 5.663.1).
# So `halite node agent` below stays watchless, which is also what gets an
# agent back that failed to come up after the detached restart.
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
{%- elif grains['init'] == 'systemd' %}
    # A transient unit of its own, outside halite-node's cgroup, which
    # systemd stops with the service.
    - name: systemd-run --on-active=30 --unit=halite-node-restart systemctl restart {{ halite.service.node }}
{%- else %}
    # OpenRC or sysvinit, which have no systemd-run: the `init` grain is
    # `systemd` only when systemd is running. setsid gives the restart a
    # session of its own, outside the agent's process group, and with its
    # output closed and `&` the job returns at once instead of waiting on
    # it. Run in the lab on Alpine and on a sysvinit Debian (DIVERGENCE
    # 5.708), though not from this file.
    - name: "setsid sh -c 'sleep 30; /etc/init.d/{{ halite.service.node }} restart' </dev/null >/dev/null 2>&1 &"
{%- endif %}
    - onchanges:
      - file: halite node configuration

halite node agent:
  service.running:
    - name: {{ halite.service.node }}
    - enable: true
