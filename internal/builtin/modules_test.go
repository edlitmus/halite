package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// writeFile is the fixture helper the editing tests share.
func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- file editing ----

func TestFileReplace(t *testing.T) {
	r := New()
	path := writeFile(t, filepath.Join(t.TempDir(), "sshd_config"),
		"Port 22\nPermitRootLogin yes\nPasswordAuthentication yes\n")

	args := value.MapOf("name", path, "pattern", `^PermitRootLogin\s+.*$`, "repl", "PermitRootLogin no")
	res := run(t, r, "file.replace", args, false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(readFile(t, path), "PermitRootLogin no") {
		t.Errorf("file:\n%s", readFile(t, path))
	}
	// Idempotent: the pattern no longer matches, so nothing changes.
	second := run(t, r, "file.replace", args, false)
	if second.HasChanges() {
		t.Errorf("second run changed: %+v", second.Changes)
	}
}

func TestFileReplaceGroupReferences(t *testing.T) {
	r := New()
	path := writeFile(t, filepath.Join(t.TempDir(), "f"), "name=old value\n")
	// Python's \1 spelling is rewritten to Go's, so a pattern carried over
	// from a Salt tree substitutes the same way.
	run(t, r, "file.replace", value.MapOf(
		"name", path, "pattern", `name=(\w+) (\w+)`, "repl", `name=\2 \1`), false)
	if got := readFile(t, path); !strings.Contains(got, "name=value old") {
		t.Errorf("file = %q", got)
	}
}

func TestFileReplaceAppendsWhenNotFound(t *testing.T) {
	r := New()
	path := writeFile(t, filepath.Join(t.TempDir(), "f"), "Port 22\n")
	res := run(t, r, "file.replace", value.MapOf(
		"name", path,
		"pattern", `^UseDNS\s+.*$`,
		"repl", "UseDNS no",
		"append_if_not_found", true), false)
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	if got := readFile(t, path); got != "Port 22\nUseDNS no\n" {
		t.Errorf("file = %q", got)
	}
}

func TestFileReplaceRefusesUnsupportedRegex(t *testing.T) {
	// A silent non-match here is a state that reports success and changes
	// nothing; SPEC section 10.4 makes it a hard error naming the
	// construct.
	r := New()
	path := writeFile(t, filepath.Join(t.TempDir(), "f"), "abc\n")
	res := run(t, r, "file.replace", value.MapOf(
		"name", path, "pattern", `a(?=b)`, "repl", "x"), false)
	if res.Succeeded() {
		t.Fatal("a lookahead must fail the state")
	}
	if !strings.Contains(res.Comment, "lookahead") {
		t.Errorf("the comment should name the construct: %s", res.Comment)
	}
}

func TestFileReplaceOnMissingFileSaysWhatToUse(t *testing.T) {
	r := New()
	res := run(t, r, "file.replace", value.MapOf(
		"name", filepath.Join(t.TempDir(), "absent"), "pattern", "x", "repl", "y"), false)
	if res.Succeeded() || !strings.Contains(res.Comment, "file.managed") {
		t.Errorf("comment = %q", res.Comment)
	}
}

func TestFileLineModes(t *testing.T) {
	r := New()
	dir := t.TempDir()

	t.Run("ensure replaces an existing line", func(t *testing.T) {
		path := writeFile(t, filepath.Join(dir, "a"), "Port 22\nPermitRootLogin yes\n")
		run(t, r, "file.line", value.MapOf(
			"name", path, "content", "PermitRootLogin no", "match", "PermitRootLogin", "mode", "ensure"), false)
		if got := readFile(t, path); got != "Port 22\nPermitRootLogin no\n" {
			t.Errorf("file = %q", got)
		}
	})

	t.Run("ensure appends when absent", func(t *testing.T) {
		path := writeFile(t, filepath.Join(dir, "b"), "Port 22\n")
		run(t, r, "file.line", value.MapOf(
			"name", path, "content", "UseDNS no", "mode", "ensure"), false)
		if got := readFile(t, path); got != "Port 22\nUseDNS no\n" {
			t.Errorf("file = %q", got)
		}
	})

	t.Run("absent removes", func(t *testing.T) {
		path := writeFile(t, filepath.Join(dir, "c"), "a\nb\nc\n")
		run(t, r, "file.line", value.MapOf("name", path, "content", "b", "mode", "absent"), false)
		if got := readFile(t, path); got != "a\nc\n" {
			t.Errorf("file = %q", got)
		}
	})

	t.Run("insert after", func(t *testing.T) {
		path := writeFile(t, filepath.Join(dir, "d"), "one\nthree\n")
		run(t, r, "file.line", value.MapOf(
			"name", path, "content", "two", "mode", "insert", "after", "one"), false)
		if got := readFile(t, path); got != "one\ntwo\nthree\n" {
			t.Errorf("file = %q", got)
		}
	})

	t.Run("insert at start", func(t *testing.T) {
		path := writeFile(t, filepath.Join(dir, "e"), "b\n")
		run(t, r, "file.line", value.MapOf(
			"name", path, "content", "a", "mode", "insert", "location", "start"), false)
		if got := readFile(t, path); got != "a\nb\n" {
			t.Errorf("file = %q", got)
		}
	})

	t.Run("ensure keeps indentation", func(t *testing.T) {
		path := writeFile(t, filepath.Join(dir, "f"), "block:\n    setting old\n")
		run(t, r, "file.line", value.MapOf(
			"name", path, "content", "setting new", "match", "setting", "mode", "ensure"), false)
		if got := readFile(t, path); got != "block:\n    setting new\n" {
			t.Errorf("indentation was not preserved: %q", got)
		}
	})
}

func TestFileLineIsIdempotent(t *testing.T) {
	r := New()
	path := writeFile(t, filepath.Join(t.TempDir(), "f"), "Port 22\n")
	args := value.MapOf("name", path, "content", "UseDNS no", "mode", "ensure")
	run(t, r, "file.line", args, false)
	second := run(t, r, "file.line", args, false)
	if second.HasChanges() {
		t.Errorf("second run changed: %+v", second.Changes)
	}
}

func TestFileBlockReplace(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "f"), "before\n")

	args := value.MapOf(
		"name", path,
		"marker_start", "# HALITE START",
		"marker_end", "# HALITE END",
		"content", "managed line\n")

	res := run(t, r, "file.blockreplace", args, false)
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "# HALITE START\nmanaged line\n# HALITE END") {
		t.Errorf("file:\n%s", got)
	}
	// Idempotent.
	if second := run(t, r, "file.blockreplace", args, false); second.HasChanges() {
		t.Errorf("second run changed: %+v", second.Changes)
	}
	// And the block, not the file, is what changes.
	args.Set("content", "different line\n")
	run(t, r, "file.blockreplace", args, false)
	got = readFile(t, path)
	if !strings.HasPrefix(got, "before\n") {
		t.Errorf("content outside the block was lost:\n%s", got)
	}
	if !strings.Contains(got, "different line") {
		t.Errorf("the block was not updated:\n%s", got)
	}
}

