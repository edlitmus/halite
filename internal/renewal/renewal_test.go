package renewal

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// Each renewal sets the old key aside and, once it has succeeded, removes
// the ones earlier renewals set aside -- and nothing else.
//
// Nothing pruned them: a node kept every private key it had renewed
// away from, one per 45 days on the default lifetime, each for a
// certificate the hub had revoked. DIVERGENCE 5.222. A key `enroll
// --force` moved aside, and one named the old way that could be either,
// are not the renewal's to remove.
func TestARenewalPrunesOnlyTheKeysEarlierRenewalsSetAside(t *testing.T) {
	files := pki.Files{Dir: t.TempDir()}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(files.Path(name), []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(pki.NodeKeyFile)
	const enrollAside = "node.key.20250101T000000" // enroll --force, and the old renewal name
	write(enrollAside)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var asides []string
	for i := 0; i < 3; i++ {
		aside, err := SetKeyAside(files, start.Add(time.Duration(i)*45*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		asides = append(asides, aside)
		write(pki.NodeKeyFile) // the renewal's new key
		removed, err := PruneKeysAside(files, aside)
		if err != nil {
			t.Fatal(err)
		}
		if want := min(i, 1); len(removed) != want {
			t.Errorf("renewal %d removed %v, want %d", i+1, removed, want)
		}
	}

	entries, err := os.ReadDir(files.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{pki.NodeKeyFile, enrollAside, filepath.Base(asides[2])}
	sort.Strings(want)
	if strings.Join(left, " ") != strings.Join(want, " ") {
		t.Errorf("after three renewals the directory holds %v, want %v", left, want)
	}
	if !strings.HasPrefix(filepath.Base(asides[2]), "node.key.renewed.") {
		t.Errorf("a renewal's aside is %s, which cannot be told from enroll --force's", asides[2])
	}
}
