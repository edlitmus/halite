a:
  test.succeed_with_changes:
    - onfail_in:
      - sls: 'g*'
include:
  - g1
  - g2
  - other
