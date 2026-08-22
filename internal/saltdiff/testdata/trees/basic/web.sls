/tmp/halite-diff-motd:
  file.managed:
    - contents: hello
    - mode: '0644'
    - user: root

say_hello:
  cmd.run:
    - name: echo hi
    - cwd: /tmp

short_declaration:
  cmd.run
