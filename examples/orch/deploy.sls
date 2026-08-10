# Ordered fleet-wide steps: halite orchestrate deploy
#
# Steps are ordinary SLS states, so `require` between them is the same
# `require` you use between states — it just means "those hosts finished,
# and succeeded".

drain_lb:
  halite.run:
    - target: 'lb*'
    - kind: call
    - fn: cmd.run
    - args:
        name: lb-drain

upgrade_web:
  halite.run:
    - target: 'web*'
    - kind: state.apply
    - sls:
      - web.nginx
    - require:
      - halite: drain_lb

restore_lb:
  halite.run:
    - target: 'lb*'
    - kind: call
    - fn: cmd.run
    - args:
        name: lb-restore
    - require:
      - halite: upgrade_web
