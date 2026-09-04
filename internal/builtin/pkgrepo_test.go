package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A repository written by the apt provider is read back as what was
// declared, which is the property a state converging depends on.
//
// Against a redirected root rather than /etc, so this runs on a machine
// with no apt — which is every machine this project is developed on.
func TestAnAptRepositoryRoundTrips(t *testing.T) {
	p := aptRepoProvider{root: t.TempDir()}
	c := newCtx(false)

	config := value.MapOf(
		"type", "deb",
		"baseurl", "https://apt.example.com/ubuntu",
		"dist", "noble",
		"comps", []any{"main", "universe"},
		"architectures", []any{"amd64"},
		"signedby", "/usr/share/keyrings/example.gpg",
		"enabled", true,
	)

	changed, err := p.Set(c, "example", config)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("writing a repository that was not there reported no change")
	}

	// Writing the same thing again is not a change, which is what stops a
	// state reporting one on every run.
	changed, err = p.Set(c, "example", config)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("writing the same repository twice reported a change")
	}

	got, err := p.Get(c, "example")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("the repository was not read back")
	}
	if matches, err := p.Matches(c, "example", config); err != nil || !matches {
		t.Errorf("the declaration does not match what was written (err=%v); read back %v", err, got)
	}

	// And the file is a source list apt would accept.
	raw, err := os.ReadFile(filepath.Join(p.dir(), "example.list"))
	if err != nil {
		t.Fatal(err)
	}
	line := sourceLineOf(string(raw))
	for _, want := range []string{
		"deb ", "arch=amd64", "signed-by=/usr/share/keyrings/example.gpg",
		"https://apt.example.com/ubuntu", "noble", "main", "universe",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the source line does not carry %q:\n%s", want, line)
		}
	}
}

// A disabled repository is a commented line, which is how apt has always
// spelled it and what an operator disabling one by hand will have done.
func TestADisabledAptRepositoryIsCommentedAndReadsBackDisabled(t *testing.T) {
	p := aptRepoProvider{root: t.TempDir()}
	c := newCtx(false)

	config := value.MapOf("type", "deb", "baseurl", "https://x.example/ubuntu",
		"dist", "noble", "comps", []any{"main"}, "enabled", false)
	if _, err := p.Set(c, "off", config); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(p.dir(), "off.list"))
	if !strings.Contains(string(raw), "# deb ") {
		t.Errorf("a disabled repository is not commented out:\n%s", raw)
	}
	got, err := p.Get(c, "off")
	if err != nil {
		t.Fatal(err)
	}
	if enabled, _ := got.Get("enabled"); enabled != false {
		t.Errorf("a commented repository read back as enabled: %v", got)
	}
	if url, _ := got.Get("baseurl"); url != "https://x.example/ubuntu" {
		t.Errorf("the commented line was not parsed: %v", got)
	}
}

// An apt repository with no dist is refused with the reason, because a
// source line without one is not a source line and apt's own error for
// it names no field.
func TestAnAptRepositoryNeedsADist(t *testing.T) {
	p := aptRepoProvider{root: t.TempDir()}
	_, err := p.Set(newCtx(false), "x", value.MapOf("baseurl", "https://x.example/ubuntu"))
	if err == nil {
		t.Fatal("a source with no dist was written")
	}
	if !strings.Contains(err.Error(), "dist") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
	if !strings.Contains(err.Error(), "noble") {
		t.Errorf("the refusal does not show what one looks like: %v", err)
	}
}

// Removing a repository removes its file, and removing one that is not
// there is not an error.
func TestRemovingAnAptRepository(t *testing.T) {
	p := aptRepoProvider{root: t.TempDir()}
	c := newCtx(false)

	if _, err := p.Set(c, "gone", value.MapOf(
		"baseurl", "https://x.example/ubuntu", "dist", "noble")); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(c, "gone"); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(c, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("the repository survived removal: %v", got)
	}
	if err := p.Delete(c, "gone"); err != nil {
		t.Errorf("removing a repository that is not there reported %v", err)
	}
}

// The yum provider writes an INI a `.repo` file is, and reads it back.
func TestAYumRepositoryRoundTrips(t *testing.T) {
	p := yumRepoProvider{root: t.TempDir()}
	c := newCtx(false)

	config := value.MapOf(
		"humanname", "Example 9 - x86_64",
		"baseurl", "https://rpm.example.com/9/x86_64",
		"gpgkey", "https://rpm.example.com/RPM-GPG-KEY-example",
		"gpgcheck", true,
		"enabled", true,
	)
	if _, err := p.Set(c, "example", config); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(c, "example")
	if err != nil {
		t.Fatal(err)
	}
	if matches, err := p.Matches(c, "example", config); err != nil || !matches {
		t.Errorf("the declaration does not match what was written (err=%v); read back %v", err, got)
	}

	raw, _ := os.ReadFile(filepath.Join(p.dir(), "example.repo"))
	for _, want := range []string{"[example]", "name=Example 9 - x86_64", "enabled=1", "gpgcheck=1"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the repo file does not carry %q:\n%s", want, raw)
		}
	}

	// A second write of the same thing is not a change.
	changed, err := p.Set(c, "example", config)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("writing the same repository twice reported a change")
	}
}

