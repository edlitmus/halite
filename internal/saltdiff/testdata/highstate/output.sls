# Every shape the highstate outputter draws differently: changes, none,
# a name that is not the ID, a failure, a requisite that held a state
# back, a comment over several lines, and a warning long enough to wrap.
changed:
  test.succeed_with_changes: []

unchanged:
  test.succeed_without_changes: []

named:
  test.succeed_without_changes:
    - name: a name that is not the ID

failed:
  test.fail_without_changes: []

held:
  test.succeed_without_changes:
    - require:
      - test: failed

multiline:
  test.configurable_test_state:
    - changes: False
    - result: True
    - comment: |
        the first line of a comment
        and the second line of it

warned:
  test.configurable_test_state:
    - changes: False
    - result: True
    - warnings: "a warning long enough that the outputter has to wrap it at eighty columns, which it does on whitespace"
