from_targeted_pillar:
  cmd.run:
    - name: echo {{ pillar['host_specific']['note'] }} {{ pillar['everywhere'] }}

via_dispatcher:
  cmd.run:
    - name: echo {{ salt['pillar.get']('everywhere') }} {{ salt['grains.get']('kernel') }} {{ salt['pillar.get']('absent:key', 'fallback') }}

list_traversal:
  cmd.run:
    - name: echo {{ salt['pillar.get']('accounts:ed:shell') }} {{ salt['pillar.get']('accounts:ed:password') }}
