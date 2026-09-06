package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// These test the parsers rather than the module functions.
//
// Every function in the module asks the real filesystem whether dpkg is
// installed before it runs anything, so on a machine without it — which
// is every machine this is developed on — a test that went in through
// the module would get the refusal and never reach the parsing. The
// parsing is where the defects are, so it is a function that takes a
// string. TestDpkgRefusesWhereItIsNotInstalled covers the other half.

// The listing reports a package's state rather than filtering on it.
//
// This is the whole difference from `pkg.list_pkgs`, which keeps only
// what is installed. A package left half-configured by an interrupted
// upgrade is not installed, so the high-level module does not mention
// it — and apt will refuse to do anything else until it is resolved, so
// it is exactly what an operator is looking for.
func TestTheListingReportsWhatIsNotInstalledToo(t *testing.T) {
	got := parseDpkgList(
		"nginx\tamd64\t1.24.0-1\tinstall ok installed\n" +
			"oldconf\tamd64\t2.1-3\tdeinstall ok config-files\n" +
			"broken\tamd64\t0.9-1\tinstall ok half-configured\n" +
			"held\tamd64\t3.3-2\thold ok installed\n")

	if got.Len() != 4 {
		t.Fatalf("%d packages, want 4: %v", got.Len(), got.StringKeys())
	}
	for _, tc := range []struct {
		name      string
		state     string
		want      string
		installed bool
	}{
		{"nginx", "installed", "install", true},
		{"oldconf", "config-files", "deinstall", false},
		{"broken", "half-configured", "install", false},
		{"held", "installed", "hold", true},
	} {
		raw, ok := got.Get(tc.name)
		if !ok {
			t.Errorf("%s is missing", tc.name)
			continue
		}
		m := raw.(*value.Map)
		if state, _ := m.Get("state"); state != tc.state {
			t.Errorf("%s state = %v, want %s", tc.name, state, tc.state)
		}
		if want, _ := m.Get("want"); want != tc.want {
			t.Errorf("%s want = %v, want %s", tc.name, want, tc.want)
		}
		if inst, _ := m.Get("installed"); inst != tc.installed {
			t.Errorf("%s installed = %v, want %v", tc.name, inst, tc.installed)
		}
	}
}

// Control output folds a long field across continuation lines, and a
// lone dot is how it spells a blank line inside one.
//
// Dropping the continuations would leave Description as its one-line
// summary and Conffiles as nothing at all, and Conffiles is the field
// that says which paths dpkg will argue with an operator about.
func TestControlFieldsKeepTheirContinuations(t *testing.T) {
	const control = `Package: nginx
Version: 1.24.0-1
Description: small, powerful, scalable web/proxy server
 Nginx is a web server.
 .
 It also does other things.
Conffiles:
 /etc/nginx/nginx.conf 1a2b3c
 /etc/nginx/mime.types 4d5e6f
Installed-Size: 1234
`
	got := parseControlFields(control)

	if v, _ := got.Get("Package"); v != "nginx" {
		t.Errorf("Package = %v", v)
	}
	// The field after a folded one is the one a naive parser loses.
	if v, _ := got.Get("Installed-Size"); v != "1234" {
		t.Errorf("Installed-Size = %v", v)
	}
	desc, _ := got.Get("Description")
	text, _ := desc.(string)
	for _, want := range []string{
		"scalable web/proxy server",
		"Nginx is a web server.",
		"It also does other things.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Description lost %q: %q", want, text)
		}
	}
	if strings.Contains(text, "\n.\n") {
		t.Errorf("the dot continuation was kept literally rather than as a blank line: %q", text)
	}
	conf, _ := got.Get("Conffiles")
	s, _ := conf.(string)
	if !strings.Contains(s, "/etc/nginx/nginx.conf") || !strings.Contains(s, "/etc/nginx/mime.types") {
		t.Errorf("Conffiles = %q", conf)
	}
}

// One path can have several owners, and dpkg lists them on one line.
func TestSearchHandlesSeveralOwners(t *testing.T) {
	got := parseDpkgSearch("pkg-one, pkg-two: /usr/share/doc/thing\n")
	for _, owner := range []string{"pkg-one", "pkg-two"} {
		files, ok := got.Get(owner)
		if !ok {
			t.Errorf("%s does not own the path", owner)
			continue
		}
		list, _ := files.([]any)
		if len(list) != 1 || list[0] != "/usr/share/doc/thing" {
			t.Errorf("%s owns %v", owner, files)
		}
	}
	if got.Len() != 2 {
		t.Errorf("%d owners, want 2: %v", got.Len(), got.StringKeys())
	}
}

