a:
  test.fail_without_changes:
    - onfail_in:
      - sls: g1
include:
  - g1
  - other
