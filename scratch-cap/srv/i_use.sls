a:
  test.configurable_test_state:
    - changes: False
    - result: True
    - comment: from a
    - use_in:
      - sls: u1
include:
  - u1
  - other
