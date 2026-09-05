package gitfs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// A real git repository, driven by the real git binary. The whole point
// of SPEC 13.3 is that this uses the system git rather than a library,
// so a test that mocked git would test nothing that ships.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main", ".")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.git("config", "tag.gpgsign", "false")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"GIT_AUTHOR_DATE=2026-08-25T12:00:00Z", "GIT_COMMITTER_DATE=2026-08-25T12:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *repo) write(path, body string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(message string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", message)
}

func backendFor(t *testing.T, r *repo, adjust func(*Options)) *Backend {
	t.Helper()
	opts := Options{
		Remotes:  []Remote{{URL: r.dir, Name: "tree"}},
		CacheDir: filepath.Join(t.TempDir(), "gitfs"),
		Base:     "main",
		Timeout:  60 * time.Second,
		Log:      func(level, msg string, kv ...any) { t.Logf("%s: %s %v", level, msg, kv) },
	}
	if adjust != nil {
		adjust(&opts)
	}
	b, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestABranchBecomesAnEnvironment(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base:\n  '*':\n    - web\n")
	r.write("web.sls", "nginx:\n  pkg.installed: []\n")
	r.commit("initial")
	r.git("branch", "staging")

	b := backendFor(t, r, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}

	// `main` becomes `base`, as gitfs_base says; another branch keeps
	// its name.
	envs := b.Envs()
	sort.Strings(envs)
	if strings.Join(envs, ",") != "base,staging" {
		t.Fatalf("the environments are %v", envs)
	}

	roots := b.Roots()
	body, err := os.ReadFile(filepath.Join(roots["base"][0], "top.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "'*'") {
		t.Errorf("the served tree is %q", body)
	}
}

// A tag is not an environment unless the estate asked, because every
// tag becoming one turns a release history into a file server.
func TestTagsAreNotServedByDefault(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")
	r.git("tag", "v1.0.0")

	b := backendFor(t, r, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if envs := b.Envs(); strings.Join(envs, ",") != "base" {
		t.Errorf("the environments are %v", envs)
	}

	withTags := backendFor(t, r, func(o *Options) { o.RefTypes = []string{"branches", "tags"} })
	if err := withTags.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	envs := withTags.Envs()
	sort.Strings(envs)
	if strings.Join(envs, ",") != "base,v1.0.0" {
		t.Errorf("with tags the environments are %v", envs)
	}
}

// A branch name reaches a directory name and a URL path.
func TestABranchNameIsMadeSafeAsAnEnvironment(t *testing.T) {
	cases := map[string]string{
		"main":          "base",
		"staging":       "staging",
		"feature/thing": "feature-thing",
		"release-1.2":   "release-1.2",
		"../../etc":     "etc",
		"..":            "",
		"///":           "",
	}
	for ref, want := range cases {
		if got := envFor(ref, "main"); got != want {
			t.Errorf("envFor(%q) = %q, want %q", ref, got, want)
		}
	}
}

// A branch deleted upstream must stop being an environment, or an
// estate keeps applying a tree nobody maintains.
func TestADeletedBranchStopsBeingServed(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")
	r.git("branch", "temporary")

	b := backendFor(t, r, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.Envs()) != 2 {
		t.Fatalf("the environments are %v", b.Envs())
	}

	r.git("branch", "-D", "temporary")
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if envs := b.Envs(); strings.Join(envs, ",") != "base" {
		t.Errorf("a deleted branch is still served: %v", envs)
	}
}

func TestAllowAndDenyFilterEnvironments(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")
	for _, branch := range []string{"staging", "qa", "wip-thing"} {
		r.git("branch", branch)
	}

	allowed := backendFor(t, r, func(o *Options) { o.AllowEnvs = []string{"base", "staging"} })
	if err := allowed.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	envs := allowed.Envs()
	sort.Strings(envs)
	if strings.Join(envs, ",") != "base,staging" {
		t.Errorf("the allowlist gave %v", envs)
	}

	// Deny wins, because a denial is what somebody wrote down
	// deliberately.
	denied := backendFor(t, r, func(o *Options) {
		o.AllowEnvs = []string{"*"}
		o.DenyEnvs = []string{"wip-*"}
	})
	if err := denied.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, env := range denied.Envs() {
		if strings.HasPrefix(env, "wip-") {
			t.Errorf("a denied environment is served: %v", denied.Envs())
		}
	}
}

// A repository that holds the tree under a subdirectory.
func TestGitfsRootServesASubdirectory(t *testing.T) {
	r := newRepo(t)
	r.write("README.md", "not the tree\n")
	r.write("salt/top.sls", "base:\n  '*':\n    - web\n")
	r.commit("initial")

	b := backendFor(t, r, func(o *Options) { o.Remotes[0].Root = "salt" })
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	root := b.Roots()["base"][0]
	if _, err := os.Stat(filepath.Join(root, "top.sls")); err != nil {
		t.Errorf("the subdirectory is not at the root of the served tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); err == nil {
		t.Error("a file outside gitfs_root is served")
	}
}

// A network blip must not empty the file server.
func TestAFailedUpdateLeavesTheServedTreeInPlace(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")

	b := backendFor(t, r, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := b.Roots()

	// The remote goes away.
	if err := os.RemoveAll(r.dir); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(context.Background()); err == nil {
		t.Error("an update against a vanished remote reported success")
	}
	after := b.Roots()
	if len(after["base"]) == 0 || after["base"][0] != before["base"][0] {
		t.Errorf("a failed update emptied the file server: %v", after)
	}
}

// The cache must not grow with every push.
func TestOldTreesAreSweptAway(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")

	b := backendFor(t, r, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	trees := filepath.Dir(b.Roots()["base"][0])

	for i := 0; i < 3; i++ {
		r.write("top.sls", strings.Repeat("# change\n", i+1))
		r.commit("change")
		if err := b.Update(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(trees)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%d trees are cached after four commits: %v", len(entries), names)
	}
}

// An unauthenticated, unencrypted transport is a way to serve an estate
// whatever the network says.
func TestAPlaintextRemoteIsRefused(t *testing.T) {
	_, err := New(Options{
		Remotes:  []Remote{{URL: "git://example.com/tree.git"}},
		CacheDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("a git:// remote was accepted")
	}
	if !strings.Contains(err.Error(), "insecure") {
		t.Errorf("the refusal does not say how to accept it deliberately: %v", err)
	}
	// And it can be accepted deliberately.
	if _, err := New(Options{
		Remotes:  []Remote{{URL: "git://example.com/tree.git", Insecure: true}},
		CacheDir: t.TempDir(),
	}); err != nil {
		t.Errorf("an explicitly insecure remote was refused: %v", err)
	}
}

// Verifying against the hub user's own keyring is not a decision
// anybody made.
func TestVerificationWithoutAKeyringIsRefused(t *testing.T) {
	_, err := New(Options{
		Remotes:          []Remote{{URL: "https://example.com/tree.git"}},
		CacheDir:         t.TempDir(),
		VerifySignatures: true,
	})
	if err == nil {
		t.Fatal("verification with no keyring was accepted")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Errorf("the refusal is %v", err)
	}
}

// A ref that fails verification is not served. That is the whole
// control: a tree whose signature is checked and served anyway is a
// tree that is served.
func TestAnUnsignedRefIsNotServedWhenVerificationIsOn(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")

	keyring := t.TempDir()
	b := backendFor(t, r, func(o *Options) {
		o.VerifySignatures = true
		o.Keyring = keyring
	})
	err := b.Update(context.Background())
	if err == nil {
		t.Fatal("an unsigned repository was served with verification on")
	}
	if len(b.Envs()) != 0 {
		t.Errorf("an unverified environment is served: %v", b.Envs())
	}
	// And the operator is told which ref and why.
	found := false
	for _, state := range b.State() {
		if why, ok := state.Refused["main"]; ok {
			found = true
			if !strings.Contains(why, "signed") {
				t.Errorf("the reason is %q", why)
			}
		}
	}
	if !found {
		t.Errorf("the refusal was not recorded: %+v", b.State())
	}
	// And by category, which is what a metric can carry: one series
	// per ref would be one per branch in every repository the estate
	// serves. SPEC 13.3 makes an unverified ref one that is not
	// served, and a control needs a number behind it.
	refusals := b.Refusals()
	if refusals[RefusedSignature] != 1 {
		t.Errorf("signature refusals counted as %v, want 1", refusals[RefusedSignature])
	}
	if refusals[RefusedMaterialise] != 0 {
		t.Errorf("a signature failure was counted as %v materialise failures",
			refusals[RefusedMaterialise])
	}
}

// A repository that verifies refuses nothing, so the counter stays at
// zero rather than reporting the healthy case as a problem.
func TestAServedRepositoryRefusesNothing(t *testing.T) {
	r := newRepo(t)
	r.write("top.sls", "base: {}\n")
	r.commit("initial")

	b := backendFor(t, r, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := b.Refusals(); len(got) != 0 {
		t.Errorf("a repository that was served reports refusals %v", got)
	}
}

func TestABackendWithNoRemotesIsRefused(t *testing.T) {
	if _, err := New(Options{CacheDir: t.TempDir()}); err == nil {
		t.Error("a git backend with no remotes was accepted")
	}
}

func TestAnUnknownRefTypeIsRefused(t *testing.T) {
	_, err := New(Options{
		Remotes:  []Remote{{URL: "https://example.com/t.git"}},
		CacheDir: t.TempDir(),
		RefTypes: []string{"commits"},
	})
	if err == nil {
		t.Fatal("an unknown ref type was accepted")
	}
	if !strings.Contains(err.Error(), "branches") {
		t.Errorf("the refusal does not say what is valid: %v", err)
	}
}

// A signed ref is served, and the same repository with the signing key
// removed from the keyring is not.
//
// Against real GnuPG and real `git verify-commit`, because the control
// is "git agrees this is signed by a key we trust" and a stand-in for
// either half would prove nothing.
func TestASignedRefIsServedAndAnUntrustedOneIsNot(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg is not installed")
	}
	keyring := t.TempDir()
	if err := os.Chmod(keyring, 0o700); err != nil {
		t.Fatal(err)
	}
	keyID := generateKey(t, keyring)

	r := newRepo(t)
	r.git("config", "user.signingkey", keyID)
	r.git("config", "gpg.program", "gpg")
	r.write("top.sls", "base: {}\n")
	r.git("add", "-A")
	signedCommit(t, r, keyring, "signed")

	b := backendFor(t, r, func(o *Options) {
		o.VerifySignatures = true
		o.Keyring = keyring
	})
	if err := b.Update(context.Background()); err != nil {
		t.Fatalf("a signed ref was not served: %v", err)
	}
	if envs := b.Envs(); strings.Join(envs, ",") != "base" {
		t.Fatalf("the environments are %v", envs)
	}

	// The same repository, checked against a keyring that does not hold
	// the key. Nothing is served.
	empty := t.TempDir()
	if err := os.Chmod(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	other := backendFor(t, r, func(o *Options) {
		o.VerifySignatures = true
		o.Keyring = empty
	})
	if err := other.Update(context.Background()); err == nil {
		t.Error("a ref signed by an untrusted key was served")
	}
	if envs := other.Envs(); len(envs) != 0 {
		t.Errorf("an untrusted ref is served as %v", envs)
	}
}

// generateKey makes a signing key in its own GnuPG home and returns its
// fingerprint.
func generateKey(t *testing.T, home string) string {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("gpg", append([]string{"--batch", "--homedir", home}, args...)...)
		cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("gpg %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("--quick-generate-key", "--passphrase", "", "Halite Test <test@example.com>", "default", "default", "never")
	listing := run("--list-secret-keys", "--with-colons")
	for _, line := range strings.Split(listing, "\n") {
		if fields := strings.Split(line, ":"); len(fields) > 9 && fields[0] == "fpr" {
			return fields[9]
		}
	}
	t.Skip("no key fingerprint in gpg's output")
	return ""
}

// signedCommit makes a commit signed by the key in home.
func signedCommit(t *testing.T, r *repo, home, message string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "--quiet", "-S", "-m", message)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GNUPGHOME="+home, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"GIT_AUTHOR_DATE=2026-08-25T12:00:00Z", "GIT_COMMITTER_DATE=2026-08-25T12:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("signing a commit: %v\n%s", err, out)
	}
}

// A remote URL is judged by what it is, and a local path is not a
// transport at all.
//
// Both platforms' conventions are checked from one host, for the reason
// the configuration layout is: "starts with a slash" is a local path on
// unix and nothing at all on Windows, so a hub there configured with
// `C:\srv\states` was told its own disk "is not an encrypted transport"
// and eight gitfs tests failed on it.
//
// The scp-style case is the one that mattered more than the refusal. A
// Windows path holding an `@` — an account name from a directory, say —
// satisfied "contains @ and contains :" and was accepted as an ssh
// remote, so a local directory would have been handed to git as a
// network URL against a host named after a drive letter.
func TestALocalPathIsNotATransport(t *testing.T) {
	cases := []struct {
		url     string
		windows bool
		local   bool
	}{
		{"/srv/states", false, true},
		{"/srv/states", true, true},
		{`C:\srv\states`, true, true},
		{"C:/srv/states", true, true},
		{`c:\srv\states`, true, true},
		{`\srv\states`, true, true},
		{`C:\Users\some.name@corp\states`, true, true},
		{`C:\srv\states`, false, false},
		// A UNC path is SMB over the network, which is the kind of
		// transport this check exists to refuse.
		{`\\server\share\states`, true, false},
		{"//server/share/states", true, false},
		{"https://git.example/x.git", true, false},
		{"git@git.example:ops/states.git", true, false},
		{"C:", true, false},
		{"", true, false},
	}
	for _, c := range cases {
		if got := isLocalPath(c.url, c.windows); got != c.local {
			t.Errorf("isLocalPath(%q, windows=%v) = %v, want %v", c.url, c.windows, got, c.local)
		}
	}

	scp := map[string]bool{
		"git@git.example:ops/states.git": true,
		"user@host:path":                 true,
		`C:\Users\some.name@corp\states`: false,
		"https://git.example/x.git":      false,
		"/srv/states":                    false,
	}
	for url, want := range scp {
		if got := isSCPStyle(url); got != want {
			t.Errorf("isSCPStyle(%q) = %v, want %v", url, got, want)
		}
	}
}

// A UNC path still needs saying so out loud, since it is the one local-
// looking form that is not local.
func TestAUNCPathIsRefusedUnlessInsecure(t *testing.T) {
	if err := checkURL(Remote{URL: `\\server\share\states`}); err == nil {
		t.Error("a UNC path was accepted without insecure; SMB is a network transport")
	}
	if err := checkURL(Remote{URL: `\\server\share\states`, Insecure: true}); err != nil {
		t.Errorf("insecure should accept it: %v", err)
	}
}
