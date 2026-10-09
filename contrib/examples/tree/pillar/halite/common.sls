# What every node shares: where the hub is, which extension signatures
# it trusts and which bundles it runs, and the schedule every node runs.
#
# Each node writes this into its node.yaml (states/halite/node.sls), so a
# change here reaches a node at its next highstate and takes effect when
# the node restarts.
halite:
  node:
    config:
      hub: hub.example.com
      hub_port: 4510
      log_level: info
      log_format: json

      # The trust line `halite-hub extensions key create release` printed
      # on the signer, and the pin `extensions sign` printed for each
      # bundle. Both halves of a pin matter: the version is a label the
      # publisher chose; the root is not. See docs/extensions.md.
      extension_require_signature: true
      extension_trust_keys:
        - 'release AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
      extension_pins: {}
      #  hello:
      #    version: 1.0.0
      #    root: <the root `extensions sign` printed>

      # Scheduled jobs run inside `halite-node connect`; there is no
      # hub-side scheduler. A schedule that does not parse stops the
      # agent from starting, so change it with care.
      schedule:
        nightly_highstate:
          function: state.apply
          cron: '17 3 * * *'
          splay: 900
          maxrunning: 1
