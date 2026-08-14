# A third-party repository and a version held against it. The signing key
# is a file.managed the repository requires: halite does not fetch keys,
# because a state that downloaded and trusted one would be the wrong
# default.
#
#   halite apply examples/repo.sls -test

{{ if eq .Grains.os_family "Debian" }}
/etc/apt/keyrings/nginx.gpg:
  file.managed:
    - source: files/nginx.gpg
    - mode: "0644"
    - makedirs: true

nginx-upstream:
  pkgrepo.managed:
    - url: https://nginx.org/packages/debian
    - dist: bookworm
    - comps: nginx
    - signed_by: /etc/apt/keyrings/nginx.gpg
    - require:
      - file: /etc/apt/keyrings/nginx.gpg
    - require_in:
      - pkg: nginx
{{ end }}

nginx:
  pkg.installed:
    - version: 1.28.0
    - hold: true
