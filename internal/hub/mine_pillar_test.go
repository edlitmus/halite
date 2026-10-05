package hub

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// mineLab is three connected nodes over pillarTargetTree -- web1 has
// role web, db1 is FreeBSD and so has platform bsd, broken1's pillar
// will not compile -- each having published `backend`, the last two
// arguments being the allow_tgt every entry carries.
func mineLab(t *testing.T, allowTgt, allowKind string) *lab {
	t.Helper()
	l := newLab(t).withJobs(t).withMine(t).withPillar(t, pillarTargetTree)
	for _, n := range []struct{ id, grains string }{
		{"web1.example", `{"os":"Linux"}`},
		{"db1.example", `{"os":"FreeBSD"}`},
		{"broken1.example", `{"os":"Linux"}`},
	} {
		client := l.enrolled(t, n.id)
		t.Cleanup(l.connect(t, client, n.id, n.grains))
		if err := l.server.Mine.Update(n.id, map[string]*MineEntry{
			"backend": {Data: json.RawMessage(`"` + n.id + `"`), AllowTgt: allowTgt, AllowKind: allowKind},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func mineNodes(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// `mine.get` with a pillar target reads each publisher's compiled pillar,
// as dispatch does (DIVERGENCE 5.204).
//
// It read an empty one. So `I@platform:bsd` matched nothing at all, and
// `not I@role:web` matched every publisher including the web hosts -- a
// template asking the mine for "everything but the web tier" was handed
// the web tier. And a publisher whose pillar will not compile has no
// answer: it is refused, naming the node, rather than guessed about.
func TestMineGetMatchesPillarTargetsAgainstCompiledPillar(t *testing.T) {
	l := mineLab(t, "", "")

	got, err := l.server.MineGet("", "L@web1.example,db1.example and I@platform:bsd", "compound", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if ids := mineNodes(got); len(ids) != 1 || ids[0] != "db1.example" {
		t.Errorf("I@platform:bsd read the mine of %v, want [db1.example]", ids)
	}
	got, err = l.server.MineGet("", "L@web1.example,db1.example and not I@role:web", "compound", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if ids := mineNodes(got); len(ids) != 1 || ids[0] != "db1.example" {
		t.Errorf("not I@role:web read the mine of %v, want [db1.example]", ids)
	}

	for _, tc := range []struct{ target, kind string }{
		{"not I@role:db", "compound"},
		{"role:web", "pillar"},
	} {
		got, err := l.server.MineGet("", tc.target, tc.kind, "backend")
		if err == nil {
			t.Errorf("%s %q read %v with a publisher's pillar uncompilable", tc.kind, tc.target, mineNodes(got))
			continue
		}
		if !strings.Contains(err.Error(), "broken1.example") {
			t.Errorf("%s %q: the refusal does not name the node: %v", tc.kind, tc.target, err)
		}
	}
}

// allow_tgt is the publisher's own restriction on which nodes may read
// its entry (SPEC 19.5), and a pillar term in it is checked against the
// reader's compiled pillar.
//
// It was checked against an empty one, which fails open under `not`: an
// entry published with `not I@role:web` -- keep this from the web tier --
// was readable by the web tier. A reader whose pillar will not compile
// is refused the entry, since whether the publisher meant to allow it is
// unknown and the restriction exists to withhold.
func TestMineAllowTgtReadsTheReadersCompiledPillar(t *testing.T) {
	l := mineLab(t, "not I@role:web", "compound")

	for _, tc := range []struct {
		reader string
		want   bool
	}{
		{"db1.example", true},
		{"web1.example", false},
		{"broken1.example", false},
	} {
		got, err := l.server.MineGet(tc.reader, "db1.example", "glob", "backend")
		if err != nil {
			t.Fatalf("%s: %v", tc.reader, err)
		}
		if _, saw := got["db1.example"]; saw != tc.want {
			t.Errorf("%s reading an entry restricted to `not I@role:web`: saw it %v, want %v",
				tc.reader, saw, tc.want)
		}
	}

	// And the positive form, which matched nobody at all.
	l = mineLab(t, "platform:bsd", "pillar")
	got, err := l.server.MineGet("db1.example", "web1.example", "glob", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if _, saw := got["web1.example"]; !saw {
		t.Error("db1, whose pillar has platform: bsd, was refused an entry restricted to -I platform:bsd")
	}
}

// A read across many entries that each restrict by pillar compiles the
// reader's pillar once. The reader is one node with one pillar; asking
// about it once per publisher would put a render of its whole pillar
// tree -- external sources and GPG included -- in front of every entry.
func TestMineAllowTgtCompilesTheReadersPillarOnce(t *testing.T) {
	l := mineLab(t, "platform:bsd", "pillar")
	before := l.server.pillarCompiles.Load()
	got, err := l.server.MineGet("db1.example", "L@web1.example,db1.example", "compound", "backend")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("db1 read %v, want both entries", mineNodes(got))
	}
	if n := l.server.pillarCompiles.Load() - before; n != 1 {
		t.Errorf("reading 2 entries restricted by pillar compiled the reader's pillar %d times, want 1", n)
	}
}
