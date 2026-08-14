# An internal CA and a certificate signed by it, from the standard
# library — no openssl, no external tool. The whole thing converges: each
# run checks the expiry and reissues inside the renewal window.
#
#   halite apply examples/tls.sls -test

/usr/local/etc/ssl/ca.key:
  x509.private_key_managed:
    - algo: ec
    - mode: "0600"

/usr/local/etc/ssl/ca.crt:
  x509.certificate_managed:
    - private_key: /usr/local/etc/ssl/ca.key
    - CN: internal ca
    - O: example
    - ca: true
    - days_valid: 3650
    - require:
      - x509: /usr/local/etc/ssl/ca.key

/usr/local/etc/ssl/site.key:
  x509.private_key_managed:
    - algo: ec

/usr/local/etc/ssl/site.crt:
  x509.certificate_managed:
    - private_key: /usr/local/etc/ssl/site.key
    - CN: {{ .Grains.host }}
    - subject_alt_names:
      - DNS:{{ .Grains.host }}
      - DNS:site.example.com
    - signing_private_key: /usr/local/etc/ssl/ca.key
    - signing_cert: /usr/local/etc/ssl/ca.crt
    - days_valid: 90
    - days_remaining: 30
    - require:
      - x509: /usr/local/etc/ssl/site.key
      - x509: /usr/local/etc/ssl/ca.crt
    - watch_in:
      - service: nginx

nginx:
  service.running:
    - enable: true
