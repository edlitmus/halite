include:
  - common

install_nginx:
  pkg.installed:
    - name: nginx

nginx_conf:
  file.managed:
{{ if eq .Grains.os_family "FreeBSD" }}
    - name: /usr/local/etc/nginx/nginx.conf
{{ else }}
    - name: /etc/nginx/nginx.conf
{{ end }}
    - source: files/nginx.conf.tmpl
    - template: true
    - require:
      - pkg: install_nginx

nginx:
  service.running:
    - enable: true
    - watch:
      - file: nginx_conf
