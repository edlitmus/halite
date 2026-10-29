a:
  test.succeed_with_changes
c:
  test.succeed_without_changes
b:
  test.succeed_without_changes:
    - watch_any:
      - test: a
      - test: c