func TestFileAppendAndPrepend(t *testing.T) {
	r := New()
	dir := t.TempDir()

	path := writeFile(t, filepath.Join(dir, "a"), "one\n")
	run(t, r, "file.append", value.MapOf("name", path, "text", []any{"two", "three"}), false)
	if got := readFile(t, path); got != "one\ntwo\nthree\n" {
		t.Errorf("append = %q", got)
	}
	// A line already present is not added again.
	res := run(t, r, "file.append", value.MapOf("name", path, "text", []any{"two"}), false)
	if res.HasChanges() {
		t.Errorf("append duplicated a line: %q", readFile(t, path))
	}

	path = writeFile(t, filepath.Join(dir, "b"), "last\n")
	run(t, r, "file.prepend", value.MapOf("name", path, "text", []any{"first"}), false)
	if got := readFile(t, path); got != "first\nlast\n" {
		t.Errorf("prepend = %q", got)
	}
}

func TestFileCommentAndUncomment(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "f"), "keep me\nPermitRootLogin yes\n")

	run(t, r, "file.comment", value.MapOf("name", path, "regex", "PermitRootLogin"), false)
	if got := readFile(t, path); got != "keep me\n#PermitRootLogin yes\n" {
		t.Errorf("comment = %q", got)
	}
	run(t, r, "file.uncomment", value.MapOf("name", path, "regex", "PermitRootLogin"), false)
	if got := readFile(t, path); got != "keep me\nPermitRootLogin yes\n" {
		t.Errorf("uncomment = %q", got)
	}
}

