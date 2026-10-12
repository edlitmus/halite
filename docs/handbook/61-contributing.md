# Contributing

<!-- abstract: How to change halite: the branch and review workflow, the gates a change must pass, the ledger and changelog, and the habits the project asks of every contributor. -->
<!-- kind: guide -->
<!-- index: contributing; pull request; make check; ledger; DIVERGENCE; changelog; commit message; evidence level -->

halite manages machines that people depend on, so the process is built
around one rule: **a claim nobody demonstrated is a defect**. That covers
code, comments, documentation and the report you write at the end of a task.

## Workflow

1. Branch from `main`; never commit to it.
2. Make one logical change per commit. A second defect found on the way gets
   its own commit, usually its own pull request.
3. Subjects are imperative, under 72 characters, prefixed `feat:`, `fix:`,
   `docs:`, `test:`, `ci:` or `chore:`. The body explains why and names what
   was measured rather than what was intended.
4. Open a pull request, wait for CI to be green, then merge.

## Gates

`make check` is the local gate: formatting, vet, build for every platform,
the tests, the race detector, the policy checks and the FIPS tests. Pass
`-count=1` when running tests by hand, because several audits read the whole
repository and a cached result hides a change.

The Makefile is written for BSD make. Use `!=` rather than `$(shell …)`, and
on a Mac use `bmake` or `gmake`.

## Habits the project asks for

- **Break it on purpose.** After fixing something, undo the fix and watch the
  test fail. A test that passes before and after was not testing the fix.
- **Capture fixtures; never write them from documentation.** A test built
  from a manual page tests the manual page.
- **Say what you did not verify**, in the commit, in the pull request and to
  the reviewer.
- **Look for the pair.** Most defects here are two things that must agree and
  do not: a parser and its tool, a document and its code, a fixture and a
  machine, a state's check and its change.

## Prose is checked

`SPEC.md`, `README.md`, `CHANGELOG.md`, `plan.md` and everything under `docs/`
are held to the code by tests. A change to a module is usually also a change to
`docs/DIVERGENCE.md`, sometimes to the specification, and often to a count in
a file you did not expect; run the audits and they say which. Generated pages
(`docs/modules.md`, `docs/configuration.md`, the handbook's `gen-*` chapters)
are regenerated, never edited.

When you fix a defect, add a ledger entry; when you change what an operator
sees, add a changelog entry. Two branches grab the same ledger number, so run
`go run ./tools/ledger next` for the next free one and
`go run ./tools/ledger renumber` just before merging.

## Evidence

Every module records how much anybody knows about it: assumed, captured or
hardware. A new module arrives assumed. Running it against the real tool on a
real system raises it, and the note says which machine, which version and what
was not covered, in the same change. See the chapter on
[testing and evidence](59-testing-and-evidence.md).
