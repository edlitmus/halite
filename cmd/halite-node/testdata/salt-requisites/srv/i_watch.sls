a:
  test.succeed_with_changes:
    - watch_in:
      - sls: g1
include:
  - g1
  - other
