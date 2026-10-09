# The signer: the one machine that holds the extension-signing key.
#
# Not the hub. A hub holding the key would verify its own signatures,
# and whoever took the hub could sign anything every node then runs
# (docs/extensions.md).
halite:
  node:
    config:
      schedule:
        # Signs whatever has arrived since the last run. A version is
        # signed once: the state does nothing for one already published.
        sign_extensions:
          function: state.apply
          args: [halite.signer]
          cron: '23 * * * *'
          maxrunning: 1
  signer:
    key_name: release
    # A checkout of the repository the hub serves its tree from, through
    # gitfs; signed bundles are published into it and pushed.
    checkout: /srv/halite-tree
    # Where built executables arrive -- from CI, or a build step of your
    # own -- one directory per extension and version.
    incoming: /srv/halite-incoming
    # Each is signed once its executable is in <incoming>/<name>/<version>/;
    # until then the state that signs it does nothing.
    extensions:
      - name: hello
        version: 1.0.0
        kind: module
        exe: hello
        declares: network
