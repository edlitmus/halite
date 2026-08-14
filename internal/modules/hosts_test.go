package modules

import (
	"strings"
	"testing"
)

// hostsFixture writes a hosts file and returns args pointing at it.
func hostsFixture(t *testing.T, body string) (map[string]any, string) {
	t.Helper()
	path := editFixture(t, body)
	return map[string]any{"config": path}, path
}

func TestHostIsAddedThenLeftAlone(t *testing.T) {
	args, path := hostsFixture(t, "127.0.0.1\tlocalhost\n")
	args["ip"] = "10.0.0.1"

	if r := hostPresent(&Ctx{}, "db1", args); !r.Changed {
		t.Fatalf("want the entry added: %+v", r)
	}
	if got := read(t, path); got != "127.0.0.1\tlocalhost\n10.0.0.1\tdb1\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	if r := hostPresent(&Ctx{}, "db1", args); r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}
}

func TestSeveralNamesShareOneLine(t *testing.T) {
	args, path := hostsFixture(t, "127.0.0.1\tlocalhost\n")
	args["ip"] = "10.0.0.1"
	args["names"] = []any{"db1", "db1.internal"}

	hostPresent(&Ctx{}, "db", args)
	if got := read(t, path); got != "127.0.0.1\tlocalhost\n10.0.0.1\tdb1 db1.internal\n" {
		t.Fatalf("names for one address belong on one line, got %q", got)
	}
}

func TestNameIsAddedToTheAddressAlreadyThere(t *testing.T) {
	args, path := hostsFixture(t, "10.0.0.1\tdb1\n")
	args["ip"] = "10.0.0.1"

	hostPresent(&Ctx{}, "db1.internal", args)
	if got := read(t, path); got != "10.0.0.1\tdb1 db1.internal\n" {
		t.Fatalf("want the name appended to the existing line, got %q", got)
	}
}

func TestUnrelatedLinesSurvive(t *testing.T) {
	start := "# a comment\n\n127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost\n"
	args, path := hostsFixture(t, start)
	args["ip"] = "10.0.0.1"

	hostPresent(&Ctx{}, "db1", args)
	got := read(t, path)
	for _, want := range []string{"# a comment", "127.0.0.1\tlocalhost", "::1\tlocalhost ip6-localhost"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q kept, got %q", want, got)
		}
	}
}

func TestTrailingCommentsAreKept(t *testing.T) {
	args, path := hostsFixture(t, "10.0.0.1\tdb1 # the database\n")
	args["ip"] = "10.0.0.1"

	hostPresent(&Ctx{}, "db1.internal", args)
	if got := read(t, path); got != "10.0.0.1\tdb1 db1.internal # the database\n" {
		t.Fatalf("want the comment kept, got %q", got)
	}
}

func TestCleanMovesANameOffAnotherAddress(t *testing.T) {
	args, path := hostsFixture(t, "10.0.0.9\tdb1 old\n")
	args["ip"] = "10.0.0.1"

	// Without clean, the old entry stands: removing it is destructive.
	hostPresent(&Ctx{}, "db1", args)
	if got := read(t, path); !strings.Contains(got, "10.0.0.9\tdb1 old") {
		t.Fatalf("the other address should be left alone, got %q", got)
	}

	args2, path2 := hostsFixture(t, "10.0.0.9\tdb1 old\n")
	args2["ip"] = "10.0.0.1"
	args2["clean"] = "true"
	r := hostPresent(&Ctx{}, "db1", args2)
	if !strings.Contains(r.Comment, "moved") {
		t.Fatalf("want the move reported: %+v", r)
	}
	if got := read(t, path2); got != "10.0.0.9\told\n10.0.0.1\tdb1\n" {
		t.Fatalf("want the name moved, got %q", got)
	}
	_ = path
}

func TestHostAbsentRemovesTheNameAndEmptyLines(t *testing.T) {
	args, path := hostsFixture(t, "127.0.0.1\tlocalhost\n10.0.0.1\tdb1 db1.internal\n")

	if r := hostAbsent(&Ctx{}, "db1.internal", args); !r.Changed {
		t.Fatalf("want the name removed: %+v", r)
	}
	if got := read(t, path); got != "127.0.0.1\tlocalhost\n10.0.0.1\tdb1\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	hostAbsent(&Ctx{}, "db1", args)
	if got := read(t, path); got != "127.0.0.1\tlocalhost\n" {
		t.Fatalf("a line with no names left should go, got %q", got)
	}
	if r := hostAbsent(&Ctx{}, "db1", args); r.Changed {
		t.Fatalf("removing what is not there is a no-op: %+v", r)
	}
}

func TestHostAbsentCanBeLimitedToOneAddress(t *testing.T) {
	args, path := hostsFixture(t, "10.0.0.1\tdb1\n10.0.0.9\tdb1\n")
	args["ip"] = "10.0.0.9"

	hostAbsent(&Ctx{}, "db1", args)
	if got := read(t, path); got != "10.0.0.1\tdb1\n" {
		t.Fatalf("only the named address should lose the name, got %q", got)
	}
}

func TestHostPresentNeedsAnAddress(t *testing.T) {
	args, _ := hostsFixture(t, "")
	if r := hostPresent(&Ctx{}, "db1", args); r.Ok {
		t.Fatal("an entry with no address cannot be written")
	}
}

func TestHostDryRunWritesNothing(t *testing.T) {
	args, path := hostsFixture(t, "127.0.0.1\tlocalhost\n")
	args["ip"] = "10.0.0.1"

	r := hostPresent(&Ctx{Test: true}, "db1", args)
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending entry: %+v", r)
	}
	if got := read(t, path); got != "127.0.0.1\tlocalhost\n" {
		t.Fatalf("a dry run must not write: %q", got)
	}
}