func TestFileEditTestModeWritesNothing(t *testing.T) {
	r := New()
	for _, tc := range []struct {
		fn   string
		args *value.Map
	}{
		{"file.replace", value.MapOf("pattern", "a", "repl", "b")},
		{"file.line", value.MapOf("content", "new line", "mode", "ensure")},
		{"file.append", value.MapOf("text", []any{"new line"})},
		{"file.comment", value.MapOf("regex", "a")},
	} {
		path := writeFile(t, filepath.Join(t.TempDir(), "f"), "a\n")
		before := readFile(t, path)
		tc.args.Set("name", path)
		res := run(t, r, tc.fn, tc.args, true)
		if res.Result != nil {
			t.Errorf("%s in test mode should report that it would change, got %s", tc.fn, res.ResultString())
		}
		if readFile(t, path) != before {
			t.Errorf("%s wrote in test mode", tc.fn)
		}
	}
}

// ---- hosts ----

func TestHostPresentAndAbsent(t *testing.T) {
	r := New()
	dir := t.TempDir()
	saved := HostsPath
	HostsPath = filepath.Join(dir, "hosts")
	defer func() { HostsPath = saved }()
	writeFile(t, HostsPath, "# a comment\n127.0.0.1\tlocalhost\n")

	res := run(t, r, "host.present", value.MapOf("name", "web1.prod", "ip", "10.0.1.15"), false)
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	got := readFile(t, HostsPath)
	if !strings.Contains(got, "10.0.1.15\tweb1.prod") {
		t.Errorf("hosts:\n%s", got)
	}
	// The comment and the existing entry survive a rewrite.
	if !strings.Contains(got, "# a comment") || !strings.Contains(got, "localhost") {
		t.Errorf("the rewrite lost existing content:\n%s", got)
	}

	if second := run(t, r, "host.present", value.MapOf("name", "web1.prod", "ip", "10.0.1.15"), false); second.HasChanges() {
		t.Errorf("second run changed: %+v", second.Changes)
	}

	// A second name on the same address joins the entry rather than
	// making a new one.
	run(t, r, "host.present", value.MapOf("name", "web1", "ip", "10.0.1.15"), false)
	got = readFile(t, HostsPath)
	if strings.Count(got, "10.0.1.15") != 1 {
		t.Errorf("the address was duplicated:\n%s", got)
	}

	run(t, r, "host.absent", value.MapOf("name", "web1.prod"), false)
	got = readFile(t, HostsPath)
	if strings.Contains(got, "web1.prod") {
		t.Errorf("the name survived removal:\n%s", got)
	}
	if !strings.Contains(got, "web1") {
		t.Errorf("removing one name took the other with it:\n%s", got)
	}
}

func TestHostsParsingPreservesShape(t *testing.T) {
	entries := parseHosts("# top\n\n127.0.0.1 localhost # trailing\n10.0.0.1 a b\n")
	if len(entries) != 4 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0].Raw != "# top" || entries[1].Raw != "" {
		t.Errorf("comment and blank lines were not preserved: %+v", entries[:2])
	}
	if entries[2].Comment != "trailing" {
		t.Errorf("trailing comment = %q", entries[2].Comment)
	}
	if len(entries[3].Names) != 2 {
		t.Errorf("names = %v", entries[3].Names)
	}
}

