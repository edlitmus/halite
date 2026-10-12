# Every requisite on one page, using the test module so that it runs
# anywhere and changes nothing. "changer" always reports a change,
# "quiet" never does, and "breaker" always fails.

changer:
  test.succeed_with_changes: []

quiet:
  test.succeed_without_changes: []

breaker:
  test.fail_without_changes: []

# require: after, and only if the target succeeded.
needs_quiet:
  test.succeed_without_changes:
    - require:
      - test: quiet

# require on something that failed: skipped, and says why.
needs_breaker:
  test.succeed_without_changes:
    - require:
      - test: breaker

# watch: like require, plus react if the target changed. cmd.wait is the
# state that only runs when something it watches changed.
react_to_changer:
  cmd.wait:
    - name: echo reacted
    - watch:
      - test: changer

react_to_quiet:
  cmd.wait:
    - name: echo reacted
    - watch:
      - test: quiet

# onchanges: run only if the target reported a change.
only_if_changer_changed:
  test.succeed_with_changes:
    - onchanges:
      - test: changer

only_if_quiet_changed:
  test.succeed_with_changes:
    - onchanges:
      - test: quiet

# onfail: run only if the target failed. This is how a rollback is written.
rollback:
  test.succeed_with_changes:
    - onfail:
      - test: breaker

no_rollback_needed:
  test.succeed_with_changes:
    - onfail:
      - test: quiet

# require_in: the dependency written on the other side.
declared_first:
  test.succeed_without_changes:
    - require_in:
      - test: declared_second

declared_second:
  test.succeed_without_changes: []

# listen: like watch, but the reaction happens at the end of the run.
at_the_end:
  test.succeed_with_changes:
    - listen:
      - test: changer
