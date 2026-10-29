a:
  test.succeed_without_changes:
    - onchanges_in:
      - sls: g1
include:
  - g1
  - other
