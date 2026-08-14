# Getting content and schedules onto a host: a checkout, an archive, a
# mount, a directory, and a cron entry.
#
#   halite apply examples/provisioning.sls -test

/srv:
  file.directory:
    - mode: "0755"

/srv/site:
  git.latest:
    - name: https://github.com/example/site.git
    - rev: main
    - target: /srv/site
    - require:
      - file: /srv

/srv/vendor:
  archive.extracted:
    - source: https://example.com/vendor-1.4.tar.gz
    - source_hash: sha256=0000000000000000000000000000000000000000000000000000000000000000
    - if_missing: /srv/vendor/VERSION
    - require:
      - file: /srv

/var/backups:
  mount.mounted:
    - device: 10.0.0.20:/backups
    - fstype: nfs
    - opts: rw,noatime
    - mkmnt: true
    - persist: true

nightly-backup:
  cron.present:
    - name: /usr/local/bin/backup /srv/site
    - hour: "2"
    - minute: "30"
    - identifier: site-backup
    - require:
      - mount: /var/backups
