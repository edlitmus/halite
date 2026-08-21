{% set services = ['alpha', 'beta', 'gamma'] %}
{% for svc in services %}
/tmp/halite-diff-{{ svc }}:
  file.managed:
    - contents: |
        service {{ svc }}
        index {{ loop.index }}
    - mode: '0640'
{% endfor %}

{% if pillar.get('extra', False) %}
extra_state:
  cmd.run:
    - name: echo extra
{% endif %}

from_pillar:
  cmd.run:
    - name: echo {{ pillar.get('greeting', 'default') }}
