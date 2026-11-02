package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
)

func fileServerResult(t *testing.T, yaml string) doctor.Result {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.Hub.FileName()), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Hub, config.LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return hubFileServerCheck(cfg).Run(context.Background())
}

// A hub that serves from git or S3 is not told it serves nothing. The
// check read `file_roots` alone and warned that every `source:` would fail.
// DIVERGENCE 5.554.
func TestTheHubFileServerCheckCountsTheGitAndS3Backends(t *testing.T) {
	git := fileServerResult(t, "fileserver_backend: [git]\ngitfs_remotes:\n  - https://git.example/states.git\n")
	if git.Status != doctor.Pass || !strings.Contains(git.Detail, "the git backend") {
		t.Errorf("a git-only hub: %s %q, want a pass naming the backend", git.Status, git.Detail)
	}
	s3 := fileServerResult(t, "fileserver_backend: [s3]\ns3_buckets:\n  - states\n  - more\n")
	if s3.Status != doctor.Pass || !strings.Contains(s3.Detail, "the S3 backend") {
		t.Errorf("an S3-only hub: %s %q", s3.Status, s3.Detail)
	}
	both := fileServerResult(t, "fileserver_backend: [git, s3]\ngitfs_remotes: [https://g.example/a.git]\ns3_buckets: [b]\n")
	if both.Status != doctor.Pass || !strings.Contains(both.Detail, "git backend") || !strings.Contains(both.Detail, "S3 backend") {
		t.Errorf("both: %s %q", both.Status, both.Detail)
	}

	// What must still be said: no roots and no backend serves nothing, and
	// a backend named with nothing behind it serves nothing.
	if none := fileServerResult(t, "log_level: info\n"); none.Status != doctor.Warn {
		t.Errorf("a hub with nothing to serve: %s %q, want a warning", none.Status, none.Detail)
	}
	if empty := fileServerResult(t, "fileserver_backend: [git]\n"); empty.Status != doctor.Warn {
		t.Errorf("git named and no remote configured: %s %q, want a warning", empty.Status, empty.Detail)
	}
	// A root that does not exist is still a failure, backends or not.
	missing := fileServerResult(t, "file_roots:\n  base:\n    - /nonexistent/halite-doctor-test\nfileserver_backend: [roots, git]\ngitfs_remotes: [https://g.example/a.git]\n")
	if missing.Status != doctor.Fail {
		t.Errorf("a missing root: %s %q, want a failure", missing.Status, missing.Detail)
	}
}
