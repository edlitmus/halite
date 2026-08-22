from_targeted_pillar:
  cmd.run:
    - name: echo {{ pillar['host_specific']['note'] }} {{ pillar['everywhere'] }}
