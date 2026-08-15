package modules

import (
	"strings"
	"testing"
)

func TestAptRepositoryLine(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "url and dist",
			args: map[string]any{"url": "https://nginx.org/packages/debian", "dist": "bookworm"},
			want: "deb https://nginx.org/packages/debian bookworm",
		},
		{
			name: "components and signing key",
			args: map[string]any{
				"url": "https://example.com/apt", "dist": "stable",
				"comps": []any{"main", "contrib"}, "arch": "amd64",
				"signed_by": "/etc/apt/keyrings/example.gpg",
			},
			want: "deb [arch=amd64 signed-by=/etc/apt/keyrings/example.gpg] https://example.com/apt stable main contrib",
		},
		{
			name: "verbatim line wins",
			args: map[string]any{"line": "deb https://verbatim.example/apt sid main", "url": "ignored"},
			want: "deb https://verbatim.example/apt sid main",
		},
		{
			name: "disabled is commented out",
			args: map[string]any{"url": "https://example.com/apt", "dist": "stable", "enabled": "false"},
			want: "# deb https://example.com/apt stable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := aptRepo("example", tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(repo.body, tc.want+"\n") {
				t.Fatalf("want %q in:\n%s", tc.want, repo.body)
			}
			if repo.path != "/etc/apt/sources.list.d/example.list" {
				t.Fatalf("unexpected path %q", repo.path)
			}
		})
	}
}

func TestAptRepositoryNeedsUrlAndDist(t *testing.T) {
	if _, err := aptRepo("example", map[string]any{"url": "https://example.com/apt"}); err == nil {
		t.Fatal("a repository with no suite cannot be written")
	}
}

func TestYumRepositoryFile(t *testing.T) {
	repo := yumRepo("example", map[string]any{
		"humanname": "Example Repo",
		"baseurl":   "https://example.com/el9/$basearch",
		"gpgkey":    "https://example.com/RPM-GPG-KEY",
	}, "/etc/yum.repos.d", nil)

	for _, want := range []string{
		"[example]",
		"name=Example Repo",
		"baseurl=https://example.com/el9/$basearch",
		"gpgcheck=1",
		"enabled=1",
		"gpgkey=https://example.com/RPM-GPG-KEY",
	} {
		if !strings.Contains(repo.body, want+"\n") {
			t.Fatalf("want %q in:\n%s", want, repo.body)
		}
	}
	if repo.path != "/etc/yum.repos.d/example.repo" {
		t.Fatalf("unexpected path %q", repo.path)
	}
}

func TestYumRepositoryTakesUrlAsBaseurl(t *testing.T) {
	repo := yumRepo("example", map[string]any{"url": "https://example.com/el9"}, "/etc/yum.repos.d", nil)
	if !strings.Contains(repo.body, "baseurl=https://example.com/el9\n") {
		t.Fatalf("url should stand in for baseurl:\n%s", repo.body)
	}
}

func TestFreebsdRepositoryFile(t *testing.T) {
	repo := freebsdRepo("Local", map[string]any{
		"url":            "pkg+http://pkg.example.com/${ABI}/latest",
		"signature_type": "pubkey",
		"enabled":        "false",
	})
	for _, want := range []string{
		`Local: {`,
		`url: "pkg+http://pkg.example.com/${ABI}/latest",`,
		`signature_type: "pubkey",`,
		`enabled: false`,
	} {
		if !strings.Contains(repo.body, want) {
			t.Fatalf("want %q in:\n%s", want, repo.body)
		}
	}
	if repo.path != "/usr/local/etc/pkg/repos/Local.conf" {
		t.Fatalf("unexpected path %q", repo.path)
	}
}

func TestRepositoryBodyIsStable(t *testing.T) {
	args := map[string]any{"baseurl": "https://example.com", "gpgkey": "https://example.com/key", "priority": "10"}
	first := yumRepo("example", args, "/etc/yum.repos.d", nil).body
	for i := 0; i < 5; i++ {
		if got := yumRepo("example", args, "/etc/yum.repos.d", nil).body; got != first {
			t.Fatal("field order must not vary between runs, or every run reports a change")
		}
	}
}

// TestRepoChangesHonoursShowDiff keeps a repository credential out of a
// job result. docs/pillar-security.md tells operators that show_diff is
// how to do that, which only works if every state that emits a diff obeys
// it — this one did not.
func TestRepoChangesHonoursShowDiff(t *testing.T) {
	repo := repoFile{
		path: "/etc/yum.repos.d/acme.repo",
		body: "[acme]\nbaseurl=https://user:s3cret@repo.example.com/rpm\n",
	}
	current := []byte("[acme]\nbaseurl=https://old.example.com/rpm\n")

	shown := repoChanges(repo, current, true, true)
	if shown["diff"] == "" {
		t.Error("a diff is still the default")
	}
	hidden := repoChanges(repo, current, true, false)
	if diff, ok := hidden["diff"]; ok {
		t.Errorf("show_diff: false still reported a diff: %q", diff)
	}
	for key, value := range hidden {
		if strings.Contains(value, "s3cret") {
			t.Errorf("changes[%q] carries the credential: %q", key, value)
		}
	}
	if hidden[repo.path] != "written" {
		t.Errorf("the file itself is still reported: %v", hidden)
	}
	// A repository that did not exist has nothing to diff against.
	if created := repoChanges(repo, nil, false, true); created["diff"] != "" {
		t.Errorf("a new file should have no diff: %v", created)
	}
}
