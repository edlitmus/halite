# Windows states, guarded on a grain so the file is harmless in a
# highstate that also runs on unix hosts.
#
# The translation is unit tested; these calls have not been run on a real
# Windows host — see docs/salt-parity.md.

{{ if eq .Grains.os_family "Windows" }}
7zip:
  pkg.installed:

# HKLM\SOFTWARE\... — a value, its type, and the key it lives under.
disable-fast-startup:
  reg.present:
    - name: 'HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Power'
    - vname: HiberbootEnabled
    - vtype: REG_DWORD
    - vdata: "0"

old-flag:
  reg.absent:
    - name: 'HKLM\SOFTWARE\Example'
    - vname: Deprecated

# cron.* becomes a scheduled task under \halite\ on Windows.
nightly-report:
  cron.present:
    - name: 'C:\ProgramData\halite\report.exe'
    - hour: "3"
    - identifier: nightly-report

wuauserv:
  service.running:
    - enable: true
{{ end }}