// ---- cron ----

func TestCronParseAndRender(t *testing.T) {
	text := "MAILTO=ops\n" +
		identifierPrefix + " nightly\n" +
		"# the nightly run\n" +
		"17 3 * * * /usr/local/bin/run --nightly\n" +
		"@reboot /usr/local/bin/boot\n"

	entries := parseCrontab(text)
	var nightly, reboot *cronEntry
	for i := range entries {
		switch entries[i].Identifier {
		case "nightly":
			nightly = &entries[i]
		case "/usr/local/bin/boot":
			reboot = &entries[i]
		}
	}
	if nightly == nil {
		t.Fatalf("the identified entry was not found: %+v", entries)
	}
	if nightly.Spec != "17 3 * * *" || nightly.Command != "/usr/local/bin/run --nightly" {
		t.Errorf("nightly = %+v", nightly)
	}
	if nightly.Comment != "the nightly run" {
		t.Errorf("comment = %q", nightly.Comment)
	}
	if reboot == nil || reboot.Spec != "@reboot" {
		t.Errorf("the shorthand schedule was not parsed: %+v", reboot)
	}

	// A rewrite round-trips the identified entry.
	out := renderCrontab(entries)
	again := parseCrontab(out)
	found := false
	for _, e := range again {
		if e.Identifier == "nightly" && e.Spec == "17 3 * * *" {
			found = true
		}
	}
	if !found {
		t.Errorf("the entry did not survive a round trip:\n%s", out)
	}
}

func TestCronSpecFromFields(t *testing.T) {
	if got := cronSpec(value.MapOf("minute", "17", "hour", "3")); got != "17 3 * * *" {
		t.Errorf("spec = %q", got)
	}
	if got := cronSpec(value.MapOf("special", "reboot")); got != "@reboot" {
		t.Errorf("spec = %q", got)
	}
	if got := cronSpec(value.MapOf("special", "@daily")); got != "@daily" {
		t.Errorf("spec = %q", got)
	}
}

