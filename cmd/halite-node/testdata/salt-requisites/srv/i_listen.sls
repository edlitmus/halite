a:
  test.succeed_with_changes:
    - listen_in:
      - sls: g1
include:
  - g1
  - other
