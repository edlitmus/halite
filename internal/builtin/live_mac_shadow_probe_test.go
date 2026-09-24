package builtin

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// TEMPORARY: which of macOS's password tools read a new password from a
// non-terminal stdin verbatim. Each candidate is verified by authonly.
// Removed once the answer is in the ledger.
func TestLiveMacShadowProbeStdinRoutes(t *testing.T) {
	c, account, _ := macAccountLiveRoot(t)
	res, err := macUserPresentState(c, value.MapOf("name", account, "shell", "/bin/zsh",
		"home", "/Users/"+account, "fullname", "halite probe"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("creating %s: %v %+v", account, err, res)
	}
	suffix := throwawayPassword(t)
	passwords := map[string]string{
		"plain":      suffix,
		"everything": "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~ " + suffix,
		"spaces":     "  two  spaces  " + suffix + " ",
		"leading-":   "-" + suffix,
		"non-ascii":  "pässwörd-" + suffix,
	}
	routes := map[string]func(pw string) exec.Command{
		"dscl-prompt": func(pw string) exec.Command {
			return exec.Command{Argv: []string{"dscl", "-q", "."},
				Stdin: "passwd /Users/" + account + "\n" + pw + "\n" + pw + "\n"}
		},
		"sysadminctl": func(pw string) exec.Command {
			return exec.Command{Argv: []string{"sysadminctl", "-resetPasswordFor", account, "-newPassword", "-"},
				Stdin: pw + "\n" + pw + "\n"}
		},
		"passwd": func(pw string) exec.Command {
			return exec.Command{Argv: []string{"passwd", account}, Stdin: pw + "\n" + pw + "\n"}
		},
	}
	for rname, route := range routes {
		for pname, pw := range passwords {
			cmd := route(pw)
			cmd.IgnoreExitCode = true
			cmd.Timeout = 15 * time.Second
			r, err := c.Run(cmd)
			ok, _ := c.Run(exec.Command{Argv: []string{"dscl", ".", "-authonly", account, pw}, IgnoreExitCode: true})
			t.Logf("%s/%s: exit %d err %v stdout %q stderr %q -> authonly exit %d",
				rname, pname, r.Code, err, redact(r.Stdout, suffix), redact(r.Stderr, suffix), ok.Code)
		}
	}
}

func redact(s, secret string) string {
	out := []rune{}
	_ = out
	for i := 0; i+len(secret) <= len(s); i++ {
		if s[i:i+len(secret)] == secret {
			return s[:i] + "<SECRET>" + redact(s[i+len(secret):], secret)
		}
	}
	return s
}
