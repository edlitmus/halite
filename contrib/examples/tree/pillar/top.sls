# Which pillar each machine sees. The node IDs are this example's; use
# your own. Pillar is compiled on the hub for the node that asks, so a
# value only the hub host may read lives in a file only it is given.
base:
  '*':
    - halite.common
  'hub.example.com':
    - halite.hub
  'signer.example.com':
    - halite.signer
