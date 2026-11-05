package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every setting the programs read is a setting the table declares.
//
// The loader says a key it does not know "is not recognised and was
// ignored", and the table is also what the generated documentation lists, so
// a setting that is read and not declared is one an operator is told does
// nothing while it does something, and one nobody can find in the reference.
// Six were: `hub_timeout`, `beacon_tick`, `gitfs_timeout`, `s3_timeout`,
// `output`, and `require_signed_extensions`, which the node's `doctor` read
// in place of the setting the node enforces. The audit that existed ran the
// other way, declared and never read. DIVERGENCE 5.545.
//
// There are no exceptions. `id` was one: the hub's event returner read it,
// and the Salt shim renames `id` to `node_id` on load, so it was always
// empty. The read is gone (DIVERGENCE 5.591).
func TestEverySettingTheProgramsReadIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, k := range Keys {
		declared[k.Name] = true
	}
	read := regexp.MustCompile(`\b(?:cfg|config)\.(?:String|Bool|Int|Int64|Duration|StringSlice|Map|Roots|PathUnderRoot)\("([a-z][a-z0-9_]*)"`)
	known := map[string]bool{}

	var missing []string
	for _, dir := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for _, m := range read.FindAllStringSubmatch(string(src), -1) {
				if !declared[m[1]] && !known[m[1]] {
					missing = append(missing, m[1]+" ("+filepath.ToSlash(path)+")")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(missing) > 0 {
		t.Errorf("settings read and not declared in Keys:\n  %s", strings.Join(missing, "\n  "))
	}
}
