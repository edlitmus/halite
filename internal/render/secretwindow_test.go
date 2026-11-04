package render

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// pemSecret is shaped like what a pillar actually holds for a TLS key:
// several lines, each long enough to be a secret on its own, none of
// which is the whole secret.
const pemSecret = "-----BEGIN PRIVATE KEY-----\n" +
	"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\n" +
	"MzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\n" +
	"NMoSfm76oqFvAp8Gy0iz5sxjZmSnXyCdPEovGhLa0VzMaQ8s+CLOyS56YyCFGeJZ\n" +
	"-----END PRIVATE KEY-----"

// pemSLS uses the secret the way 5.407 describes: without `|indent`, so
// that its second line lands at column 0 and the YAML parse fails inside
// it, with the secret in the window either side of the error.
const pemSLS = "tls_key:\n" +
	"  file.managed:\n" +
	"    - name: /etc/ssl/private/web.key\n" +
	"    - contents: {{ pillar['tls']['key'] }}\n" +
	"    - mode: '0600'\n"

func pemPillar() *value.Map {
	tls := value.NewMap(1)
	tls.Set("key", pemSecret)
	p := value.NewMap(1)
	p.Set("tls", tls)
	return p
}

// secretLines is every line of the secret that the redactor would scrub
// if it were a secret on its own: the test asks that none of them is
// printed, because one line of a private key is a line of a private key.
func secretLines() []string {
	var out []string
	for _, line := range strings.Split(pemSecret, "\n") {
		if len(line) >= 6 {
			out = append(out, line)
		}
	}
	return out
}

// A multi-line secret in the rendered output of a file that fails to
// parse must not reach the error's numbered window line by line. The
// sink's scrub looks for the whole secret, and the window has put a
// line number and a template position in front of each of its lines, so
// at the sink nothing matched and the key was printed. DIVERGENCE 5.407
// asked for this; it is fixed by scrubbing the rendered text before it
// is split.
func TestAMultiLineSecretIsNotNumberedIntoTheErrorWindow(t *testing.T) {
	secrets := redact.New()
	secrets.Add(pemSecret)
	_, err := Render([]byte(pemSLS), Options{File: "tls.sls", Pillar: pemPillar(), Secrets: secrets})
	var re *Error
	if !errors.As(err, &re) {
		t.Fatalf("want a render error, got %T: %v", err, err)
	}
	// What a sink does with it: the logger, a state's comment, lint.
	msg := secrets.Scrub(err.Error())
	for _, line := range secretLines() {
		if strings.Contains(msg, line) {
			t.Errorf("the error prints a line of the secret, %q:\n%s", line, msg)
		}
	}
	if re.Context == "" || !strings.Contains(msg, redact.Placeholder) {
		t.Errorf("the window should still be shown, with the secret replaced:\n%s", msg)
	}
	// Scrubbing must not move a line: the window is numbered against
	// the text the parser read, and the marked line has to be the one
	// the parser named, with the lines around it the ones that were
	// around it.
	marked := ""
	for _, line := range strings.Split(re.Context, "\n") {
		if strings.HasPrefix(line, "> ") {
			marked = line
		}
	}
	if !strings.Contains(marked, fmt.Sprintf(" %d ", re.RenderedLine)) {
		t.Errorf("the marked line is %q, want rendered line %d", marked, re.RenderedLine)
	}
	if re.RenderedLine != 5 || !strings.Contains(marked, "> 5 (line 4)") {
		t.Errorf("the error names rendered line %d, marked %q; want line 5, the secret's second", re.RenderedLine, marked)
	}
	if !strings.Contains(re.Context, "3 (line 3)   |     - name: /etc/ssl/private/web.key") ||
		!strings.Contains(re.Context, "4 (line 4)   |     - contents: "+redact.Placeholder) {
		t.Errorf("the lines above the secret are missing or moved:\n%s", re.Context)
	}
	if n := len(strings.Split(re.Context, "\n")); n != 7 {
		t.Errorf("the window has %d lines, want 7: the secret's lines are still lines:\n%s", n, re.Context)
	}
	// The cause is reachable through errors.As, and the parser's copy of
	// the line it stopped at is the secret's second line.
	var ye *yaml.Error
	if errors.As(err, &ye) {
		for _, line := range secretLines() {
			if strings.Contains(ye.Detail(), line) {
				t.Errorf("the YAML cause holds a line of the secret, %q: %s", line, ye.Detail())
			}
		}
	}
}

// `lint --rendered` numbers a file's whole output the way the error
// window numbers part of it, and both lints scrubbed it at the sink,
// after numbering: the same defect on a successful render. A text
// pipeline puts the secret in the output unindented and whole, which is
// the case scrubbing can see at all. DIVERGENCE 5.568.
func TestAMultiLineSecretIsNotNumberedIntoLintRendered(t *testing.T) {
	secrets := redact.New()
	secrets.Add(pemSecret)
	src := []byte("#!jinja|text\n# key for web\n{{ pillar['tls']['key'] }}\n# end\n")
	res, err := Render(src, Options{File: "key.txt", Pillar: pemPillar(), Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	got := secrets.Scrub(Explain(src, "key.txt", res, nil, true, secrets))
	for _, line := range secretLines() {
		if strings.Contains(got, line) {
			t.Errorf("--rendered prints a line of the secret, %q:\n%s", line, got)
		}
	}
	if !strings.Contains(got, "# end") || !strings.Contains(got, "| "+redact.Placeholder) {
		t.Errorf("--rendered should still show the output, the secret replaced:\n%s", got)
	}
}
