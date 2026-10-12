{% from "common/map.jinja" import common with context %}

common_packages:
  pkg.installed:
    - pkgs: {{ common.packages | tojson }}

{% for name, user in pillar.get('admins', {}).items() %}
admin_{{ name }}:
  user.present:
    - name: {{ name }}
    - shell: {{ user.get('shell', '/bin/sh') }}
    - groups: {{ common.admin_groups | tojson }}
{% endfor %}

motd:
  file.managed:
    - name: /etc/motd
    - mode: '0644'
    - contents: |
        {{ grains['id'] }} -- managed by halite. Changes made here by hand are reverted.
