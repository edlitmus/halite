{% macro managed_file(path, body) %}
{{ path }}:
  file.managed:
    - contents: {{ body | yaml_encode }}
    - mode: '0600'
{% endmacro %}

{{ managed_file('/tmp/halite-diff-macro-1', 'alpha') }}
{{ managed_file('/tmp/halite-diff-macro-2', 'beta') }}

filters:
  cmd.run:
    - name: echo {{ ['c', 'a', 'b'] | sort | join('-') }} {{ 'Text' | lower }} {{ 5 | int + 2 }}