// The file list drops the diversion lines dpkg mixes into it.
func TestTheFileListLeavesOutDiversionNotes(t *testing.T) {
	got := parseDpkgFileList("/usr\n" +
		"/usr/bin\n" +
		"/usr/bin/thing\n" +
		"package diverts others to: /usr/bin/thing.distrib\n" +
		"/etc/thing.conf\n")
	for _, path := range got {
		if s, _ := path.(string); strings.Contains(s, "diverts") {
			t.Errorf("a diversion note was reported as a path: %q", s)
		}
	}
	if len(got) != 4 {
		t.Errorf("%d paths, want 4: %v", len(got), got)
	}
}

// A selection that is not one of dpkg's four is refused before anything
// is written, because dpkg takes them on stdin and would report the
// mistake against a line number rather than a package.
//
// Checked before the tool is, so it holds on a machine with no dpkg —
// which is the machine somebody writes the state on.
func TestASelectionMustBeOneDpkgTakes(t *testing.T) {
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{}
	_, err := dpkgSetSelections(c, value.MapOf("nginx", "pinned"))
	if err == nil {
		t.Fatal("a selection dpkg does not have was accepted")
	}
	if !strings.Contains(err.Error(), "hold") {
		t.Errorf("the refusal does not name the selections dpkg takes: %v", err)
	}
}

// changingRunner answers the same command differently each time, which
// is what reading a value, changing it, and reading it back needs. The
// RecordingRunner maps a command to one result, so it cannot say "and
// then it was different".
type changingRunner struct {
	answers map[string][]exec.Result
	ran     []exec.Command
}

func (r *changingRunner) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	r.ran = append(r.ran, cmd)
	key := strings.Join(cmd.Argv, " ")
	queued := r.answers[key]
	if len(queued) == 0 {
		return exec.Result{}, nil
	}
	next := queued[0]
	// The last answer stands for every call after it, so a command
	// asked once more does not fall off the end.
	if len(queued) > 1 {
		r.answers[key] = queued[1:]
	}
	return next, nil
}

// Setting a selection reports only what moved.
//
// This one goes through the module function, so it needs a dpkg on the
// machine: holding a package is the only thing here that changes
// anything, and what it reports is worth testing against the real
// sequence of calls rather than against the diff alone. The diff itself
// is covered without one, below.
func TestSetSelectionsReportsOnlyWhatMoved(t *testing.T) {
	c := newCtx(false)
	if c.Which("dpkg") == "" {
		t.Skip("no dpkg on this node; the diff this produces is covered by TestOnlyAMovedSelectionIsAChange")
	}
	runner := &changingRunner{answers: map[string][]exec.Result{
		"dpkg --get-selections": {
			{Stdout: "nginx\tinstall\nheld\thold\n"},
			{Stdout: "nginx\thold\nheld\thold\n"},
		},
		"dpkg --set-selections": {{}},
	}}
	c.Runner = runner

	got, err := dpkgSetSelections(c, value.MapOf("nginx", "hold", "held", "hold"))
	if err != nil {
		t.Fatal(err)
	}
	// `held` was already held, so it is not a change.
	if _, ok := got.Get("held"); ok {
		t.Errorf("a package that did not move was reported: %v", got.StringKeys())
	}
	raw, ok := got.Get("nginx")
	if !ok {
		t.Fatalf("the package that moved was not reported: %v", got.StringKeys())
	}
	pair := raw.(*value.Map)
	if old, _ := pair.Get("old"); old != "install" {
		t.Errorf("old = %v, want install", old)
	}
	if now, _ := pair.Get("new"); now != "hold" {
		t.Errorf("new = %v, want hold", now)
	}
}

// The diff is the part of set_selections worth having, and it is pure.
func TestOnlyAMovedSelectionIsAChange(t *testing.T) {
	before := value.MapOf("nginx", "install", "held", "hold")
	after := value.MapOf("nginx", "hold", "held", "hold")
	got := dpkgSelectionDiff(before, after, []string{"held", "nginx"})
	if got.Len() != 1 {
		t.Fatalf("%d changes, want 1: %v", got.Len(), got.StringKeys())
	}
	if _, ok := got.Get("nginx"); !ok {
		t.Errorf("the moved package is missing: %v", got.StringKeys())
	}
}

// Where dpkg is not installed, every function says so by name.
//
// The message names the tool rather than the platform: a RHEL node is
// linux, which is what the signature restricts to, and telling an
// operator there that this "does not run on linux" would be false on the
// machine in front of them.
func TestDpkgRefusesWhereItIsNotInstalled(t *testing.T) {
	c := newCtx(false)
	if c.Which("dpkg-query") != "" {
		t.Skip("this node has dpkg; the refusal cannot be observed here")
	}
	_, err := dpkgListPkgs(c, nil)
	if err == nil {
		t.Fatal("dpkg.list_pkgs answered on a node with no dpkg")
	}
	if !strings.Contains(err.Error(), "dpkg-query") {
		t.Errorf("the refusal does not name the tool: %v", err)
	}
	if !strings.Contains(err.Error(), "Debian") {
		t.Errorf("the refusal does not say what kind of system this needs: %v", err)
	}
}
