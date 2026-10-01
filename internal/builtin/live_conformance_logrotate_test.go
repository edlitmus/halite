package builtin

import (
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `logrotate.set`, through SPEC 11.6's harness, once for a global
// directive and once inside a stanza.
//
// Neither case goes near the node's own configuration: the state is
// given a `conf_file` of its own in a directory of its own, which Setup
// rewrites before every phase, and that file includes a drop-in
// directory of its own so that the stanza case edits an included file
// and goes through the whole-configuration check a drop-in gets. The
// logs it names do not exist, which logrotate -d reports as a runtime
// error and the module rightly does not count against an edit.

const logrotateConformanceDir = "/tmp/halitecf-logrotate"

func logrotateConformanceCases() []liveCase {
	conf := filepath.Join(logrotateConformanceDir, "logrotate.conf")
	incDir := filepath.Join(logrotateConformanceDir, "logrotate.d")
	dropIn := filepath.Join(incDir, "halitecf")
	mainText := "# halite conformance\nweekly\nrotate 4\ninclude " + incDir + "\n"
	dropText := logrotateConformanceDir + "/logs/a.log {\n\tmissingok\n\trotate 2\n}\n"
	setup := func() error {
		if err := os.MkdirAll(incDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(conf, []byte(mainText), 0o644); err != nil {
			return err
		}
		return os.WriteFile(dropIn, []byte(dropText), 0o644)
	}
	probe := func() (string, error) {
		a, err := os.ReadFile(conf)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(dropIn)
		return string(a) + "\x00" + string(b), err
	}
	cleanup := func() { _ = os.RemoveAll(logrotateConformanceDir) }
	return []liveCase{
		{
			label:     "logrotate.set (global)",
			platforms: logrotatePlatforms,
			needs:     []string{"logrotate"},
			Conformance: states.Conformance{
				Name:    "logrotate.set",
				Args:    value.MapOf("name", "rotate-globally", "key", "rotate", "value", int64(7), "conf_file", conf),
				Setup:   setup,
				Probe:   probe,
				Cleanup: cleanup,
			},
		},
		{
			label:     "logrotate.set (stanza, in an included file)",
			platforms: logrotatePlatforms,
			needs:     []string{"logrotate"},
			Conformance: states.Conformance{
				Name: "logrotate.set",
				Args: value.MapOf("name", "compress-a", "key", logrotateConformanceDir+"/logs/a.log",
					"value", "compress", "setting", true, "conf_file", conf),
				Setup:   setup,
				Probe:   probe,
				Cleanup: cleanup,
			},
		},
	}
}
