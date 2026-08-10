# Agent-side watchers. Each kind takes a list, so one kind can watch
# several things. Beacons are edge triggered: a condition that stays true
# raises one event, not one per check.
#
#   halite agent -master master.example.com -beacons beacons.sls

disk:
  - mount: /var
    threshold: "90"
    interval: 60s
  - mount: /

service:
  - name: nginx
    interval: 30s

file:
  - path: /usr/local/etc/nginx/nginx.conf
    interval: 10s
