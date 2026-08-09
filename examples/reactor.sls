# Converge every agent as soon as it connects.
'halite/agent/*/hello':
  - run:
      kind: state.highstate
      target: '{{ .Source }}'

# React to a beacon by restarting what fell over.
'halite/beacon/*/service-down':
  - run:
      kind: call
      target: '{{ .Source }}'
      fn: file.managed
      args:
        name: '/tmp/halite-reacted-{{ .Data.service }}'
        contents: 'restarted by the reactor'
