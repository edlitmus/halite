# Every shape the highstate outputter draws differently: changes, none,
# a name that is not the ID, a failure, a requisite that held a state
# back, a comment over several lines, a warning long enough to wrap,
# changes holding numbers, lists and nested maps, and states run in
# parallel, whose durations the summary adds differently.
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

numbers:
  cmd.run:
    - name: 'echo one; echo two'

nested:
  module.run:
    - test.arg:
      - 1
      - 2.5
      - [a, b]
      - first: [x, y]
        second:
          inner: true

parallel one:
  cmd.run:
    - name: 'sleep 0.2'
    - parallel: True

parallel two:
  cmd.run:
    - name: 'sleep 0.1'
    - parallel: True

# A warning Python's textwrap breaks inside its hyphenated words, which a
# wrap on whitespace alone does not (DIVERGENCE 5.274).
hyphenated warning:
  test.configurable_test_state:
    - changes: False
    - result: True
    - warnings: "the state-of-the-art well-known self-signed certificate was re-issued for the long-running hub-and-spoke deployment"
