package modules

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceSubstitutesEveryMatch(t *testing.T) {
	path := editFixture(t, "listen 80;\nlisten 80;\n")
	args := map[string]any{"name": path, "pattern": "listen 80;", "repl": "listen 8080;"}

	r := fileReplace(&Ctx{}, "nginx", args)
	if !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	if got := read(t, path); got != "listen 8080;\nlisten 8080;\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if r := fileReplace(&Ctx{}, "nginx", args); r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}
}

func TestReplaceHonoursCount(t *testing.T) {
	path := editFixture(t, "x\nx\nx\n")
	fileReplace(&Ctx{}, "f", map[string]any{"name": path, "pattern": "x", "repl": "y", "count": "2"})
	if got := read(t, path); got != "y\ny\nx\n" {
		t.Fatalf("count should bound the substitutions, got %q", got)
	}
}

func TestReplaceExpandsCaptureGroups(t *testing.T) {
	path := editFixture(t, "Port 22\n")
	fileReplace(&Ctx{}, "f", map[string]any{"name": path, "pattern": `Port (\d+)`, "repl": "Port 2$1"})
	if got := read(t, path); got != "Port 222\n" {
		t.Fatalf("want a Go-style $1 expansion, got %q", got)
	}
}

func TestReplaceAppendsWhenNothingMatches(t *testing.T) {
	path := editFixture(t, "Port 22\n")
	args := map[string]any{
		"name": path, "pattern": "^PermitRootLogin .*", "repl": "PermitRootLogin no",
		"append_if_not_found": "true",
	}
	if r := fileReplace(&Ctx{}, "sshd", args); !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	if got := read(t, path); got != "Port 22\nPermitRootLogin no\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if r := fileReplace(&Ctx{}, "sshd", args); r.Changed {
		t.Fatalf("the appended line now matches, so this is a no-op: %+v", r)
	}
}

func TestReplaceLeavesAFileWithNoMatchAlone(t *testing.T) {
	path := editFixture(t, "Port 22\n")
	r := fileReplace(&Ctx{}, "f", map[string]any{"name": path, "pattern": "nowhere", "repl": "x"})
	if r.Changed {
		t.Fatalf("without append_if_not_found there is nothing to do: %+v", r)
	}
	if got := read(t, path); got != "Port 22\n" {
		t.Fatalf("unexpected contents %q", got)
	}
}

func TestReplaceOnAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if r := fileReplace(&Ctx{}, "f", map[string]any{"name": path, "pattern": "x", "repl": "y"}); r.Ok {
		t.Fatal("a missing file should fail the state")
	}
	r := fileReplace(&Ctx{}, "f", map[string]any{
		"name": path, "pattern": "x", "repl": "y", "ignore_if_missing": "true",
	})
	if !r.Ok || r.Changed {
		t.Fatalf("ignore_if_missing should make it a no-op: %+v", r)
	}
}

func TestReplaceRejectsABadPattern(t *testing.T) {
	path := editFixture(t, "x\n")
	if r := fileReplace(&Ctx{}, "f", map[string]any{"name": path, "pattern": "^[unclosed", "repl": "y"}); r.Ok {
		t.Fatal("an invalid pattern should fail the state")
	}
	if r := fileReplace(&Ctx{}, "f", map[string]any{"name": path, "repl": "y"}); r.Ok {
		t.Fatal("replace with no pattern should fail the state")
	}
}

func TestBlockReplaceManagesItsOwnBlock(t *testing.T) {
	path := editFixture(t, "127.0.0.1 localhost\n")
	args := map[string]any{
		"name":                path,
		"marker_start":        "# BEGIN halite",
		"marker_end":          "# END halite",
		"content":             "10.0.0.1 db1",
		"append_if_not_found": "true",
	}

	if r := fileBlockReplace(&Ctx{}, "hosts", args); !r.Changed {
		t.Fatalf("want the block appended: %+v", r)
	}
	want := "127.0.0.1 localhost\n# BEGIN halite\n10.0.0.1 db1\n# END halite\n"
	if got := read(t, path); got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
	if r := fileBlockReplace(&Ctx{}, "hosts", args); r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}

	args["content"] = "10.0.0.2 db2"
	if r := fileBlockReplace(&Ctx{}, "hosts", args); !r.Changed {
		t.Fatalf("changed content should rewrite the block: %+v", r)
	}
	want = "127.0.0.1 localhost\n# BEGIN halite\n10.0.0.2 db2\n# END halite\n"
	if got := read(t, path); got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestBlockReplaceLeavesTheRestOfTheFile(t *testing.T) {
	path := editFixture(t, "before\n# BEGIN halite\nold\n# END halite\nafter\n")
	fileBlockReplace(&Ctx{}, "f", map[string]any{
		"name": path, "marker_start": "# BEGIN halite", "marker_end": "# END halite",
		"content": "new",
	})
	want := "before\n# BEGIN halite\nnew\n# END halite\nafter\n"
	if got := read(t, path); got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestBlockReplaceReportsAnUnclosedBlock(t *testing.T) {
	path := editFixture(t, "# BEGIN halite\nstuff\n")
	r := fileBlockReplace(&Ctx{}, "f", map[string]any{
		"name": path, "marker_start": "# BEGIN halite", "marker_end": "# END halite", "content": "x",
	})
	if r.Ok || !strings.Contains(r.Comment, "marker_end") {
		t.Fatalf("an unclosed block should be reported: %+v", r)
	}
}

func TestBlockReplaceNeedsBothMarkers(t *testing.T) {
	path := editFixture(t, "x\n")
	if r := fileBlockReplace(&Ctx{}, "f", map[string]any{"name": path, "marker_start": "# B"}); r.Ok {
		t.Fatal("a block with one marker cannot be found again")
	}
}

func TestReplaceAnchorsMatchLineBoundaries(t *testing.T) {
	// Salt's file.replace defaults to MULTILINE; a pattern anchored with ^
	// is the normal way to write one, and it must not silently match
	// nothing just because the line is not the first.
	path := editFixture(t, "Port 22\n#PermitRootLogin yes\nX11Forwarding yes\n")
	r := fileReplace(&Ctx{}, "sshd", map[string]any{
		"name": path, "pattern": "^#?PermitRootLogin .*", "repl": "PermitRootLogin no",
	})
	if !r.Changed {
		t.Fatalf("want the commented line replaced: %+v", r)
	}
	if got := read(t, path); got != "Port 22\nPermitRootLogin no\nX11Forwarding yes\n" {
		t.Fatalf("unexpected contents %q", got)
	}
}
