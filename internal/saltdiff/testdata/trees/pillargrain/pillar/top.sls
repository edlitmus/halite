base:
  '*':
    - common
  # A grain glob rather than a literal kernel name.
  #
  # This said `kernel:FreeBSD`, which is the development host and no
  # other. Anywhere else the branch did not match, `host_specific` was
  # undefined, and both implementations failed to render `uses.sls` — so
  # the case that exists to compare grain-targeted pillar compared two
  # rendering errors, and could not even be decoded. A glob matches
  # whatever kernel the gate is running on, so the branch is exercised
  # on every host rather than on one.
  'kernel:*':
    - match: grain
    - bsd
  'kernel:NoSuchKernel':
    - match: grain
    - never
    - ignore_missing: True
