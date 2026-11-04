package rendersandbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/value"
)

// The same multi-line secret as render's own test, through the child.
//
// The child is not given the redactor's set -- it holds the node's
// configured secrets as well as pillar, and the child is the process a
// template runs in -- so it cannot scrub the window it would build. It
// used to build it anyway and send it as text, numbered, with the secret
// in it line by line, and the parent's set could do nothing with that.
// Now the child sends the parts and the parent builds the error with
// render.ErrorAt, the function the in-process path uses, so the two
// agree by construction; this checks that they do. DIVERGENCE 5.568.
func TestAMultiLineSecretIsNotNumberedAcrossTheCrossing(t *testing.T) {
	const secret = "-----BEGIN PRIVATE KEY-----\n" +
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\n" +
		"MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\n" +
		"-----END PRIVATE KEY-----"
	src := []byte("tls_key:\n  file.managed:\n    - name: /etc/ssl/private/web.key\n" +
		"    - contents: {{ pillar['tls']['key'] }}\n    - mode: '0600'\n")
	tls := value.NewMap(1)
	tls.Set("key", secret)
	p := value.NewMap(1)
	p.Set("tls", tls)
	secrets := redact.New()
	secrets.Add(secret)
	opts := render.Options{File: "tls.sls", Pillar: p, Secrets: secrets}

	s := newTestSandbox(t)
	_, err := s.Render(src, opts)
	if err == nil {
		t.Fatal("the broken file rendered")
	}
	msg := secrets.Scrub(err.Error())
	for _, line := range strings.Split(secret, "\n") {
		if strings.Contains(msg, line) {
			t.Errorf("the sandboxed error prints a line of the secret, %q:\n%s", line, msg)
		}
	}
	_, direct := render.Render(src, opts)
	if direct == nil || err.Error() != direct.Error() {
		t.Errorf("the sandbox and this process disagree.\nsandbox:\n%v\nin process:\n%v", err, direct)
	}
	// Still a render error, to errors.As and to the sandbox's own test
	// for which failures keep the child alive.
	var re *render.Error
	if !errors.As(err, &re) {
		t.Errorf("the sandboxed error is a %T, not a *render.Error", err)
	}
	if !errors.Is(err, errRender) {
		t.Error("the sandboxed error no longer answers to errRender")
	}
}
