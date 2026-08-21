{% if grains['kernel'] == 'FreeBSD' %}
on_freebsd:
  cmd.run:
    - name: echo bsd
{% else %}
elsewhere:
  cmd.run:
    - name: echo other
{% endif %}

report:
  cmd.run:
    - name: echo {{ grains['kernel'] }} {{ grains['os_family'] }}
