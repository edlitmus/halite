# When a node connects, rehearse its highstate. Swap `--test` for the real
# thing only after you have watched this run for a while.
{% set node = data['id'] %}

check_{{ node }}:
  local.state.apply:
    - tgt: {{ node }}
    - kwarg:
        test: True
