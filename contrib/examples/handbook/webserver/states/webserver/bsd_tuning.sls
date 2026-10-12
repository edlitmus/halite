include:
  - webserver.nginx

extend:
  nginx_service:
    service.running:
      - require:
        - sysctl: somaxconn

somaxconn:
  sysctl.present:
    - name: kern.ipc.somaxconn
    - value: '4096'
