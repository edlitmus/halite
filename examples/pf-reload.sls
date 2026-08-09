# FreeBSD: manage pf.conf, reload only when it changes (cmd.wait + watch).
pf_conf:
  file.managed:
    - name: /etc/pf.conf
    - source: files/pf.conf
    - mode: "0600"

reload_pf:
  cmd.wait:
    - name: pfctl -f /etc/pf.conf
    - watch:
      - file: pf_conf
