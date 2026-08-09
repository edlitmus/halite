# Baseline for every host: tools, motd, stale-file cleanup.
base_tools:
  pkg.installed:
    - pkgs:
      - tmux
      - curl

motd:
  file.managed:
{{ if eq .Grains.os_family "FreeBSD" }}
    - name: /etc/motd.template
{{ else }}
    - name: /etc/motd
{{ end }}
    - contents: "{{ .Grains.host }} ({{ .Grains.os }} {{ .Grains.osrelease }}) - managed by halite"
    - mode: "0644"

old_bootstrap:
  file.absent:
    - name: /root/bootstrap.sh
