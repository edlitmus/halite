# `watch_in` writes the edge from this end: nginx is declared in
# web/init.sls, and this file makes it restart when the certificate
# changes without init.sls having to know that TLS exists.
#
# That is what the `_in` forms are for — reaching a state another file
# declares.
/usr/local/etc/ssl/site.pem:
  file.managed:
    - source: files/site.pem
    - mode: "0640"
    - makedirs: true
    - watch_in:
      - service: nginx
