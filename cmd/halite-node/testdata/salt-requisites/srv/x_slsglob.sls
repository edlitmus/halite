x:
  test.succeed_without_changes:
    - require:
      - sls: "g*"
include:
  - g1
  - g2
