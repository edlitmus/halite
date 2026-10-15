# Captures Python's fnmatch.fnmatchcase over a table of patterns and
# names, for fnmatch_test.go to hold internal/fileserver/fnmatch.go to.
# Salt's file_ignore_glob is fnmatch.fnmatch, which is fnmatchcase on a
# POSIX hub. Regenerate with:
#
#   python3 -I fnmatch_cpython.py > fnmatch_cpython.json
#
# The JSON's "captured" line records the date and the Python that ran.
# DIVERGENCE 5.290.
import fnmatch, json, sys
pats = ["secrets/*", "*/er/*", "*", "*.swp", "?", "a?c", "[abc]", "[!abc]", "[a-c]x", "[!a-c]x",
        "[]]", "[!]]", "[]-]", "[z-a]", "[!z-a]", "[a-]", "[-a]", "[!-a]", "[a-cx-z]", "[z-ab-c]",
        "[", "a[b", "[!", "a\\b", "[\\]", "*.[ch]", "**/x", "a*b*c", "A*", "secrets", "secrets/",
        "*/secrets/*", ".git*", "*~", "[^a]", "\\*"]
names = ["secrets/a.txt", "secrets/deep/b.txt", "secrets/deep/er/c.txt", "public/x.txt", "x/er/y",
         "er/y", "a", "b", "d", "/", "]", "-", "!", "^", "ax", "dx", "yx", "zx", "a/c", "abc", "a.c",
         "foo.swp", "dir/foo.swp", "[", "a[b", "[!", "a\\b", "\\", "x.c", "x.h", "x.o", "q/x", "x",
         "a/b/c", "abc/x", "Abc", "secrets", "secrets/", "top/secrets/z", ".gitignore", ".git/HEAD",
         "notes~", "*", "a\nb"]
rows = []
for p in pats:
    for n in names:
        rows.append([p, n, fnmatch.fnmatchcase(n, p)])
import datetime, platform
captured = "%s, CPython %s (%s), by testdata/fnmatch_cpython.py: fnmatch.fnmatchcase(name, pattern) for every pair" % (
    datetime.date.today().isoformat(), platform.python_version(), platform.system())
sys.stdout.write('{"captured": %s, "rows": [\n  ' % json.dumps(captured))
sys.stdout.write(",\n  ".join(json.dumps(r) for r in rows))
sys.stdout.write("\n]}\n")
