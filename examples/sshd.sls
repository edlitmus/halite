# Hardening a config file that the OS package also owns: file.managed
# would fight the package on every upgrade, so each setting is edited in
# place and the service watches the file.
#
#   halite apply examples/sshd.sls -test

sshd_config_permitrootlogin:
  file.replace:
    - name: /etc/ssh/sshd_config
    - pattern: '^#?PermitRootLogin .*'
    - repl: 'PermitRootLogin no'
    - append_if_not_found: true
    - watch_in:
      - service: sshd

sshd_config_passwords:
  file.replace:
    - name: /etc/ssh/sshd_config
    - pattern: '^#?PasswordAuthentication .*'
    - repl: 'PasswordAuthentication no'
    - append_if_not_found: true
    - watch_in:
      - service: sshd

# file.line matches on a substring; file.replace is the regex one.
sshd_config_x11:
  file.line:
    - name: /etc/ssh/sshd_config
    - content: "X11Forwarding no"
    - match: "X11Forwarding"
    - watch_in:
      - service: sshd

# One block this state owns, in a file other things also write to.
sshd_config_ciphers:
  file.blockreplace:
    - name: /etc/ssh/sshd_config
    - marker_start: '# BEGIN halite ciphers'
    - marker_end: '# END halite ciphers'
    - content: "KexAlgorithms curve25519-sha256"
    - append_if_not_found: true
    - watch_in:
      - service: sshd

# Turning a setting off by commenting it, rather than writing its
# opposite: the package's own default then applies again.
sshd_config_no_gssapi:
  file.comment:
    - name: /etc/ssh/sshd_config
    - regex: '^GSSAPIAuthentication'
    - watch_in:
      - service: sshd

ed_key:
  ssh_auth.present:
    - user: root
    - enc: ssh-ed25519
    - name: AAAAC3NzaC1lZDI1NTE5AAAAIB6mFbT4tGvJv7nFqz0v0N0i0wKmrGV0i2Yh3example
    - options:
      - no-agent-forwarding

sshd:
  service.running:
    - enable: true
