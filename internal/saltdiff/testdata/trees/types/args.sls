typed_arguments:
  cmd.run:
    - name: echo typed
    - cwd: /tmp
    - timeout: 30
    - env:
        LANG: C
        COUNT: 3
        FLAG: true

a_file:
  file.managed:
    - name: /tmp/halite-diff-types
    - mode: '0644'
    - makedirs: yes
    - replace: no
    - user: 0
    - contents:
      - first line
      - second line

quoting_matters:
  cmd.run:
    - name: echo quoting
    - env:
        OCTAL_QUOTED: '0644'
        VERSION: '1.0'
        WORD: 'on'
