# Cross-platform nginx: FreeBSD pkg + rc.d, Debian-family apt + systemd.
install_nginx:
  pkg.installed:
    - name: nginx

{{ if eq .Grains.os_family "FreeBSD" }}
nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx.conf
    - mode: "0644"
    - require:
      - pkg: install_nginx
{{ else }}
nginx_conf:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: files/nginx.conf
    - mode: "0644"
    - require:
      - pkg: install_nginx
{{ end }}

nginx:
  service.running:
    - enable: true
    - require:
      - pkg: install_nginx
    - watch:
      - file: nginx_conf
