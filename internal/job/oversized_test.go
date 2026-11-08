package job

import (
	"encoding/json"
	"testing"
)

// The stand-in for an oversized return is a failure whatever the job
// reported, keeps a failing job's own code, and says what the job
// reported underneath.
func TestOversizedIsAFailureThatKeepsTheJobsOwnAnswer(t *testing.T) {
	for _, c := range []struct {
		success     bool
		code, wantC int
	}{
		{true, 0, 1},
		{false, 2, 2},
	} {
		got := (&Return{JID: "J", NodeID: "n", Fun: "f", Success: c.success, RetCode: c.code,
			Out: "highstate", Return: json.RawMessage(`"big"`)}).Oversized(100, 10)
		if got.Success || got.RetCode != c.wantC {
			t.Errorf("job success=%v retcode=%d: stand-in success=%v retcode=%d, want false/%d",
				c.success, c.code, got.Success, got.RetCode, c.wantC)
		}
		if got.Out != "" {
			t.Errorf("the stand-in carries the original's Out, which may be what made it too large")
		}
		var body map[string]any
		if err := json.Unmarshal(got.Return, &body); err != nil {
			t.Fatal(err)
		}
		if body["job_success"] != c.success || body["job_retcode"] != float64(c.code) ||
			body["bytes"] != float64(100) || body["limit"] != float64(10) {
			t.Errorf("the stand-in's body = %v", body)
		}
	}
}