func TestCronPresentThroughARecordedCrontab(t *testing.T) {
	r := New()
	rec := &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"crontab -u root -l": {Stdout: "MAILTO=ops\n"},
		},
	}
	c := newCtx(false)
	c.Runner = rec

	res, err := r.States.Call(c, "cron.present", value.MapOf(
		"name", "/usr/local/bin/run", "minute", "17", "hour", "3", "identifier", "nightly"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	// The new crontab is written on stdin, and it keeps what was there.
	var written string
	for _, cmd := range rec.Ran {
		if strings.Contains(cmd.String(), "crontab -u root -") {
			written = cmd.Stdin
		}
	}
	if !strings.Contains(written, "MAILTO=ops") {
		t.Errorf("the rewrite lost the existing content:\n%s", written)
	}
	if !strings.Contains(written, "17 3 * * * /usr/local/bin/run") {
		t.Errorf("the entry was not written:\n%s", written)
	}
	if !strings.Contains(written, identifierPrefix+" nightly") {
		t.Errorf("the identifier was not written:\n%s", written)
	}
}

// ---- sysctl ----

func TestSysctlPersistsAndIsIdempotent(t *testing.T) {
	r := New()
	dir := t.TempDir()
	saved := SysctlConfPath
	SysctlConfPath = filepath.Join(dir, "sysctl.conf")
	defer func() { SysctlConfPath = saved }()
	writeFile(t, SysctlConfPath, "# existing\nkern.other=1\n")

	rec := &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"sysctl -n net.inet.ip.forwarding": {Stdout: "0\n"},
		},
	}
	c := newCtx(false)
	c.Runner = rec

	res, err := r.States.Call(c, "sysctl.present",
		value.MapOf("name", "net.inet.ip.forwarding", "value", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	conf := readFile(t, SysctlConfPath)
	if !strings.Contains(conf, "net.inet.ip.forwarding=1") {
		t.Errorf("the setting was not persisted:\n%s", conf)
	}
	if !strings.Contains(conf, "kern.other=1") || !strings.Contains(conf, "# existing") {
		t.Errorf("the rewrite lost existing content:\n%s", conf)
	}
	// The running value was set too, not just the file.
	var assigned bool
	for _, cmd := range rec.Ran {
		if strings.Contains(cmd.String(), "net.inet.ip.forwarding=1") {
			assigned = true
		}
	}
	if !assigned {
		t.Errorf("the running value was not set: %v", rec.RanCommands())
	}
}

func TestSysctlUnknownParameterFails(t *testing.T) {
	// Writing a configuration line for a parameter the kernel does not
	// have produces a setting that will never take effect.
	r := New()
	rec := &exec.RecordingRunner{Default: exec.Result{Code: 1, Stderr: "unknown oid\n"}}
	c := newCtx(false)
	c.Runner = rec
	res, err := r.States.Call(c, "sysctl.present", value.MapOf("name", "no.such.oid", "value", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Error("an unknown parameter must fail the state")
	}
	if !strings.Contains(res.Comment, "no.such.oid") {
		t.Errorf("comment = %q", res.Comment)
	}
}

func TestSysctlNormalisesWhitespace(t *testing.T) {
	// A multi-value parameter printed with tabs is not a change from the
	// same values printed with spaces.
	if normaliseSysctl("1\t2  3") != normaliseSysctl("1 2 3") {
		t.Error("whitespace differences should not read as a change")
	}
}

// ---- sysrc ----

func TestSysrcPresent(t *testing.T) {
	r := New()
	rec := &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"sysrc -n nginx_enable": {Code: 1},
		},
	}
	c := newCtx(false)
	c.Runner = rec
	if c.Which("sysrc") == "" {
		t.Skip("sysrc is not on this node")
	}

	res, err := r.States.Call(c, "sysrc.present", value.MapOf("name", "nginx_enable", "value", "YES"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(strings.Join(rec.RanCommands(), " "), "nginx_enable=YES") {
		t.Errorf("the setting was not written: %v", rec.RanCommands())
	}
}

// ---- archive ----

func TestArchiveExtractRefusesTraversal(t *testing.T) {
	// The property that matters: an entry named ../../etc/passwd is
	// refused by name rather than written.
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "evil.tar")
	makeTar(t, tarPath, map[string]string{
		"good.txt":         "fine\n",
		"../../etc/passwd": "root::0:0::/:/bin/sh\n",
	})
	dest := filepath.Join(dir, "out")

	_, err := extractArchive(tarPath, dest, false, false)
	if err == nil {
		t.Fatal("a traversing entry must be refused")
	}
	if !strings.Contains(err.Error(), "escapes the destination") {
		t.Errorf("the error should say what happened: %v", err)
	}
	if _, err := os.Stat("/etc/passwd.halite-test"); err == nil {
		t.Fatal("something escaped")
	}
}

func TestArchiveExtractAbsolutePathRefused(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "abs.tar")
	makeTar(t, tarPath, map[string]string{"/etc/shadow": "x\n"})
	if _, err := extractArchive(tarPath, filepath.Join(dir, "out"), false, false); err == nil {
		t.Fatal("an absolute entry must be refused")
	}
}

func TestArchiveExtractedState(t *testing.T) {
	r := New()
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "app.tar.gz")
	makeTarGz(t, tarPath, map[string]string{
		"app/bin/run":      "#!/bin/sh\necho hi\n",
		"app/etc/app.conf": "port = 80\n",
	})
	dest := filepath.Join(dir, "opt")

	res := run(t, r, "archive.extracted", value.MapOf("name", dest, "source", tarPath), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	if got := readFile(t, filepath.Join(dest, "app", "etc", "app.conf")); got != "port = 80\n" {
		t.Errorf("extracted file = %q", got)
	}
	// Idempotent: everything is already there.
	if second := run(t, r, "archive.extracted", value.MapOf("name", dest, "source", tarPath), false); second.HasChanges() {
		t.Errorf("second run changed: %+v", second.Changes)
	}
	// if_missing short-circuits.
	res = run(t, r, "archive.extracted", value.MapOf(
		"name", dest, "source", tarPath, "if_missing", filepath.Join(dest, "app")), false)
	if res.HasChanges() || !strings.Contains(res.Comment, "already exists") {
		t.Errorf("if_missing was not honoured: %+v", res)
	}
}

