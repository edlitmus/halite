package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/sshexec"
)

// framedReturn decodes what a oneshot run wrote the way the hub does:
// sshexec.Unframe, then a job.Return.
func framedReturn(t *testing.T, out string) job.Return {
	t.Helper()
	framed, ok := sshexec.Unframe(out)
	if !ok {
		t.Fatalf("no framed return in:\n%s", out)
	}
	var ret job.Return
	if err := json.Unmarshal([]byte(framed), &ret); err != nil {
		t.Fatalf("the frame is not a return the hub can read: %v\n%s", err, framed)
	}
	return ret
}

// captureOneshot runs write with os.Stdout swapped for a file and
// returns what it wrote. Not parallel, for the same reason as
// runOneshotFor.
func captureOneshot(t *testing.T, write func()) string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	write()
	os.Stdout = saved
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// A failure whose error names a path holding a control character was
// framed with Go's `%q`, which wrote `\x01`; the return was not JSON,
// json.Marshal refused it, and the hub was shown the marshaller's
// complaint instead of the error, with no JID. DIVERGENCE 5.582.
//
// Driven through the run rather than oneshotFailure directly, because
// the point is that a real path reaches it: the hub sends `a\x01/b` as a
// file and `a\x01` as a binary one, the first makes `a\x01` a directory,
// and writing the second names it in an *os.PathError the run returns
// as it is. Files are written before binary files, so this is not left
// to map order.
func TestAnAgentlessFailureNamingAControlCharacterReachesTheHub(t *testing.T) {
	const jid = "20261105000000000001"
	code, out, _ := runOneshotFor(t, OneshotRequest{
		Protocol:    OneshotProtocol,
		JID:         jid,
		Fun:         "test.ping",
		Files:       map[string]string{"a\x01/b": "x"},
		FilesBase64: map[string]string{"a\x01": "eA=="},
	})
	if code == 0 {
		t.Fatalf("a tree that could not be written exited 0:\n%s", out)
	}
	ret := framedReturn(t, out)
	var text string
	if err := json.Unmarshal(ret.Return, &text); err != nil {
		t.Fatalf("the return is not a string: %v: %s", err, ret.Return)
	}
	if !strings.Contains(text, "a\x01") || strings.Contains(text, "MarshalJSON") {
		t.Errorf("the hub was not shown the error that happened; it was shown %q", text)
	}
	if string(ret.JID) != jid || ret.RetCode != 1 {
		t.Errorf("the return lost its job: jid %q retcode %d", ret.JID, ret.RetCode)
	}
}

// Every character Go quotes differently from JSON, and invalid UTF-8,
// which a string read off a target's disk can hold although one decoded
// from the hub's JSON cannot.
func TestAnAgentlessFailureFramesAnyErrorText(t *testing.T) {
	for _, msg := range []string{
		"bell \a", "vertical tab \v", "nul \x00", "del \x7f",
		"tag \U000E0001", "line separator \u2028", `C:\Program Files\"x"`,
		"invalid \xff\xfe utf-8",
	} {
		out := captureOneshot(t, func() { oneshotFailure("j", errors.New(msg)) })
		ret := framedReturn(t, out)
		var text string
		if err := json.Unmarshal(ret.Return, &text); err != nil {
			t.Errorf("%q: the return is not a string: %v: %s", msg, err, ret.Return)
			continue
		}
		// Each invalid byte becomes U+FFFD, as a []rune conversion does.
		if want := string([]rune(msg)); text != want {
			t.Errorf("%q arrived as %q", msg, text)
		}
		if ret.JID != "j" || ret.RetCode != 1 {
			t.Errorf("%q: the return lost its job: jid %q retcode %d", msg, ret.JID, ret.RetCode)
		}
	}
}

// The fallback, for a return encoding/json refuses, is JSON too and
// keeps the JID and a failing exit status.
func TestAReturnThatCannotBeEncodedIsStillFramed(t *testing.T) {
	out := captureOneshot(t, func() {
		writeOneshot(&job.Return{JID: "j2", Success: true, Return: json.RawMessage("{\x01")})
	})
	ret := framedReturn(t, out)
	if ret.JID != "j2" || ret.Success || ret.RetCode != 1 {
		t.Errorf("the fallback was %+v", ret)
	}
	var text string
	if err := json.Unmarshal(ret.Return, &text); err != nil ||
		!strings.Contains(text, "could not be encoded") {
		t.Errorf("the fallback's return is %s (%v)", ret.Return, err)
	}
}
