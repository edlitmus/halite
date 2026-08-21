include:
  - common

extend:
  base_package:
    cmd.run:
      - cwd: /var/tmp

app_state:
  cmd.run:
    - name: echo app
    - require:
      - cmd: base_package
