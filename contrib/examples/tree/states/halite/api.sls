# halite-api's configuration, from pillar (halite:api), on the hub host.
# A change restarts it: like the hub, it has no reload. Its serving
# certificate is re-read on every handshake and needs no restart; see
# halite/certs.sls.
{%- from "halite/map.jinja" import halite with context %}
{%- set api = pillar['halite']['api'] %}
{%- set cfg = api['config'] %}
{%- do cfg.update({
      'tls_cert': halite.conf ~ '/pki/api.crt',
      'tls_key': halite.conf ~ '/pki/api.key',
    }) %}

halite api configuration:
  file.serialize:
    - name: {{ halite.conf }}/api.yaml
    - dataset: {{ cfg | json }}
    - serializer: yaml
    - user: halite
    - group: {{ halite.group }}
    - mode: '0640'

halite api service:
  service.running:
    - name: {{ halite.service.api }}
    - enable: true
    - watch:
      - file: halite api configuration
