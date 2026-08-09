package modules

import (
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	a := []byte("one\ntwo\nthree\n")
	b := []byte("one\ntwo changed\nthree\nfour\n")
	d := lineDiff(a, b)
	for _, want := range []string{"-two", "+two changed", "+four"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "-one") || strings.Contains(d, "-three") {
		t.Errorf("diff removed unchanged lines:\n%s", d)
	}
}

func TestLineDiffBinary(t *testing.T) {
	if d := lineDiff([]byte{0xff, 0xfe}, []byte("x")); !strings.Contains(d, "suppressed") {
		t.Errorf("binary not suppressed: %q", d)
	}
}
