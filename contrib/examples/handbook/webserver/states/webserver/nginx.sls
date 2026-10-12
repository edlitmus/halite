{% from "webserver/map.jinja" import nginx with context %}

nginx:
  pkg.installed: []

nginx_conf:
  file.managed:
    - name: {{ nginx.conf_dir }}/nginx.conf
    - source: salt://webserver/files/nginx.conf.jinja
    - template: jinja
    - user: root
    - mode: '0644'
    - context:
        worker_processes: {{ grains['num_cpus'] }}
        run_as: {{ nginx.user }}
        port: {{ pillar['webserver']['port'] }}
    - require:
      - pkg: nginx

nginx_service:
  service.running:
    - name: nginx
    - enable: True
    - require:
      - pkg: nginx
    - watch:
      - file: nginx_conf
