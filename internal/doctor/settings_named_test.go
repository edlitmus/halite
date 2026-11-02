package doctor

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// A remedy that names a setting names one that exists. `doctor` told an
// operator to set `require_signed_extensions`, `extension_trust`,
// `queue_depth`, `workers`, `retention` and `max_bytes`, none of which is a
// setting. A remedy is the line somebody acts on at the worst moment.
// DIVERGENCE 5.545.
func TestARemedyNamesSettingsThatExist(t *testing.T) {
	declared := map[string]bool{}
	for _, k := range config.Keys {
		declared[k.Name] = true
	}
	// Words that are not settings, in backticks: a metric, a grain, a
	// struct field prefix, and the bare words doctor's comments use.
	notSettings := map[string]bool{"fips_mode": true, "f_": true}
	ident := regexp.MustCompile("`([a-z][a-z0-9_]*)(?:: [a-z]+)?`")

	for _, file := range []string{"checks.go", "fips.go", "doctor.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ident.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if !strings.Contains(name, "_") || notSettings[name] || strings.HasPrefix(name, "halite_") {
				continue
			}
			if !declared[name] {
				t.Errorf("%s names `%s`, which is not a setting", file, name)
			}
		}
	}
}
