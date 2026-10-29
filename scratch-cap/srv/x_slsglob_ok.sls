x:
  test.succeed_without_changes:
    - require:
      - sls: "g1*"
include:
  - g1
  - g2
