package modules

import (
	"path/filepath"
	"testing"
)

func TestLineEnsure(t *testing.T) {
	cases := []struct {
		name    string
		start   string
		args    map[string]any
		want    string
		changed bool
	}{
		{
			name:    "replaces the matching line",
			start:   "Port 22\nPermitRootLogin yes\n",
			args:    map[string]any{"content": "PermitRootLogin no", "match": "PermitRootLogin"},
			want:    "Port 22\nPermitRootLogin no\n",
			changed: true,
		},
		{
			name:    "appends when nothing matches",
			start:   "Port 22\n",
			args:    map[string]any{"content": "PermitRootLogin no", "match": "PermitRootLogin"},
			want:    "Port 22\nPermitRootLogin no\n",
			changed: true,
		},
		{
			name:    "leaves a line that is already right",
			start:   "PermitRootLogin no\n",
			args:    map[string]any{"content": "PermitRootLogin no", "match": "PermitRootLogin"},
			want:    "PermitRootLogin no\n",
			changed: false,
		},
		{
			name:    "collapses duplicates to one",
			start:   "PermitRootLogin yes\nPort 22\nPermitRootLogin maybe\n",
			args:    map[string]any{"content": "PermitRootLogin no", "match": "PermitRootLogin"},
			want:    "PermitRootLogin no\nPort 22\n",
			changed: true,
		},
		{
			name:    "matches on the content when no match is given",
			start:   "Port 22\n",
			args:    map[string]any{"content": "Port 22"},
			want:    "Port 22\n",
			changed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := editFixture(t, tc.start)
			args := map[string]any{"name": path}
			for k, v := range tc.args {
				args[k] = v
			}
			r := fileLine(&Ctx{}, "sshd", args)
			if !r.Ok {
				t.Fatalf("state failed: %s", r.Comment)
			}
			if r.Changed != tc.changed {
				t.Fatalf("want changed=%v, got %+v", tc.changed, r)
			}
			if got := read(t, path); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestLineModes(t *testing.T) {
	cases := []struct {
		name  string
		start string
		args  map[string]any
		want  string
	}{
		{
			name:  "delete removes every match",
			start: "keep\ndrop me\nkeep\ndrop me too\n",
			args:  map[string]any{"mode": "delete", "match": "drop"},
			want:  "keep\nkeep\n",
		},
		{
			name:  "replace does not create what is absent",
			start: "Port 22\n",
			args:  map[string]any{"mode": "replace", "content": "X y", "match": "X"},
			want:  "Port 22\n",
		},
		{
			name:  "insert at the start",
			start: "second\n",
			args:  map[string]any{"mode": "insert", "content": "first", "location": "start"},
			want:  "first\nsecond\n",
		},
		{
			name:  "insert after an anchor",
			start: "[main]\nkey=1\n[other]\n",
			args:  map[string]any{"mode": "insert", "content": "key=2", "after": "[main]", "match": "key=2"},
			want:  "[main]\nkey=2\nkey=1\n[other]\n",
		},
		{
			name:  "insert before an anchor",
			start: "[main]\nkey=1\n",
			args:  map[string]any{"mode": "insert", "content": "# a note", "before": "[main]", "match": "# a note"},
			want:  "# a note\n[main]\nkey=1\n",
		},
		{
			name:  "insert is a no-op when the line is there",
			start: "first\nsecond\n",
			args:  map[string]any{"mode": "insert", "content": "first", "location": "start"},
			want:  "first\nsecond\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := editFixture(t, tc.start)
			args := map[string]any{"name": path}
			for k, v := range tc.args {
				args[k] = v
			}
			if r := fileLine(&Ctx{}, "f", args); !r.Ok {
				t.Fatalf("state failed: %s", r.Comment)
			}
			if got := read(t, path); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestLineRejectsUnrunnableArguments(t *testing.T) {
	path := editFixture(t, "x\n")
	cases := []struct {
		name string
		args map[string]any
	}{
		{"unknown mode", map[string]any{"mode": "sideways", "content": "x"}},
		{"ensure with no content", map[string]any{"mode": "ensure"}},
		{"delete with no match", map[string]any{"mode": "delete"}},
		{"bad location", map[string]any{"content": "x", "location": "middle"}},
		{"anchor that matches nothing", map[string]any{"mode": "insert", "content": "brand new", "after": "nowhere"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"name": path}
			for k, v := range tc.args {
				args[k] = v
			}
			if r := fileLine(&Ctx{}, "f", args); r.Ok {
				t.Fatalf("want a failure, got %+v", r)
			}
		})
	}
}

func TestLineCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.conf")
	if r := fileLine(&Ctx{}, "f", map[string]any{"name": path, "content": "hello"}); !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	if got := read(t, path); got != "hello\n" {
		t.Fatalf("unexpected contents %q", got)
	}
	missing := filepath.Join(t.TempDir(), "absent.conf")
	if r := fileLine(&Ctx{}, "f", map[string]any{"name": missing, "content": "x", "create": "false"}); r.Ok {
		t.Fatal("create: false should refuse to make the file")
	}
}
