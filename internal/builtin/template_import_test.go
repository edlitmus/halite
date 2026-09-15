package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// importingFileServer serves a directory and can resolve the imports
// inside what it serves, which is what the real file servers do:
// `fileserver.Fetcher` embeds `Roots` and `Remote` has its own.
type importingFileServer struct{ staticFileServer }

func (s importingFileServer) Templates(string) template.Loader {
	return importLoader{root: s.root}
}

type importLoader struct{ root string }

func (l importLoader) Load(name string) (string, string, error) {
	rel := strings.TrimPrefix(strings.TrimPrefix(name, "salt://"), "/")
	b, err := os.ReadFile(filepath.Join(l.root, filepath.FromSlash(rel)))
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", name, template.ErrNotFound)
	}
	return string(b), name, nil
}

// A rendered file source may import another template, and until this was
// wired it could not: the SLS compiler configured a loader and the
// file-source path did not, so `{% import "shared/salt/map.jinja" %}`
// inside a managed file failed with "no template loader is configured".
//
// That import is one of the most common things in a Salt tree. The
// estate this was found on does it in two separate files -- a salt
// configuration file and a shell script -- both failing at runtime with
// the tree otherwise compiling clean.
func TestFileSourceTemplateCanImport(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(source, "shared", "salt"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shared/salt/map.jinja", `{% set settings = {'port': 4506, 'user': 'salt'} %}`)
	write("conf", "{% import 'shared/salt/map.jinja' as m %}\nport = {{ m.settings['port'] }}\n")

	r := New()
	c := newRunningCtx()
	c.Files = importingFileServer{staticFileServer{root: source}}

	target := filepath.Join(dir, "out.conf")
	res, err := r.States.Call(c, "file.managed", value.MapOf(
		"name", target, "source", "salt://conf", "template", "jinja"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("the import did not resolve: %s", res.Comment)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "port = 4506") {
		t.Errorf("the imported value is not in the file: %q", body)
	}
}

// A file server that cannot resolve templates still serves files, and an
// import through it says so at the line that wrote it rather than
// rendering an empty value into the file.
func TestFileSourceWithoutALoaderSaysSo(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "conf"),
		[]byte("{% import 'map.jinja' as m %}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New()
	c := newRunningCtx()
	// The plain one, with no Templates method.
	c.Files = staticFileServer{root: source}

	res, err := r.States.Call(c, "file.managed", value.MapOf(
		"name", filepath.Join(dir, "out.conf"), "source", "salt://conf", "template", "jinja"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("an unresolvable import rendered successfully")
	}
	if !strings.Contains(res.Comment, "loader") && !strings.Contains(res.Comment, "map.jinja") {
		t.Errorf("the failure names neither the loader nor the import: %q", res.Comment)
	}
}

// file.recurse renders every file it copies, so it needs the same loader.
func TestFileRecurseTemplateCanImport(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(filepath.Join(source, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "shared", "map.jinja"),
		[]byte(`{% set settings = {'port': 4505} %}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "conf"),
		[]byte("{% import 'shared/map.jinja' as m %}\nport = {{ m.settings['port'] }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New()
	c := newRunningCtx()
	c.Files = importingFileServer{staticFileServer{root: source}}

	res, err := r.States.Call(c, "file.recurse", value.MapOf(
		"name", dest, "source", "salt://", "template", "jinja"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("the import did not resolve: %s", res.Comment)
	}
	body, err := os.ReadFile(filepath.Join(dest, "conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "port = 4505") {
		t.Errorf("the imported value is not in the copied file: %q", body)
	}
}
