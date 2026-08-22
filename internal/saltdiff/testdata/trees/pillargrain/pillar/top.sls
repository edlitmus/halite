base:
  '*':
    - common
  'kernel:FreeBSD':
    - match: grain
    - bsd
  'kernel:NoSuchKernel':
    - match: grain
    - never
    - ignore_missing: True
