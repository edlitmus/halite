a:
  test.succeed_with_changes:
    - onchanges_in:
      - sls: 'g*'
include:
  - g1
  - g2
  - other
