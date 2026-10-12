webserver:
  port: 8080
  sites:
    www:
      server_name: www.example.com
    api:
      server_name: api.example.com
      root: /srv/api/public
