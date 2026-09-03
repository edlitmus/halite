package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

func serializeTo(t *testing.T, path string, args ...any) (comment string, ok bool) {
	t.Helper()
	res, err := fileSerialize(&exec.Context{}, value.MapOf(append([]any{"name", path}, args...)...))
	if err != nil {
		t.Fatal(err)
	}
	return res.Comment, res.Succeeded()
}

// `file.serialize` writes a data structure as JSON or YAML. Two
// references in a real estate's tree: it is how a tree turns structured
// data into a configuration file without rendering it through Jinja to
// get the structure back.
func TestFileSerializeWritesJSONAndYAML(t *testing.T) {
	dir := t.TempDir()

	jsonPath := filepath.Join(dir, "a.json")
	if _, ok := serializeTo(t, jsonPath, "serializer", "json",
		"dataset", value.MapOf("listen", int64(8080))); !ok {
		t.Fatal("the json write failed")
	}
	body, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"listen": 8080`) {
		t.Errorf("json = %q", body)
	}
	if !strings.HasSuffix(string(body), "\n") {
		t.Error("the file does not end with a newline, as every other file this build writes does")
	}

	yamlPath := filepath.Join(dir, "a.yaml")
	if _, ok := serializeTo(t, yamlPath, "serializer", "yaml",
		"dataset", value.MapOf("listen", int64(9090))); !ok {
		t.Fatal("the yaml write failed")
	}
	body, err = os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != "listen: 9090" {
		t.Errorf("yaml = %q", body)
	}
}

func TestFileSerializeConverges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	data := value.MapOf("listen", int64(8080))

	if _, ok := serializeTo(t, path, "serializer", "json", "dataset", data); !ok {
		t.Fatal("the first write failed")
	}
	res, err := fileSerialize(&exec.Context{},
		value.MapOf("name", path, "serializer", "json", "dataset", data))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %s", res.Comment)
	}
}

// The format is not guessed from the file name: a `.conf` that has
// always held YAML would start holding JSON the day somebody renamed it.
func TestFileSerializeRequiresASerializer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	comment, ok := serializeTo(t, path, "dataset", value.MapOf("a", int64(1)))
	if ok {
		t.Fatal("a missing serializer should be refused")
	}
	if !strings.Contains(comment, "json or yaml") {
		t.Errorf("the refusal does not name the choices: %s", comment)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused state wrote the file anyway")
	}
}

func TestFileSerializeRefusesAnUnknownSerializer(t *testing.T) {
	if _, ok := serializeTo(t, filepath.Join(t.TempDir(), "a.toml"),
		"serializer", "toml", "dataset", value.MapOf("a", int64(1))); ok {
		t.Error("a serializer this build does not have should be refused")
	}
}

// `merge_if_exists` keeps what the file already holds. The dataset wins
// where they name the same key.
func TestFileSerializeMergesOverWhatIsThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	if err := os.WriteFile(path, []byte(`{"keepme": true, "listen": 1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := serializeTo(t, path, "serializer", "json", "merge_if_exists", true,
		"dataset", value.MapOf("listen", int64(8080))); !ok {
		t.Fatal("the merge failed")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "keepme") {
		t.Error("merging discarded what the file already held")
	}
	if !strings.Contains(string(body), "8080") {
		t.Error("the dataset did not win for the key it names")
	}
}

// A file that does not parse is an error rather than something to
// overwrite: merging asks to keep what is there, and the one case where
// that cannot be honoured is the one where discarding it would be worst.
func TestFileSerializeRefusesToMergeIntoUnparseable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	if err := os.WriteFile(path, []byte("{not json at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	comment, ok := serializeTo(t, path, "serializer", "json", "merge_if_exists", true,
		"dataset", value.MapOf("listen", int64(8080)))
	if ok {
		t.Fatal("merging into a file that does not parse should be refused")
	}
	if !strings.Contains(comment, "merge_if_exists") {
		t.Errorf("the refusal does not say why: %s", comment)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "not json") {
		t.Error("the unparseable file was overwritten, which is what the refusal exists to prevent")
	}
}

// Two sources for one file is a question about which wins, so it is
// refused rather than answered.
func TestFileSerializeRefusesBothSources(t *testing.T) {
	if _, ok := serializeTo(t, filepath.Join(t.TempDir(), "a.json"),
		"serializer", "json", "dataset", value.MapOf("a", int64(1)),
		"dataset_pillar", "some:key"); ok {
		t.Error("dataset and dataset_pillar together should be refused")
	}
}

func TestFileSerializeInTestModeWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	res, err := fileSerialize(&exec.Context{Test: true},
		value.MapOf("name", path, "serializer", "json", "dataset", value.MapOf("a", int64(1))))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Error("test mode should leave the result undecided")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("test mode wrote the file")
	}
}

// `create: false` updates a file that is there and does not make one
// that is not.
func TestFileSerializeHonoursCreateFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.json")
	if _, ok := serializeTo(t, path, "serializer", "json", "create", false,
		"dataset", value.MapOf("a", int64(1))); !ok {
		t.Fatal("it should succeed and do nothing")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the file was created with `create: false`")
	}
}
