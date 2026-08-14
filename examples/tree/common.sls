# `names:` declares this state once per name — one package per state, so
# the run output says which one did what, and a requisite naming
# base_tools reaches all of them.
base_tools:
  pkg.installed:
    - names:
      - tmux
      - curl

/etc/motd:
  file.managed:
    - contents: "{{ .Grains.host }} - managed by halite"
    - mode: "0644"
    - require:
      - pkg: base_tools
