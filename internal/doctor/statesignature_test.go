package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTheStateSignatureCheckSaysWhatTheNextRunStartsFrom(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	revision := func(age time.Duration) []StateRevision {
		return []StateRevision{{Env: "base", Issued: now.Add(-age), Signer: "tree-ci"}}
	}
	cases := []struct {
		name  string
		state StateSignatureState
		want  Status
		says  string
	}{
		{"off", StateSignatureState{Mode: "false"}, Skip, "trusts the hub's digest"},
		{"no keys", StateSignatureState{Mode: "true", Now: now}, Fail, "state_signer_keys is empty"},
		{"no keys under warn", StateSignatureState{Mode: "warn", Now: now}, Warn, "state_signer_keys is empty"},
		{"unreadable records", StateSignatureState{Mode: "true", Keys: 1, RecordErr: errors.New("denied")}, Fail, "cannot be read"},
		{"fresh", StateSignatureState{Mode: "true", Keys: 1, Local: true, MaxAge: 720 * time.Hour, Now: now,
			Revisions: revision(time.Hour)}, Pass, "Pillar is not covered"},
		{"older than the bound", StateSignatureState{Mode: "true", Keys: 1, MaxAge: 720 * time.Hour, Now: now,
			Revisions: revision(721 * time.Hour)}, Fail, "base issued"},
		{"warn with a revision", StateSignatureState{Mode: "warn", Keys: 1, Now: now, Revisions: revision(time.Hour)},
			Warn, "by tree-ci"},
	}
	for _, c := range cases {
		res := StateSignatures(c.state).Run(context.Background())
		if res.Status != c.want || !strings.Contains(res.Detail+res.Remedy, c.says) {
			t.Errorf("%s: %s %q / %q, want %s saying %q", c.name, res.Status, res.Detail, res.Remedy, c.want, c.says)
		}
	}
}

func TestTheHubsStateSignatureCheckNamesAStrayFile(t *testing.T) {
	res := ServedTreeSignatures(2, []string{"environment base: /srv/x/_halite holds notes.txt"}, false).Run(context.Background())
	if res.Status != Fail || !strings.Contains(res.Detail, "notes.txt") {
		t.Fatalf("%s %q", res.Status, res.Detail)
	}
	if res := ServedTreeSignatures(2, nil, true).Run(context.Background()); res.Status != Fail {
		t.Fatalf("a hidden manifest: %s", res.Status)
	}
	if res := ServedTreeSignatures(2, nil, false).Run(context.Background()); res.Status != Pass {
		t.Fatalf("clean: %s %q", res.Status, res.Detail)
	}
}
