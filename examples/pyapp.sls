# Deploying a Python application: its own environment, its requirements,
# a symlink for the current release, and a service that watches all of it.
#
#   halite apply examples/pyapp.sls -test

app_group:
  group.present:
    - name: app

app_user:
  user.present:
    - name: app
    - home: /opt/app
    - system: true
    - groups:
      - app
    - require:
      - group: app_group

/opt/app/releases/2026.08.1:
  file.recurse:
    - source: files/app
    - file_mode: "0644"
    - dir_mode: "0755"
    - user: app
    - require:
      - user: app_user

/opt/app/venv:
  virtualenv.managed:
    - requirements: /opt/app/releases/2026.08.1/requirements.txt
    - require:
      - file: /opt/app/releases/2026.08.1

# The venv's own pip, so the application's dependencies stay out of the
# system's site-packages.
gunicorn:
  pip.installed:
    - bin_env: /opt/app/venv
    - name: gunicorn==23.0.0
    - require:
      - virtualenv: /opt/app/venv

/opt/app/current:
  file.symlink:
    - target: /opt/app/releases/2026.08.1
    - require:
      - file: /opt/app/releases/2026.08.1
    - watch_in:
      - service: app

app:
  service.running:
    - enable: true