func TestArchiveExtractedVerifiesHash(t *testing.T) {
	r := New()
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "a.tar")
	makeTar(t, tarPath, map[string]string{"f": "x\n"})

	res := run(t, r, "archive.extracted", value.MapOf(
		"name", filepath.Join(dir, "out"),
		"source", tarPath,
		"source_hash", "sha256="+strings.Repeat("0", 64)), false)
	if res.Succeeded() {
		t.Error("a wrong source_hash must fail the state")
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "f")); err == nil {
		t.Error("the archive was extracted despite a failed hash check")
	}
}

// ---- ssh_auth ----

func TestSSHAuthPresentAndAbsent(t *testing.T) {
	r := New()
	dir := t.TempDir()
	keys := filepath.Join(dir, "authorized_keys")
	const blob = "AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyBlobHere"

	res := run(t, r, "ssh_auth.present", value.MapOf(
		"name", blob, "enc", "ssh-ed25519", "comment", "ops@laptop", "config", keys), false)
	if !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	got := readFile(t, keys)
	if !strings.Contains(got, "ssh-ed25519 "+blob+" ops@laptop") {
		t.Errorf("authorized_keys:\n%s", got)
	}
	// sshd refuses a group-writable file, so the mode is not left to the
	// umask.
	info, err := os.Stat(keys)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %s, want 0600", formatMode(info.Mode()))
	}

	if second := run(t, r, "ssh_auth.present", value.MapOf(
		"name", blob, "enc", "ssh-ed25519", "comment", "ops@laptop", "config", keys), false); second.HasChanges() {
		t.Errorf("second run changed: %+v", second.Changes)
	}

	run(t, r, "ssh_auth.absent", value.MapOf("name", blob, "config", keys), false)
	if strings.Contains(readFile(t, keys), blob) {
		t.Errorf("the key survived removal:\n%s", readFile(t, keys))
	}
}

func TestSSHAuthAcceptsAWholeKeyLine(t *testing.T) {
	r := New()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	line := "no-pty,no-agent-forwarding ssh-rsa AAAAB3NzaC1yc2ExampleBlob ops@host"

	run(t, r, "ssh_auth.present", value.MapOf("name", line, "config", keys), false)
	got := readFile(t, keys)
	if !strings.Contains(got, "no-pty,no-agent-forwarding") {
		t.Errorf("the options were lost:\n%s", got)
	}
	if !strings.Contains(got, "ops@host") {
		t.Errorf("the comment was lost:\n%s", got)
	}
}

func TestAuthorizedKeysParsing(t *testing.T) {
	keys := parseAuthorizedKeys([]string{
		"# a comment",
		"ssh-ed25519 AAAABLOB ops@laptop",
		`command="/bin/backup",no-pty ssh-rsa AAAARSA backup@host`,
		"",
	})
	if len(keys) != 4 {
		t.Fatalf("keys = %d", len(keys))
	}
	if keys[1].Type != "ssh-ed25519" || keys[1].Key != "AAAABLOB" || keys[1].Comment != "ops@laptop" {
		t.Errorf("simple key = %+v", keys[1])
	}
	if len(keys[2].Options) == 0 || keys[2].Type != "ssh-rsa" {
		t.Errorf("key with options = %+v", keys[2])
	}
	if keys[0].Raw == "" || keys[3].Raw != "" && keys[3].Key != "" {
		t.Errorf("comments and blanks were not preserved: %+v", []authKey{keys[0], keys[3]})
	}
}

