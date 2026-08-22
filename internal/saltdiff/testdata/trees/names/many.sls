make_dirs:
  file.directory:
    - names:
      - /tmp/halite-diff-a
      - /tmp/halite-diff-b
      - /tmp/halite-diff-c
    - mode: '0755'

ordered_last:
  cmd.run:
    - name: echo last
    - order: last

ordered_first:
  cmd.run:
    - name: echo first
    - order: first

per_name_arguments:
  file.managed:
    - user: root
    - names:
      - /tmp/halite-diff-first:
        - contents: first file
      - /tmp/halite-diff-second:
        - contents: second file
        - mode: '0600'
