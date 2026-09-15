package builtin

import (
	"strings"
	"testing"
)

// A test-mode result must not read as though it acted. This said "The
// service salt-minion was started" for a service it had not touched, // lexicon:allow — a real service name
// because one function wrote the comment for both modes and wrote it in
// the past tense. It was read that way on a host where starting that
// service purges the compilers -- the reading cost a check of
// `systemctl is-active` to disprove, and a dry run should not need one.
func TestServiceTestModeSaysWouldNotWas(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		runState, boot, enabled, want bool
		would                         bool
		expect                        string
	}{
		{"test mode, starting", true, false, true, true, true, "would be started"},
		{"real, starting", true, false, true, true, false, "was started"},
		{"test mode, stopping", true, false, false, false, true, "would be stopped"},
		{"real, stopping", true, false, false, false, false, "was stopped"},
		{"test mode, boot only", false, true, true, true, true, "would be enabled at boot"},
		{"real, boot only", false, true, true, true, false, "was enabled at boot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := describeServiceChange("salt-minion", tc.runState, tc.boot, tc.enabled, tc.want, tc.would) // lexicon:allow — a real service name
			if !strings.Contains(got, tc.expect) {
				t.Errorf("describeServiceChange = %q, want it to contain %q", got, tc.expect)
			}
			if tc.would && strings.Contains(got, " was ") {
				t.Errorf("a test-mode comment reads as though it acted: %q", got)
			}
		})
	}
}
