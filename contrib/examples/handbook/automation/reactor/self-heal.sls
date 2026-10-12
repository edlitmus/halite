# Put a managed file back when somebody edits it by hand.
#
# The beacon on the node saw the file change; this applies the one SLS that
# owns the file, to that node only. Naming the SLS (rather than running the
# whole highstate) keeps the repair small and lets the policy restrict this
# principal to exactly that SLS.
{% set node = data['id'] %}

heal_{{ node }}:
  local.state.apply:
    - tgt: {{ node }}
    - arg:
      - playground
