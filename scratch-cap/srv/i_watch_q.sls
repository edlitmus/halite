a:
  test.succeed_without_changes:
    - watch_in:
      - sls: g1
include:
  - g1
  - other
