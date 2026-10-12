# Roll a new version of the web tier, a node at a time, and record a
# rollback if a step fails.
#
# Rehearse:  halite-hub orch run rolling-update --test --pillar '{"version": "1.2"}'
# Run:       halite-hub orch run rolling-update --pillar '{"version": "1.2"}'
#
# An orchestration sees the `pillar` the caller passed and nothing else:
# the hub is not a node and has no pillar of its own.
{% set version = pillar['version'] %}

announce_start:
  salt.runner:
    - name: event.send
    - kwarg:
        tag: halite/audit/rollout_started
        data:
          version: "{{ version }}"

rehearse_web:
  salt.state:
    - tgt: 'web*'
    - highstate: True
    - test: True
    - require:
      - salt: announce_start

apply_web:
  salt.state:
    - tgt: 'web*'
    - highstate: True
    - batch: 1
    - batch_safe_limit: 1
    - require:
      - salt: rehearse_web

verify_web:
  salt.function:
    - name: test.ping
    - tgt: 'web*'
    - require:
      - salt: apply_web

roll_back:
  salt.function:
    - name: cmd.run
    - tgt: 'web*'
    - arg:
      - echo "rolling back {{ version }}" >> /tmp/halite-rollback.log
    - onfail:
      - salt: apply_web
      - salt: verify_web

announce_done:
  salt.runner:
    - name: event.send
    - kwarg:
        tag: halite/audit/rollout_finished
        data:
          version: "{{ version }}"
    - require:
      - salt: verify_web
