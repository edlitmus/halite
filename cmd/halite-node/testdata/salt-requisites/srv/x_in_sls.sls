a:
  test.fail_without_changes:
    - require_in:
      - sls: g1
include:
  - g1
  - other
