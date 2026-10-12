# How the Handbook is built

<!-- abstract: How the Handbook is written, checked, indexed and shipped: the manifest, the directives, the generated chapters, the examples that are run, and the commands that build it. -->
<!-- kind: guide -->
<!-- index: handbook; CONTENTS; tools/handbook; make handbook; directive; generated chapter; example directive; auto-index; reproducible build -->

This chapter is for someone changing the documentation. It describes the
machinery that makes "the documentation is up to date" a property of the
build instead of an intention.

## The pieces

| Piece | Where | Job |
|---|---|---|
| The manifest | `docs/handbook/CONTENTS` | The one list of what is in the book and in what order. A chapter not listed is an orphan and fails the check. |
| Chapters | `docs/handbook/NN-name.md` | Hand-written Markdown. The number in the file name must equal the chapter's number in the manifest. |
| Generated chapters | `docs/handbook/gen-*.md` | Written by `go run ./tools/handbook gen` from the code. Never edited by hand. |
| Examples | `contrib/examples/handbook/` | Files that tests run. A chapter quotes them. |
| The converter | `internal/handbook` | A small Markdown-subset to HTML renderer that refuses raw HTML, and the checks. |
| The command | `tools/handbook` | `check`, `gen` and `build`. |

The converter is in the repository because the project has one vendored
dependency and a Markdown library is not going to be the second. It
converts the dialect the handbook is written in, and it refuses what it does
not understand rather than guessing.

## Commands

```sh
go run ./tools/handbook check          # hold every chapter to the rules
go run ./tools/handbook gen            # rewrite the generated chapters and README
make handbook                          # check, then build HTML into build/handbook
```

`make dist` depends on `make handbook`, and `tools/disttar` refuses to make
an archive without the built handbook. The two builders of the release
workflow build it independently and their archives are compared byte for
byte, so a handbook that depended on the clock or the machine would stop the
release.

## What the check refuses

- A chapter that is not in `CONTENTS`, or whose file number disagrees with it.
- A chapter without exactly one level-one heading, or a heading that skips a level.
- A link to a file or a heading that does not exist, and a link whose text
  says "chapter 14" but points at some other chapter.
- A chapter marked `kind: runbook` that lacks **Before you start**,
  **Procedure**, **Verify** or **If it goes wrong**.
- A code block carrying an `example:` directive whose text differs from the
  file it names, by a single byte.
- Raw HTML.

Tests in `internal/docsaudit/handbook_test.go` add the checks that need the
code: the generated chapters equal what `gen` writes now, every package is
described, and every command, beacon, runner, requisite and event tag the
build knows is mentioned somewhere in the book.

## Directives

A directive is an HTML comment, so it is invisible on GitHub.

| Directive | Effect |
|---|---|
| `<!-- abstract: text -->` | The one-sentence summary shown on the contents page. |
| `<!-- kind: runbook -->` | One of `concept`, `tutorial`, `guide`, `runbook`, `reference`. |
| `<!-- index: a; b; c -->` | Concept terms for the index. |
| `<!-- example: handbook/x/y.sls -->` | The next code block must equal that file under `contrib/examples`. A line range, `example: path lines=7-13`, quotes part of it. |

Commands, settings, module functions, beacons and event tags are indexed
without a directive: the index is built from the build's own registries and
matches the code spans in the prose, so a function added tomorrow is indexed
the day a chapter mentions it.

## Adding to the book

1. Write the chapter, number first in the file name, and add it to `CONTENTS`.
2. Put runnable material under `contrib/examples/handbook` and quote it with
   `example:`. Then make a test run it, as
   `cmd/halite-node/handbook_examples_test.go` does, and break the example on
   purpose to watch the test fail.
3. Run `go run ./tools/handbook gen` and `go test -count=1 ./internal/docsaudit ./internal/handbook`.
4. If you added a feature, the audits will name the chapter that has to
   mention it. That is the intended way for the handbook to stay current.

Do not write output into a chapter that you did not capture from a real run.
The project's rule applies to prose as it does to code: a claim nobody
demonstrated is a defect.
