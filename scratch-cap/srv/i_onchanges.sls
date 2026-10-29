a:
  test.succeed_with_changes:
    - onchanges_in:
      - sls: g1
include:
  - g1
  - other
