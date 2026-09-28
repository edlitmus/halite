package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/signature"
)

// `user.present` with `groups:` is reported, because it runs and means
// something else.
//
// # Why this needed a category of its own
//
// Every other question this audit asks is "would this run?", and for a whole
// class of difference that question has the wrong answer: these run perfectly
// and do something different. Salt's `remove_groups` defaults to **true**, so
// `groups:` there was the account's complete supplementary set and anything
// unlisted was taken away. Here it defaults to false, so `groups:` means "must
// be in these" and a membership added by hand survives a run that never named
// it.
//
// A tree relying on Salt's pruning therefore gets a run that prunes nothing and
// reports success — the benign direction, and so the one an operator never finds
// out about. `docs/from-salt.md` item 5 used to end by telling the reader to grep
// their own tree, which is a worse answer than a finding with a file and a line.
//
// DIVERGENCE 5.162, plan.md 19h.
func userPresentRegistry() *signature.Registry {
	states := signature.NewRegistry()
	states.Add(
		signature.Signature{Module: "user", Function: "present", Params: []signature.Param{
			{Name: "name", Type: signature.String},
			{Name: "groups", Type: signature.List},
			{Name: "remove_groups", Type: signature.Bool},
		}},
		signature.Signature{Module: "user", Function: "absent", Params: []signature.Param{
			{Name: "name", Type: signature.String},
		}},
	)
	return states
}

func auditOneFile(t *testing.T, body string) *Report {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "accounts.sls"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(Options{Root: root, StateRegistry: userPresentRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestUserPresentGroupsIsReportedAsASemanticDifference(t *testing.T) {
	rep := auditOneFile(t, `deploy:
  user.present:
    - name: deploy
    - groups:
      - docker
      - sudo
`)
	found := findingsFor(rep, CatSemantics)
	if len(found) != 1 {
		t.Fatalf("semantics findings = %v, want one for user.present.groups", found)
	}
	f := found[0]
	if f.Subject != "user.present.groups" {
		t.Errorf("subject = %q, want user.present.groups", f.Subject)
	}
	// Review and not blocking: the tree runs. A blocking finding here would
	// tell an operator to stop for something that works.
	if f.Severity != Review {
		t.Errorf("severity = %s, want review; this declaration runs", f.Severity)
	}
	// The line of the `groups` key, so the reader is taken to the
	// declaration rather than to the top of the file.
	if f.Line != 4 {
		t.Errorf("line = %d, want 4, where `groups` is", f.Line)
	}
	if !strings.Contains(f.Action, "remove_groups: true") {
		t.Errorf("the action should name the setting that restores Salt's behaviour: %q", f.Action)
	}
	if !strings.Contains(f.Action, "from-salt.md") {
		t.Errorf("the action should point at the document that lists these: %q", f.Action)
	}
}

// **A state that already names `remove_groups` says nothing.**
//
// Either value is a decision the operator has made, and reporting a decision
// back as a finding is the audit describing work that does not exist — the
// failing the `cmd_default_shell` branch was corrected for. Both values are
// checked, because "true" silencing it and "false" not would be a rule about
// agreeing with Salt rather than about the operator having chosen.
func TestUserPresentWithRemoveGroupsStatedIsNotReported(t *testing.T) {
	for _, stated := range []string{"true", "false"} {
		rep := auditOneFile(t, `deploy:
  user.present:
    - name: deploy
    - groups:
      - docker
    - remove_groups: `+stated+"\n")
		if found := findingsFor(rep, CatSemantics); len(found) != 0 {
			t.Errorf("remove_groups: %s still reported %v; the operator has decided",
				stated, found)
		}
	}
}

// Nothing else is dragged in: a `user.present` without `groups`, and a
// `groups` on a different state, are both silent.
func TestOnlyUserPresentWithGroupsIsReported(t *testing.T) {
	rep := auditOneFile(t, `deploy:
  user.present:
    - name: deploy
    - remove_groups: true

gone:
  user.absent:
    - name: old
`)
	if found := findingsFor(rep, CatSemantics); len(found) != 0 {
		t.Errorf("findings = %v, want none", found)
	}
}

// The finding reaches the rendered report and the JSON, because a finding the
// effort estimate does not count is a finding nobody plans for. The estimate
// iterates the category counts rather than a written-down list of categories,
// so this is the assertion that a new category needs no table entry -- and
// that nothing silently drops one it does not recognise.
func TestSemanticsFindingsReachTheReportAndTheJSON(t *testing.T) {
	rep := auditOneFile(t, `deploy:
  user.present:
    - name: deploy
    - groups:
      - docker
`)
	text := rep.Summary()
	if !strings.Contains(text, "semantics") {
		t.Errorf("the rendered report does not name the category:\n%s", text)
	}
	if !strings.Contains(text, "remove_groups") {
		t.Errorf("the rendered report does not carry the action:\n%s", text)
	}
	// And it is not blocking, so a tree whose only finding is this one is
	// still reported as applicable.
	if !strings.Contains(text, "No blocking items") {
		t.Errorf("a tree whose only finding is a semantic difference reads as blocked:\n%s", text)
	}

	raw, ok := rep.JSON().Get("findings")
	if !ok {
		t.Fatal("the JSON has no findings")
	}
	list, _ := raw.([]any)
	if len(list) != 1 {
		t.Fatalf("the JSON holds %d findings, want 1", len(list))
	}
}
