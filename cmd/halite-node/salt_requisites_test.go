package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// saltRequisiteCase is one case of testdata/salt-requisites: what Salt
// did with srv/<case>.sls, reduced from `salt-call --local --out=json
// state.apply` by capture.sh's run. Exactly one of States and Error.
type saltRequisiteCase struct {
	Exit   int    `json:"exit"`
	Error  string `json:"error"`
	States []struct {
		Tag     string         `json:"tag"`
		ID      *string        `json:"id"`
		Result  *bool          `json:"result"`
		Changes map[string]any `json:"changes"`
		Comment string         `json:"comment"`
	} `json:"states"`
}

// refused reports whether Salt refused the run rather than ran it: a
// list of errors, a raise, a requisite it refused as recursive (3006.28
// fails that state and runs the rest, 3008.3 refuses the run), or
// listen's error state.
func (c saltRequisiteCase) refused() bool {
	if c.Error != "" {
		return true
	}
	for _, s := range c.States {
		if s.Comment == "Recursive requisite found" || strings.HasPrefix(s.Tag, "Listen_Error_") {
			return true
		}
	}
	return false
}

// saltWords are Salt's comments that halite words differently, with the
// same result. Every other comment is compared.
var saltWords = []string{"Success!", "Failure!", "One or more requisite failed",
	"State was not run because", "No changes detected"}

// Where halite refuses what Salt runs. Each is a decision recorded in
// the ledger, and the test holds that halite still refuses it, so that
// a change of mind is made here rather than by accident.
var haliteRefusesWhatSaltRuns = map[string]string{
	// An `_in` naming an sls glob, or an sls not in the run: Salt attaches
	// nothing, so the states it might have guarded run unguarded.
	"i_watch_glob": "5.476", "i_watch_none": "5.476",
	"i_onchanges_glob": "5.476", "i_onchanges_none": "5.476",
	"i_onfail_glob": "5.476", "i_onfail_none": "5.476",
	"i_prereq_glob": "5.476", "i_prereq_none": "5.476",
	// An `_any_in`, which Salt does not have and passes to the function.
	"i_require_any": "5.478", "i_watch_any": "5.478",
	"i_onchanges_any": "5.478", "i_onfail_any": "5.478",
	"s_require_any_in": "5.478",
}

// Every requisite case captured from Salt, run through halite: the `_in`
// kinds naming an sls (firing and not, a glob, an sls not in the run),
// a state naming itself in each requisite 5.461 did not capture, and the
// sls requisites of 5.475 and 5.476 executed rather than only compiled.
// Each state's result and whether it changed must be Salt's, and so must
// any comment halite does not word on its own; run order is not
// compared, because SPEC 11.4's differs from Salt's (5.476).
//
// Salt 3008.3 is the reference. 3006.28 is held where it ran the case:
// it crashed on two self-requisites 3008.3 refuses, and failed the
// state rather than the run on the rest. DIVERGENCE 5.478.
func TestRequisitesBehaveAsSaltsCaptured(t *testing.T) {
	dir := filepath.Join("testdata", "salt-requisites")
	load := func(version string) map[string]saltRequisiteCase {
		raw, err := os.ReadFile(filepath.Join(dir, "salt-"+version+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases map[string]saltRequisiteCase `json:"cases"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Cases
	}
	latest, older := load("3008.3"), load("3006.28")

	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) != 42 {
		t.Fatalf("the fixture has %d cases; capture.sh ran 42", len(names))
	}
	srv, err := filepath.Abs(filepath.Join(dir, "srv"))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range names {
		salt := latest[name]
		t.Run(name, func(t *testing.T) {
			if o := older[name]; o.refused() != salt.refused() && name != "s_prereq_in" && name != "s_onfail_all" {
				t.Fatalf("3006.28 and 3008.3 disagree on whether to run this; the fixture needs a decision")
			}
			got := run(t, append([]string{"state", "apply", name},
				isolated(t, srv, t.TempDir(), "--out", "json")...)...)
			var returns map[string]map[string]any
			ranIt := json.Unmarshal([]byte(got.stdout), &returns) == nil && len(returns) > 0

			if ledger, ok := haliteRefusesWhatSaltRuns[name]; ok {
				if salt.refused() {
					t.Fatalf("Salt refused this too; the exception for %s is stale", ledger)
				}
				if ranIt {
					t.Errorf("halite ran what it refuses by decision (%s):\n%s", ledger, got.stdout)
				}
				return
			}
			if salt.refused() {
				if ranIt || got.code == 0 {
					t.Errorf("Salt refused this and halite ran it, exit %d:\n%s", got.code, got.stdout)
				}
				return
			}
			if !ranIt {
				t.Fatalf("Salt ran this and halite did not, exit %d:\n%s", got.code, got.stderr)
			}

			byID := map[string]map[string]any{}
			for _, r := range returns {
				byID[r["__id__"].(string)] = r
			}
			// A listen reaction is its own entry in Salt, `listener_<id>`,
			// and replaces the state's entry in halite.
			want := map[string]int{}
			for i, s := range salt.States {
				id := *s.ID
				if strings.HasPrefix(s.Tag, "test_|-listener_") {
					id = strings.TrimPrefix(id, "listener_")
				}
				want[id] = i
			}
			if len(want) != len(byID) {
				t.Errorf("Salt ran %d states and halite %d", len(want), len(byID))
			}
			for id, i := range want {
				s := salt.States[i]
				h := byID[id]
				if h == nil {
					t.Errorf("%s: Salt ran it and halite did not", id)
					continue
				}
				if h["result"] != *s.Result {
					t.Errorf("%s: result %v, Salt %v (halite: %v; Salt: %s)", id, h["result"], *s.Result, h["comment"], s.Comment)
				}
				hChanges, _ := h["changes"].(map[string]any)
				if (len(hChanges) > 0) != (len(s.Changes) > 0) {
					t.Errorf("%s: changes %v, Salt %v", id, hChanges, s.Changes)
				}
				ownWords := false
				for _, w := range saltWords {
					ownWords = ownWords || strings.HasPrefix(s.Comment, w)
				}
				if ownWords {
					continue
				}
				comment := strings.TrimPrefix(h["comment"].(string), "Listening reaction, run at the end of the state run. ")
				if comment != s.Comment {
					t.Errorf("%s: comment %q, Salt %q", id, comment, s.Comment)
				}
				if !reflect.DeepEqual(normalJSON(t, hChanges), normalJSON(t, s.Changes)) {
					t.Errorf("%s: changes %v, Salt %v", id, hChanges, s.Changes)
				}
			}
		})
	}
}

// normalJSON passes v through JSON, so that two decodings compare.
func normalJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
