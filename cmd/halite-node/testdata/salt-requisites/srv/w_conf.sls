a:
  test.succeed_with_changes
c:
  test.succeed_without_changes
b:
  test.configurable_test_state:
    - changes: False
    - watch:
      - test: a