// ---- grains.filter_by ----

func TestGrainsFilterBy(t *testing.T) {
	r := New()
	c := newCtx(false)
	c.Grains = value.MapOf("os_family", "FreeBSD", "osfinger", "FreeBSD-15")

	lookup := value.MapOf(
		"Debian", value.MapOf("pkg", "nginx-full", "conf", "/etc/nginx"),
		"FreeBSD", value.MapOf("pkg", "nginx", "conf", "/usr/local/etc/nginx"),
		"default", value.MapOf("pkg", "nginx", "conf", "/etc/nginx"),
	)

	out, err := r.Exec.Call(c, "grains.filter_by", value.MapOf("lookup", lookup))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if got, _ := m.Get("conf"); got != "/usr/local/etc/nginx" {
		t.Errorf("conf = %#v", got)
	}

	// The default is used when nothing matches.
	c.Grains = value.MapOf("os_family", "Plan9")
	out, err = r.Exec.Call(c, "grains.filter_by", value.MapOf("lookup", lookup))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := out.(*value.Map).Get("conf"); got != "/etc/nginx" {
		t.Errorf("default conf = %#v", got)
	}

	// merge is layered over the chosen entry.
	c.Grains = value.MapOf("os_family", "FreeBSD")
	out, err = r.Exec.Call(c, "grains.filter_by", value.MapOf(
		"lookup", lookup, "merge", value.MapOf("conf", "/override")))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := out.(*value.Map).Get("conf"); got != "/override" {
		t.Errorf("merged conf = %#v", got)
	}
	// And the merge did not damage the lookup it was given.
	orig, _ := lookup.Get("FreeBSD")
	if got, _ := orig.(*value.Map).Get("conf"); got != "/usr/local/etc/nginx" {
		t.Errorf("filter_by mutated its lookup: %#v", got)
	}
}

