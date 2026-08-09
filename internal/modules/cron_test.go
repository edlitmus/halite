package modules

import "testing"

func TestCronEntry(t *testing.T) {
	marker, entry := cronEntry("converge", map[string]any{
		"name":   "halite apply /etc/halite/base.sls",
		"minute": "*/30",
	})
	if marker != "# halite: converge" {
		t.Errorf("marker = %q", marker)
	}
	if entry != "*/30 * * * * halite apply /etc/halite/base.sls" {
		t.Errorf("entry = %q", entry)
	}
}
