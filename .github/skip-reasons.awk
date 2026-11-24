# For every test `go test -v` reports as skipped, print what that test
# logged: one line per log entry, each the entry's first line, prefixed
# with the test's name. Used by fleet.yml's live legs (issue 857).
#
#   awk -f .github/skip-reasons.awk /tmp/live.txt
#
# It replaces `awk '/^--- SKIP/ { print $3": "prev } { prev=$0 }'`, which
# took the line just before `--- SKIP` to be the reason. It is not, in two
# shapes the linux leg shows (Fleet job 138603165785):
#
#   - A Cleanup that logs. Cleanups run after t.Skip, and what they log
#     comes before `--- SKIP`, so the kernelpkg removal test's "reason" was
#     the last line of the kernel set its Cleanup had put back.
#   - A skip message of more than one line. The iptables and nftables tests
#     run themselves again in a network namespace and skip with the child's
#     output, whose last line is `PASS`, so six skips read "PASS".
#
# So the reason is read from the test's own block, from its `=== RUN` to
# its `--- SKIP`. Within it, `go test -v` puts each entry of the test's log
# at four spaces and that entry's further lines at eight. Only the first
# line of each entry is printed: it holds the file and line, and the rest
# (a namespace child's output, a package list) is what misled the old
# reading. Every entry is printed, not a guess at which one is the skip
# message, because the text cannot tell t.Skip from t.Log, and a Cleanup's
# entries come after it.
#
# Each `=== RUN`, `=== CONT` and `=== NAME` line names the test whose
# lines follow, by its full name, and `=== PAUSE` ends a block. A
# subtest's log lines are at four spaces too, under its own name, so they
# are kept apart from the parent's rather than read as its reason, and
# `=== NAME Parent` returns to the parent; parallel tests, which
# interleave, read as their own the same way. Subtests that skip are not
# reported here, as before: the step counts top-level skips only. (All of
# this is from `go test -v`'s real output, in
# internal/buildpolicy/testdata, not from its documentation.)
#
# POSIX awk only: the freebsd leg runs FreeBSD's, the linux leg mawk.

/^=== (RUN|CONT|NAME) / {
	cur = $3
	if ($2 == "RUN") {
		said[cur] = ""
		n[cur] = 0
	}
	next
}

/^=== PAUSE / {
	cur = ""
	next
}

/^--- SKIP: / {
	name = $3
	if (n[name] == 0)
		print name ": (it logged nothing)"
	else
		printf "%s", said[name]
	cur = ""
	next
}

/^--- / {
	cur = ""
	next
}

cur != "" && /^    [^ ]/ && !/^    --- / {
	entry = $0
	sub(/^    /, "", entry)
	said[cur] = said[cur] cur ": " entry "\n"
	n[cur]++
}
