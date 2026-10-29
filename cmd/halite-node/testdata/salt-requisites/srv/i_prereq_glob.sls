a:
  test.succeed_with_changes:
    - prereq_in:
      - sls: 'g*'
include:
  - g1
  - g2
  - other
