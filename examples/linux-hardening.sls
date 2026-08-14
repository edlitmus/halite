# Linux-only settings, guarded on a grain so the file is harmless
# elsewhere in a highstate.
#
#   halite apply examples/linux-hardening.sls -test

{{ if eq .Grains.kernel "Linux" }}
enforcing:
  selinux.mode:

httpd_can_network_connect:
  selinux.boolean:
    - value: true

# Boot-time only: enable it now, but let the deploy start it when ready.
auditd:
  service.enabled:

# The opposite for something that should stop coming back after a reboot.
rpcbind:
  service.disabled:

# A module that should not load, and should not load next boot either.
firewire_core:
  kmod.absent:
    - persist: true

editor:
  alternatives.install:
    - link: /usr/bin/editor
    - path: /usr/bin/vim
    - priority: 100

editor-choice:
  alternatives.set:
    - name: editor
    - path: /usr/bin/vim
    - require:
      - alternatives: editor

/etc/security/limits.conf:
  file.append:
    - text:
      - "* hard core 0"
{{ end }}
