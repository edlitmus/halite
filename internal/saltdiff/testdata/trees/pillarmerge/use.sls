uses_pillar:
  cmd.run:
    - name: echo {{ pillar['app']['name'] }} {{ pillar['app']['port'] }}
