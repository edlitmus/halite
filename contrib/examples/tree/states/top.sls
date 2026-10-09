# Which states each machine applies. Every node manages its own node.yaml;
# the hub host manages the hub and the API as well; the signer signs.
base:
  '*':
    - halite.node
  'hub.example.com':
    - halite.hub
    - halite.api
  'signer.example.com':
    - halite.signer
