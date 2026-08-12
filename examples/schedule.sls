# Work this agent runs on its own clock, so the fleet converges without
# anything poking it. Each key names a job; the name is also the event tag
# it reports under (halite/schedule/<agent>/<name>).
#
#   halite agent -master master.example.com -schedule schedule.sls
#
# splay delays each run by a random amount up to the given duration. The
# interval stays what it says — the splay spreads a fleet's runs inside it,
# so two hundred hosts do not pull the state tree in the same second.

converge:
  kind: highstate
  interval: 30m
  splay: 5m
  at_start: true

# A dry run is a drift report: it changes nothing and says what would have
# changed, on the event bus, where a reactor rule can pick it up.
nightly-audit:
  kind: highstate
  interval: 24h
  test: true

tls-renewal:
  kind: apply
  sls:
    - web.tls
  interval: 12h
  splay: 30m

disk:
  kind: call
  fn: disk.usage
  interval: 5m
