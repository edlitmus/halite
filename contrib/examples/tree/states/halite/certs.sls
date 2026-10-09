# The certificates on the hub host that nothing renews by itself, run
# daily by the hub host's node (pillar: halite:node:config:schedule).
#
# Not here, because they renew themselves at half their life: the hub's
# own certificate (DIVERGENCE 5.259) and every node's (SPEC 7.4).
{%- from "halite/map.jinja" import halite with context %}
{%- set api = pillar['halite']['api'] %}
{%- set pki = halite.conf ~ '/pki' %}

include:
  - halite.api

# A CA for halite-api's serving certificate and nothing else, made once
# and kept for ten years. Clients -- Prometheus, operators' curl -- trust
# this rather than the serving certificate itself, so a renewal below
# changes nothing they hold. A self-signed serving certificate, which is
# the quickest route in docs/operations.md, is a new certificate to trust
# every time it is renewed.
halite api ca key:
  x509.private_key_managed:
    - name: {{ pki }}/api-ca.key
    - algo: ec
    - keysize: 256
    - user: halite
    - mode: '0600'

halite api ca:
  x509.certificate_managed:
    - name: {{ pki }}/api-ca.crt
    - signing_private_key: {{ pki }}/api-ca.key
    - CN: halite api CA
    - ca: true
    - days_valid: 3650
    - days_remaining: 0
    - user: halite
    - mode: '0644'
    - require:
      - x509: halite api ca key

# The serving certificate, renewed 30 days before it expires. halite-api
# reads the files again on its next handshake, so no restart.
halite api serving key:
  x509.private_key_managed:
    - name: {{ pki }}/api.key
    - algo: ec
    - keysize: 256
    - user: halite
    - mode: '0600'

halite api serving certificate:
  x509.certificate_managed:
    - name: {{ pki }}/api.crt
    - private_key: {{ pki }}/api.key
    - signing_cert: {{ pki }}/api-ca.crt
    - signing_private_key: {{ pki }}/api-ca.key
    - CN: {{ api['serving_names'][0] }}
    - subject_alt_names:
{%- for name in api['serving_names'] %}
        - 'DNS:{{ name }}'
{%- endfor %}
    - ext_key_usage:
        - serverAuth
    - days_valid: 90
    - days_remaining: 30
    - user: halite
    - mode: '0644'
    - require:
      - x509: halite api serving key
      - x509: halite api ca

# The API's own certificate to the hub, which `api_operator` names. It is
# an operator certificate, there is no renew command for one, and halite-api
# loads it once at startup: so it is issued again a week before it
# expires, as the account halite runs as so the files are its own, and
# the API is restarted to present it.
halite api operator certificate:
  cmd.run:
    - name: >-
        halite-hub keys operator create {{ api['config']['api_operator'] }}
        --lifetime {{ api['operator_lifetime'] }}
        --config {{ halite.conf }}/hub.yaml
    - runas: halite
    - unless: >-
        openssl x509 -noout -checkend {{ api['operator_renew_seconds'] }}
        -in {{ pki }}/operator-{{ api['config']['api_operator'] }}.crt
    - watch_in:
      - service: halite api service
