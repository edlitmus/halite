# A FreeBSD jail: its configuration, and the lifecycle that follows it.
# The filesystem is not halite's — this assumes a base already extracted
# or a ZFS dataset cloned into place.
#
#   halite apply examples/jail.sls -test

{{ if eq .Grains.os_family "FreeBSD" }}
# A jail's filesystem is a dataset, and the dataset is a state too. The
# snapshot is what makes the upgrade below reversible.
zroot/jails/www:
  zfs.filesystem_present:
    - parents: true
    - properties:
        compression: lz4
        mountpoint: /usr/local/jails/www

zroot/jails/www@before-upgrade:
  zfs.snapshot_present:
    - require:
      - zfs: zroot/jails/www

www:
  jail.present:
    - path: /usr/local/jails/www
    - hostname: www.example.com
    - ip4_addr: 10.0.0.10
    - interface: em0
    - boot: true
    - params:
        allow.raw_sockets: true
        devfs_ruleset: "4"
        exec.poststart: logger jail www started
    - require:
      - zfs: zroot/jails/www

# The configuration is what the next start reads, so a jail whose block
# changed has to be restarted to pick it up. That is a watch, exactly as
# it is for a service and its config file.
start-www:
  jail.running:
    - name: www
    - watch:
      - jail: www

# Something that should not come back: stopped, unconfigured, and out of
# jail_list — but its filesystem left where it is.
old-staging:
  jail.absent:
    - boot: false
{{ end }}
