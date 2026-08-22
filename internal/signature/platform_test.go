package signature

import (
	"runtime"
	"strings"
	"testing"
)

// "Platforms restricts the function; empty means every platform" is what
// the field documents, and nothing restricted anything: a `sysrc` call
// on Linux reached the module, which looked for a binary no Linux has
// and reported that instead — a true statement about the wrong thing.
func TestCheckPlatform(t *testing.T) {
	here := runtime.GOOS
	elsewhere := "plan9"
	if here == elsewhere {
		elsewhere = "linux"
	}

	if err := (Signature{Module: "test", Function: "any"}).CheckPlatform(); err != nil {
		t.Errorf("an empty Platforms means every platform: %v", err)
	}
	if err := (Signature{Module: "sysrc", Function: "set", Platforms: []string{here}}).CheckPlatform(); err != nil {
		t.Errorf("this platform is listed: %v", err)
	}
	err := (Signature{Module: "sysrc", Function: "set", Platforms: []string{elsewhere}}).CheckPlatform()
	if err == nil {
		t.Fatal("a function restricted to another platform should be refused")
	}
	// The refusal has to name all three things an operator needs: what
	// they called, where it runs, and where they are.
	for _, want := range []string{"sysrc.set", elsewhere, here} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %q: %v", want, err)
		}
	}
}

// The privilege declaration explains a failure rather than preventing an
// attempt: every function that declares one is a mutating function that
// declares root, and refusing up front would refuse a `--test` run,
// which is the run an operator makes precisely because they are not
// ready to be root.
func TestPrivilegeNote(t *testing.T) {
	s := Signature{Module: "user", Function: "present", Privileges: []string{"root"}}
	if note := s.PrivilegeNote(0); note != "" {
		t.Errorf("root needs no explanation: %q", note)
	}
	note := s.PrivilegeNote(1000)
	if !strings.Contains(note, "user.present") || !strings.Contains(note, "root") {
		t.Errorf("the note should name the function and what it needs: %q", note)
	}
	if n := (Signature{Module: "test", Function: "nop"}).PrivilegeNote(1000); n != "" {
		t.Errorf("a function declaring nothing should say nothing: %q", n)
	}
	other := Signature{Module: "x", Function: "y", Privileges: []string{"CAP_NET_ADMIN"}}
	if n := other.PrivilegeNote(1000); !strings.Contains(n, "CAP_NET_ADMIN") {
		t.Errorf("a privilege that is not root should still be named: %q", n)
	}
}
