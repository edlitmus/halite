package state

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// The compiler hands its redactor's set to the render, so that a YAML
// error in an SLS that used a multi-line pillar secret without
// `|indent` reaches the compilation's errors scrubbed before its window
// was numbered. render tests the scrub; this tests that the set gets
// there, which is the half a compiler that forgot the field would break
// without any render test noticing. DIVERGENCE 5.568.
func TestTheCompilerScrubsAMultiLineSecretFromARenderError(t *testing.T) {
	const secret = "-----BEGIN PRIVATE KEY-----\n" +
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\n" +
		"MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\n" +
		"-----END PRIVATE KEY-----"
	secrets := redact.New()
	secrets.Add(secret)
	out := compileWith(t, map[string]string{
		"base|tls": "tls_key:\n  file.managed:\n    - name: /etc/ssl/private/web.key\n" +
			"    - contents: {{ pillar['key'] }}\n",
	}, Config{NodeID: "web1.prod", Pillar: value.MapOf("key", secret), Secrets: secrets}, "tls")
	err := out.Err()
	if err == nil {
		t.Fatal("the broken SLS compiled")
	}
	msg := secrets.Scrub(err.Error())
	if !strings.Contains(msg, "rendered output around line") {
		t.Fatalf("the error carries no window, so this test checks nothing:\n%s", msg)
	}
	for _, line := range strings.Split(secret, "\n") {
		if strings.Contains(msg, line) {
			t.Errorf("the compilation error prints a line of the secret, %q:\n%s", line, msg)
		}
	}
}
