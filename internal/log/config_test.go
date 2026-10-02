package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// fromFile loads a configuration for role out of text and builds the
// logger the service would, with over as its command line.
func fromFile(t *testing.T, role config.Role, text string, over Overrides) (*Logger, error) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, role.FileName()), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(role, config.LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return FromConfig(cfg, over, role.String(), nil)
}

// All three services' configurations reach the logger by the same keys.
// The defect this replaces was one service reading a key the other two
// did not, so the check is per role rather than once: the configuration
// loader is role-aware, and a key one role's loader dropped would make
// this function correct for two services and wrong for the third.
//
// The file sink is what is read, because it is the half the api lost and
// because it is the only one this test can capture without replacing
// os.Stderr under a parallel test binary.
func TestFromConfigReadsTheSameSettingsForEveryService(t *testing.T) {
	for _, role := range []config.Role{config.Hub, config.Node, config.API} {
		t.Run(role.String(), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "service.log")
			l, err := fromFile(t, role,
				"log_level: warn\nlog_format: console\nlog_file: "+file+"\n", Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			l.Info("below the configured level")
			l.Warn("at the configured level", "k", "v")
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("log_file was not created: %v", err)
			}
			got := strings.TrimSpace(string(data))
			if got != "warn: at the configured level k=v" {
				t.Errorf("log file = %q, want the one warn line in console form", got)
			}
		})
	}
}

// The command line wins over the file, which is what `--log-fmt` and
// `--log-level` are documented to do, and the component is attached to
// every record so lines from three services in one aggregator can be told
// apart.
func TestFromConfigFlagsOverrideTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "api.log")
	l, err := fromFile(t, config.API,
		"log_level: error\nlog_format: console\nlog_file: "+file+"\n",
		Overrides{Format: "json", Level: "info"})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hello")
	_ = l.Close()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("--log-fmt json did not win over the file: %q (%v)", data, err)
	}
	if rec["msg"] != "hello" || rec["component"] != "api" {
		t.Errorf("record = %v", rec)
	}
}

// A level or format that is not one is refused, by name, rather than
// read as a default.
func TestFromConfigRefusesWhatIsNotALevelOrFormat(t *testing.T) {
	if _, err := fromFile(t, config.API, "log_format: yaml\n", Overrides{}); err == nil ||
		!strings.Contains(err.Error(), `log_format "yaml" is not a format`) {
		t.Errorf("log_format: yaml = %v", err)
	}
	if _, err := fromFile(t, config.Hub, "log_level: loud\n", Overrides{}); err == nil ||
		!strings.Contains(err.Error(), `log_level "loud" is not a level`) {
		t.Errorf("log_level: loud = %v", err)
	}
}
