a:
  test.succeed_without_changes:
    - prereq_in:
      - sls: g1
include:
  - g1
  - other
