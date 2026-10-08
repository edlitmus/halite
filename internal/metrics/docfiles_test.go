package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every file docs/metrics.md tells Prometheus to read is one the page
// also tells the operator how to make.
//
// The `halite-nodes` scrape job named `ca_file:
// /usr/local/etc/prometheus/halite-nodes-ca.crt`, and `cert_file` and
// `key_file` beside it, and nothing on the page produced any of the
// three. An operator copying the job got a scrape pool that would not
// build, which this page itself says registers no target and silences
// every alert. The owner found it by reading. DIVERGENCE 5.243.
//
// This checks that each path a `tls_config` or `authorization` setting
// names also appears in a shell block. It does not check that the shell
// block makes the right file, or makes it before something reads it --
// the same review found the token emptied by an `install` that ran after
// the login had written it, and nothing here would have caught that.
func TestEveryFilePrometheusReadsIsMadeOnThePage(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "metrics.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(body)

	var shell strings.Builder
	for _, m := range regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(doc, -1) {
		shell.WriteString(m[1])
	}
	setting := regexp.MustCompile(`(?m)^\s*(ca_file|cert_file|key_file|credentials_file):\s*(\S+)`)
	checked := 0
	for _, block := range regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(doc, -1) {
		for _, m := range setting.FindAllStringSubmatch(block[1], -1) {
			checked++
			if !strings.Contains(shell.String(), m[2]) {
				t.Errorf("docs/metrics.md has Prometheus read %s: %s, and no shell block on the "+
					"page makes it; a scrape pool naming a file that does not exist never builds",
					m[1], m[2])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tls_config or authorization file was found on the page; this check has stopped checking")
	}
	t.Logf("checked %d files Prometheus is told to read", checked)
}
