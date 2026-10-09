# The hub host: the hub's and the API's configuration and policy, and
# what the hub host's own node schedules. Only the hub host sees this.
halite:
  # The hub host runs a node of its own, enrolled to the hub on the same
  # machine. It must not share the hub's pki_dir or state_dir: the hub
  # keeps its CA and its extension cache there, and a node that finds
  # the hub's ca.crt treats it as its pinned CA.
  node:
    # Gives this node pki_dir <config root>/node-pki and its own state
    # directory; states/halite/node.sls sets both.
    separate_dirs: true
    config:
      hub: localhost
      schedule:
        # The certificates nothing renews by itself: the API's serving
        # certificate and its operator certificate to the hub. The hub's
        # own certificate and every node's renew themselves.
        renew_certificates:
          function: state.apply
          args: [halite.certs]
          cron: '41 4 * * *'
          splay: 600
          maxrunning: 1

  hub:
    config:
      listen: ':4510'
      certificate_lifetime: 2160h
      enrollment_mode: manual
      file_roots:
        base: [/srv/halite/states]
      pillar_roots:
        base: [/srv/halite/pillar]
      extension_require_signature: true
      extension_trust_keys:
        - 'release AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='
      extension_pins: {}
      log_level: info
      log_format: json

    # policy.yaml. The hub reads it once at startup, so the state that
    # writes it restarts the hub.
    policy:
      roles:
        admin:
          - target: '*'
            functions: ['*']
          - runners: ['*']
        api-service:
          - runners: ['metrics.show']
      bindings:
        - principal: 'cert:CN=ed'
          roles: [admin]
        - principal: 'cert:CN=api'
          roles: [api-service]

  api:
    config:
      listen: ':4511'
      hub: localhost
      api_operator: api
      log_level: info
      log_format: json
    # The API's serving certificate is its own, not the enrollment CA's
    # (docs/operations.md, "The API's serving certificate"). These are
    # the names clients reach it by.
    serving_names:
      - api.example.com
      - localhost
    # How long the API's operator certificate to the hub lasts, and how
    # close to expiry halite.certs replaces it.
    operator_lifetime: 720h
    operator_renew_seconds: 604800   # seven days
