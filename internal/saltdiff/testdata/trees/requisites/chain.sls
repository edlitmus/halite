first:
  cmd.run:
    - name: echo first

second:
  cmd.run:
    - name: echo second
    - require:
      - cmd: first

third:
  cmd.run:
    - name: echo third
    - watch:
      - cmd: second

fourth:
  cmd.run:
    - name: echo fourth
    - onchanges:
      - cmd: third
    - onfail:
      - cmd: first

fifth:
  cmd.run:
    - name: echo fifth
    - require_in:
      - cmd: first