// RHEL's own tooling refuses a repository with no name, so one is
// supplied rather than an invalid file being written.
func TestAYumRepositoryAlwaysHasAName(t *testing.T) {
	p := yumRepoProvider{root: t.TempDir()}
	if _, err := p.Set(newCtx(false), "short", value.MapOf(
		"baseurl", "https://rpm.example.com/9")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(p.dir(), "short.repo"))
	if !strings.Contains(string(raw), "name=short") {
		t.Errorf("no name was written:\n%s", raw)
	}
}

// A Salt tree names a Debian repository by its whole source line. It has
// to be taken apart, and the file named something an operator can find.
func TestASaltStyleSourceLineIsTakenApart(t *testing.T) {
	name, config, err := repoFromArgs(value.MapOf(
		"name", "deb [arch=amd64 signed-by=/usr/share/keyrings/e.gpg] https://apt.example.com/ubuntu noble main universe"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "apt.example.com" {
		t.Errorf("name = %q; the file should be named after the host", name)
	}
	if url, _ := config.Get("baseurl"); url != "https://apt.example.com/ubuntu" {
		t.Errorf("baseurl = %v", url)
	}
	if dist, _ := config.Get("dist"); dist != "noble" {
		t.Errorf("dist = %v", dist)
	}
	comps := repoList(config, "comps")
	if len(comps) != 2 || comps[0] != "main" || comps[1] != "universe" {
		t.Errorf("comps = %v", comps)
	}
	if arches := repoList(config, "architectures"); len(arches) != 1 || arches[0] != "amd64" {
		t.Errorf("architectures = %v", arches)
	}
	if signed, _ := config.Get("signedby"); signed != "/usr/share/keyrings/e.gpg" {
		t.Errorf("signedby = %v", signed)
	}

	// A short name is left alone.
	name, _, err = repoFromArgs(value.MapOf("name", "example", "baseurl", "https://x/y", "dist", "noble"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "example" {
		t.Errorf("a short name became %q", name)
	}

	// A field stated explicitly beats the one parsed out of the line: a
	// state that says both means the explicit one.
	_, config, err = repoFromArgs(value.MapOf(
		"name", "deb https://apt.example.com/ubuntu noble main",
		"dist", "jammy"))
	if err != nil {
		t.Fatal(err)
	}
	if dist, _ := config.Get("dist"); dist != "jammy" {
		t.Errorf("the explicit dist was overridden by the parsed one: %v", dist)
	}
}

// A node with no package manager this build can manage says so, naming
// the providers, rather than failing with something about a missing file.
func TestANodeWithNoManagerSaysWhichProvidersExist(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	_, err := pickRepoProvider(c)
	if err == nil {
		t.Fatal("a provider was chosen on a node with no package manager")
	}
	for _, want := range []string{"aptpkg", "yumpkg", "chocolatey"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
}

// sourceLineOf returns the first line of a rendered source list that is
// not a comment.
func sourceLineOf(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return trimmed
		}
	}
	return ""
}

// The state converges: it configures a repository, then reports nothing
// on the second run.
//
// Against the apt provider with a redirected root, so it runs anywhere.
// The registered provider list is not touched; what is exercised is the
// state's own logic against a provider that behaves like a real one.
func TestPkgrepoManagedConvergesAgainstAProvider(t *testing.T) {
	p := aptRepoProvider{root: t.TempDir()}
	c := newCtx(false)
	// The declaration a state builds carries `gpgcheck` on every
	// platform, and apt has no such concept. That is the case this test
	// exists for: a comparison made outside the provider found a
	// difference in it on every run, and the state never converged.
	declared := value.MapOf(
		"type", "deb", "baseurl", "https://apt.example.com/ubuntu",
		"dist", "noble", "comps", []any{"main"}, "enabled", true, "gpgcheck", true)

	matches, err := p.Matches(c, "example", declared)
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Fatal("an unconfigured repository matched a declaration")
	}

	if _, err := p.Set(c, "example", declared); err != nil {
		t.Fatal(err)
	}
	matches, err = p.Matches(c, "example", declared)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		raw, _ := os.ReadFile(filepath.Join(p.dir(), "example.list"))
		t.Errorf("after configuring, the state would still report a change; the file holds:\n%s", raw)
	}

	// A declaration that differs is a change.
	tightened := value.MapOf(
		"type", "deb", "baseurl", "https://apt.example.com/ubuntu",
		"dist", "noble", "comps", []any{"main", "universe"})
	matches, err = p.Matches(c, "example", tightened)
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Error("adding a component reported no change")
	}
}
