# The signer: it holds the extension-signing key, signs every bundle
# pillar lists (halite:signer:extensions), publishes it into a checkout of
# the repository the hub serves through gitfs, and pushes.
#
# It is not the hub. A hub holding the key would verify its own
# signatures, and whoever took it could sign anything every node runs.
{%- from "halite/map.jinja" import halite with context %}
{%- set signer = pillar['halite']['signer'] %}
{%- set keydir = halite.conf ~ '/signing' %}
{%- set key = keydir ~ '/extension-' ~ signer['key_name'] ~ '.key' %}

halite signing key directory:
  file.directory:
    - name: {{ keydir }}
    - mode: '0700'

# Made once. `key create` refuses to replace a key, and `creates` keeps
# this state from asking it to. It prints the extension_trust_keys line
# for pillar's halite.common, in this state's output the first time.
halite signing key:
  cmd.run:
    - name: halite-hub extensions key create {{ signer['key_name'] }} --out {{ key }}
    - creates: {{ key }}
    - require:
      - file: halite signing key directory

{%- for ext in signer['extensions'] %}
{%-   set incoming = signer['incoming'] ~ '/' ~ ext['name'] ~ '/' ~ ext['version'] %}
{%-   set published = signer['checkout'] ~ '/_ext/' ~ ext['name'] ~ '/' ~ ext['version'] %}

# A version is signed and published once; `extensions sign --publish`
# refuses one that is already in the tree. It prints the extension_pins
# entry for pillar.
halite sign {{ ext['name'] }} {{ ext['version'] }}:
  cmd.run:
    - name: >-
        halite-hub extensions sign {{ incoming }}
        --name {{ ext['name'] }} --ext-version {{ ext['version'] }}
        --kind {{ ext['kind'] }} --exe {{ ext['exe'] }}
        {%- if ext.get('declares') %} --declares {{ ext['declares'] }}{% endif %}
        --key {{ key }} --publish {{ signer['checkout'] }}
    - creates: {{ published }}/manifest.sig
    - onlyif: test -f {{ incoming }}/{{ ext['exe'] }}
    - require:
      - cmd: halite signing key
    - require_in:
      - cmd: halite publish signed extensions
{%- endfor %}

# Getting the tree to the hub is this estate's step, not halite's: halite
# moves files from the hub to nodes and not back. Here it is a git push,
# with the hub serving the repository through gitfs; the credentials it
# needs are yours to arrange.
halite publish signed extensions:
  cmd.run:
    - name: >-
        git -C {{ signer['checkout'] }} add _ext &&
        git -C {{ signer['checkout'] }} commit -m 'Publish signed extensions' &&
        git -C {{ signer['checkout'] }} push
    - onlyif: test -n "$(git -C {{ signer['checkout'] }} status --porcelain _ext)"
