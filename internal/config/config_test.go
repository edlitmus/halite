package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// daemonFlags is a flag set shaped like a daemon's: a string, a bool, a
// repeatable value, and one of the settings the environment outranks.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func daemonFlags() (*flag.FlagSet, *string, *bool, *stringList, *string) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(os.NewFile(0, os.DevNull))
	addr := fs.String("addr", ":5617", "")
	auto := fs.Bool("auto-accept", false, "")
	var returners stringList
	fs.Var(&returners, "returner", "")
	root := fs.String("root", "", "")
	return fs, addr, auto, &returners, root
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "daemon.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingFileIsNotAnError(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatalf("running on flags alone has to keep working: %v", err)
	}
	if len(cfg.Keys()) != 0 {
		t.Fatalf("want no settings, got %v", cfg.Keys())
	}
	if cfg, err := Load(""); err != nil || len(cfg.Keys()) != 0 {
		t.Fatal("no path is no configuration")
	}
}

func TestSettingsReachTheFlags(t *testing.T) {
	path := writeConfig(t, "addr: 127.0.0.1:5618\nauto-accept: \"true\"\nroot: /srv/states\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fs, addr, auto, _, root := daemonFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(fs); err != nil {
		t.Fatal(err)
	}
	if *addr != "127.0.0.1:5618" || !*auto || *root != "/srv/states" {
		t.Fatalf("want the file's values, got addr=%q auto=%v root=%q", *addr, *auto, *root)
	}
}

func TestARepeatableFlagTakesAList(t *testing.T) {
	path := writeConfig(t, "returner:\n  - file:/var/log/results.ndjson\n  - webhook:https://example.com/h\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fs, _, _, returners, _ := daemonFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(fs); err != nil {
		t.Fatal(err)
	}
	if len(*returners) != 2 || (*returners)[1] != "webhook:https://example.com/h" {
		t.Fatalf("want both sinks, got %v", *returners)
	}
}

func TestAFlagOnTheCommandLineWins(t *testing.T) {
	path := writeConfig(t, "addr: 127.0.0.1:5618\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fs, addr, _, _, _ := daemonFlags()
	if err := fs.Parse([]string{"-addr", "127.0.0.1:9999"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(fs); err != nil {
		t.Fatal(err)
	}
	if *addr != "127.0.0.1:9999" {
		t.Fatalf("the command line is the most specific thing typed; got %q", *addr)
	}
}

func TestTheEnvironmentOutranksTheFile(t *testing.T) {
	t.Setenv("HALITE_ROOT", "/from/the/environment")
	path := writeConfig(t, "root: /from/the/file\naddr: 127.0.0.1:5618\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fs, addr, _, _, root := daemonFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(fs); err != nil {
		t.Fatal(err)
	}
	// The flag is left empty so that the caller's own environment fallback
	// resolves it — the file must not stand in front of the environment.
	if *root != "" {
		t.Fatalf("the file should not have set root while $HALITE_ROOT is set; got %q", *root)
	}
	if *addr != "127.0.0.1:5618" {
		t.Fatalf("a setting with no environment variable still applies; got %q", *addr)
	}
}

func TestAnUnknownSettingIsAnError(t *testing.T) {
	path := writeConfig(t, "adr: \":5617\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fs, _, _, _, _ := daemonFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	err = cfg.Apply(fs)
	if err == nil {
		t.Fatal("a typo that quietly did nothing would be worse than a daemon refusing to start")
	}
	if !strings.Contains(err.Error(), "adr") {
		t.Fatalf("the error should name the setting: %v", err)
	}
}

func TestAMalformedFileIsReported(t *testing.T) {
	if _, err := Load(writeConfig(t, "addr: [\":5617\", \":5618\"]\n")); err == nil {
		t.Fatal("a flow collection is not the YAML subset; it should be reported")
	}
	if _, err := Load(writeConfig(t, "- addr\n")); err == nil {
		t.Fatal("a config file is a mapping, and a list should say so")
	}
	if _, err := Load(writeConfig(t, "addr:\n  nested: value\n")); err == nil {
		t.Fatal("a setting is a value or a list of values, not a mapping")
	}
}

func TestDefaultPathFollowsTheConfigDirectory(t *testing.T) {
	if got := DefaultPath("master", "/usr/local/etc/halite"); got != "/usr/local/etc/halite/master.conf" {
		t.Fatalf("unexpected path %q", got)
	}
}
