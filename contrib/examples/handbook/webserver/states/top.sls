base:
  '*':
    - common
  'web*.example.com':
    - webserver
  'G@os_family:FreeBSD and web*':
    - match: compound
    - webserver.bsd_tuning
