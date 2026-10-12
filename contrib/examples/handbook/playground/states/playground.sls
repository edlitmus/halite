{% set root = pillar['playground']['dir'] %}

playground_dir:
  file.directory:
    - name: {{ root }}
    - mode: '0755'

playground_motd:
  file.managed:
    - name: {{ root }}/motd
    - mode: '0644'
    - require:
      - file: playground_dir
    - contents: |
        This directory is managed by halite.
        Host:  {{ grains['id'] }}
        OS:    {{ grains['os'] }} {{ grains['osrelease'] }}
        Owner: {{ pillar['playground']['owner'] }}

playground_changelog:
  cmd.run:
    - name: echo "motd changed" >> {{ root }}/changes.log
    - onchanges:
      - file: playground_motd
