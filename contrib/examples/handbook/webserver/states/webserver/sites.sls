{% from "webserver/map.jinja" import nginx with context %}

{% for site, cfg in pillar['webserver'].get('sites', {}).items() %}
site_{{ site }}:
  file.managed:
    - name: {{ nginx.conf_dir }}/sites/{{ site }}.conf
    - source: salt://webserver/files/site.conf.jinja
    - template: jinja
    - mode: '0644'
    - context:
        site: {{ site }}
        server_name: {{ cfg['server_name'] }}
        root: {{ cfg.get('root', '/var/www/' ~ site) }}
    - watch_in:
      - service: nginx_service
{% endfor %}