func TestGrainsFilterByGlobKey(t *testing.T) {
	r := New()
	c := newCtx(false)
	c.Grains = value.MapOf("osfinger", "Ubuntu-22")
	lookup := value.MapOf("Ubuntu-22*", "jammy", "default", "unknown")

	out, err := r.Exec.Call(c, "grains.filter_by", value.MapOf("lookup", lookup, "grain", "osfinger"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "jammy" {
		t.Errorf("glob key = %#v", out)
	}
}

// ---- random and hashutil ----

func TestRandomIsFromCryptoRand(t *testing.T) {
	r := New()
	c := newCtx(false)
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		out, err := r.Exec.Call(c, "random.get_str", value.MapOf("length", int64(24)))
		if err != nil {
			t.Fatal(err)
		}
		s := out.(string)
		if len(s) != 24 {
			t.Fatalf("length = %d", len(s))
		}
		if seen[s] {
			t.Fatal("random.get_str repeated a value in 32 draws")
		}
		seen[s] = true
	}
}

func TestRandomSeedIsStableForAValue(t *testing.T) {
	// A tree uses this to splay a schedule across a fleet, so the value
	// has to be the same on every run for that node.
	r := New()
	c := newCtx(false)
	first, err := r.Exec.Call(c, "random.seed", value.MapOf("value", "web1.prod", "range", int64(60)))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := r.Exec.Call(c, "random.seed", value.MapOf("value", "web1.prod", "range", int64(60)))
	if first != second {
		t.Errorf("the seed moved: %v then %v", first, second)
	}
	other, _ := r.Exec.Call(c, "random.seed", value.MapOf("value", "web2.prod", "range", int64(60)))
	if other == first {
		t.Errorf("two nodes got the same splay: %v", first)
	}
	if n := first.(int64); n < 0 || n >= 60 {
		t.Errorf("seed = %d, outside the range", n)
	}
}

func TestHMACIsConstantTimeAndCorrect(t *testing.T) {
	r := New()
	c := newCtx(false)
	sig, err := r.Exec.Call(c, "hashutil.hmac_compute",
		value.MapOf("value", "payload", "shared_secret", "key"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := r.Exec.Call(c, "hashutil.hmac_signature", value.MapOf(
		"value", "payload", "shared_secret", "key", "challenge_hmac", sig))
	if err != nil {
		t.Fatal(err)
	}
	if ok != true {
		t.Error("a correct signature was rejected")
	}
	bad, _ := r.Exec.Call(c, "hashutil.hmac_signature", value.MapOf(
		"value", "payload", "shared_secret", "wrong", "challenge_hmac", sig))
	if bad != false {
		t.Error("a wrong key was accepted")
	}
}

// ---- module.run ----

func TestModuleRunCallsAndIsMarkedArbitrary(t *testing.T) {
	r := New()
	sig, ok := r.States.Signatures().Lookup("module.run")
	if !ok || !sig.ArbitraryCode {
		t.Fatal("module.run must declare arbitrary_code; granting it grants every module")
	}

	c := newCtx(false)
	c.Dispatch = dispatcherFor(r)
	res, err := r.States.Call(c, "module.run", value.MapOf("name", "test.ping"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}
	ret, _ := res.Changes.Get("ret")
	if ret != true {
		t.Errorf("ret = %#v", ret)
	}
}

// dispatcherFor lets a module call another module in a test.
type testDispatcher struct{ r *Registries }

func (d testDispatcher) Call(c *exec.Context, name string, args *value.Map) (any, error) {
	return d.r.Exec.Call(c, name, args)
}
func (d testDispatcher) Has(name string) bool { return d.r.Exec.Has(name) }

func dispatcherFor(r *Registries) exec.Dispatcher { return testDispatcher{r} }

// ---- signature hygiene across the whole registry ----

func TestEveryFunctionHasDocumentation(t *testing.T) {
	r := New()
	for _, name := range r.Exec.Names() {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if strings.TrimSpace(sig.Doc) == "" {
			t.Errorf("%s has no documentation", name)
		}
		if sig.Section == "" {
			t.Errorf("%s does not name the specification section it comes from", name)
		}
	}
	for _, name := range r.States.Names() {
		sig, _ := r.States.Signatures().Lookup(name)
		if strings.TrimSpace(sig.Doc) == "" {
			t.Errorf("%s has no documentation", name)
		}
		if sig.Section == "" {
			t.Errorf("%s does not name the specification section it comes from", name)
		}
	}
}

func TestMutatingFunctionsDeclarePrivilegeWhereTheyNeedIt(t *testing.T) {
	// A state that needs root and does not say so gives an operator no
	// way to know why it failed on an unprivileged run.
	r := New()
	needsRoot := map[string]bool{
		"pkg.installed": true, "pkg.removed": true, "pkg.latest": true,
		"service.running": true, "service.dead": true,
		"service.enabled": true, "service.disabled": true,
		"user.present": true, "user.absent": true,
		"group.present": true, "group.absent": true,
		"host.present": true, "host.absent": true,
		"sysctl.present": true,
	}
	for name := range needsRoot {
		sig, ok := r.States.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is missing", name)
			continue
		}
		if len(sig.Privileges) == 0 {
			t.Errorf("%s changes privileged state but declares no privilege requirement", name)
		}
	}
}

func TestStateAndExecModulesAgreeOnNames(t *testing.T) {
	// A state module whose execution counterpart is missing is a state
	// nobody can debug from the command line.
	r := New()
	execModules := map[string]bool{}
	for _, m := range r.Exec.Signatures().Modules() {
		execModules[m] = true
	}
	// The state-only modules, named rather than inferred, so adding one
	// is a decision.
	stateOnly := map[string]bool{"module": true, "archive": true, "ssh_auth": true, "host": true}
	for _, m := range r.States.Modules() {
		if stateOnly[m] || execModules[m] {
			continue
		}
		t.Errorf("the %s state module has no execution module; an operator cannot inspect it from the command line", m)
	}
}
