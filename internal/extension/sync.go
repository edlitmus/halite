package extension

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
)

// ExtPrefix is where bundles live on the file server, as SPEC 24.4
// says: `_ext/<name>/<version>/...`.
const ExtPrefix = "_ext/"

// SourceFile is one file the file server offers.
type SourceFile struct {
	// Path is relative to the environment root, so it begins `_ext/`.
	Path string
	// Digest is what the server published, used to skip a file the
	// cache already holds. It is never trusted as verification: the
	// signature is.
	Digest string
}

// Source is the file server, as much of it as synchronization needs.
type Source interface {
	// List returns the files under a prefix.
	List(prefix string) ([]SourceFile, error)
	// Fetch reads one.
	Fetch(path string) ([]byte, error)
}

// Syncer fetches bundles into the cache.
//
// SPEC 24.5 is explicit that synchronization fetches and does not load,
// and the difference is the whole point of the section: in Salt,
// `saltutil.sync_all` means "the agent will now execute new code from
// the file server". Here it means "the agent has fetched signed, pinned
// bundles", and what runs does not change until the node next loads
// them.
type Syncer struct {
	Source Source
	// Dir is the cache root.
	Dir string
	// Options are how a fetched bundle is verified before it is kept.
	Options LoadOptions
	// Pins fix each extension.
	Pins map[string]Pin
	// Kinds limits what is synchronized, for the per-kind variants of
	// `saltutil.sync_modules` and friends. Empty takes everything.
	Kinds []string
}

// Change is what happened to one bundle.
type Change struct {
	Name    string
	Version string
	// Status is `fetched`, `unchanged`, or `refused`.
	Status string
	// Reason is why it was refused.
	Reason string
	// Root is the Merkle root of what was fetched, which is what an
	// operator copies into a pin.
	Root string
}

// Report is what a synchronization did.
type Report struct {
	Changes []Change
	// Errors are failures that were not about one bundle — the listing
	// itself, most often.
	Errors []string
}

// Changed reports whether anything was fetched, which is what tells an
// operator a restart will pick something up.
func (r Report) Changed() bool {
	for _, c := range r.Changes {
		if c.Status == "fetched" {
			return true
		}
	}
	return false
}

// Sync fetches what the file server offers and the node does not have.
//
// A bundle is verified in a staging directory and moved into the cache
// only if it verifies. A bundle that fails is left out entirely rather
// than replacing what is there: a node running a good version of an
// extension must not lose it because somebody published a bad one.
func (s *Syncer) Sync() (Report, error) {
	var report Report
	if s.Source == nil || s.Dir == "" {
		return report, fmt.Errorf("synchronization needs a file server and a cache")
	}
	files, err := s.Source.List(ExtPrefix)
	if err != nil {
		return report, fmt.Errorf("listing %s: %w", ExtPrefix, err)
	}

	bundles := groupBundles(files)
	names := make([]string, 0, len(bundles))
	for key := range bundles {
		names = append(names, key)
	}
	sort.Strings(names)

	for _, key := range names {
		name, version, _ := strings.Cut(key, "/")
		change := s.syncOne(name, version, bundles[key])
		report.Changes = append(report.Changes, change)
	}
	return report, nil
}

// syncOne fetches, verifies, and installs one bundle.
func (s *Syncer) syncOne(name, version string, files []SourceFile) Change {
	change := Change{Name: name, Version: version}
	target := filepath.Join(s.Dir, name, version)

	// A pinned extension at another version is not fetched at all.
	// Fetching it would fill the cache with bundles that can never
	// load, and the refusal is more useful than the download.
	if pin, ok := s.Pins[name]; ok && pin.Version != "" && pin.Version != version {
		change.Status = "skipped"
		change.Reason = fmt.Sprintf("%s is pinned to %s", name, pin.Version)
		return change
	}

	if s.upToDate(name, version, target, files) {
		change.Status = "unchanged"
		if root, err := rootOf(target); err == nil {
			change.Root = root
		}
		return change
	}

	staging, err := os.MkdirTemp(filepath.Dir(s.Dir), "halite-ext-staging-*")
	if err != nil {
		change.Status, change.Reason = "refused", err.Error()
		return change
	}
	defer os.RemoveAll(staging)

	for _, file := range files {
		rel, ok := relativeTo(name, version, file.Path)
		if !ok {
			continue
		}
		body, err := s.Source.Fetch(file.Path)
		if err != nil {
			change.Status, change.Reason = "refused", err.Error()
			return change
		}
		dest := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			change.Status, change.Reason = "refused", err.Error()
			return change
		}
		// The executable bit is set from the manifest below rather than
		// carried over the wire: a file server does not publish modes,
		// and guessing from the name would make `run.sh` executable
		// because of its extension.
		if err := os.WriteFile(dest, body, 0o600); err != nil {
			change.Status, change.Reason = "refused", err.Error()
			return change
		}
	}

	opts := s.Options
	opts.Pin = s.Pins[name]
	bundle, err := Load(staging, opts)
	if err != nil {
		// Refused before anything is moved. A node running a good
		// version must not lose it because somebody published a bad
		// one.
		change.Status, change.Reason = "refused", err.Error()
		return change
	}
	if !s.wantsKind(bundle.Manifest.Kind) {
		change.Status = "skipped"
		change.Reason = fmt.Sprintf("this synchronization is for %s and %s is a %s extension",
			strings.Join(s.Kinds, ", "), name, bundle.Manifest.Kind)
		return change
	}
	if bundle.Manifest.Name != name || bundle.Manifest.Version != version {
		// The path on the file server and the signed manifest disagree.
		// Trusting the path would let a bundle be installed under a
		// name it was not signed for, which is how a trusted extension
		// gets replaced by another trusted extension.
		change.Status = "refused"
		change.Reason = fmt.Sprintf("it is published at %s/%s and signed as %s/%s",
			name, version, bundle.Manifest.Name, bundle.Manifest.Version)
		return change
	}

	if err := markExecutable(staging, bundle.Manifest); err != nil {
		change.Status, change.Reason = "refused", err.Error()
		return change
	}
	if err := install(staging, target); err != nil {
		change.Status, change.Reason = "refused", err.Error()
		return change
	}
	change.Status = "fetched"
	if root, err := bundle.Root(); err == nil {
		change.Root = root
	}
	return change
}

// upToDate reports whether the cache already holds this bundle.
//
// By the server's published digests, which is a cheap comparison and
// not a security decision: what makes the cached copy trustworthy is
// that it verifies on every load.
func (s *Syncer) upToDate(name, version, target string, files []SourceFile) bool {
	// No files means nothing to compare against, which is not the same
	// as everything matching.
	checked := 0
	for _, file := range files {
		rel, ok := relativeTo(name, version, file.Path)
		if !ok {
			continue
		}
		got, err := DigestFile(filepath.Join(target, filepath.FromSlash(rel)))
		if err != nil {
			return false
		}
		if file.Digest == "" || !EqualDigest(got, file.Digest) {
			return false
		}
		checked++
	}
	return checked > 0 && checked == len(files)
}

func (s *Syncer) wantsKind(kind string) bool {
	if len(s.Kinds) == 0 {
		return true
	}
	for _, want := range s.Kinds {
		if want == kind {
			return true
		}
	}
	return false
}

// markExecutable sets the mode on what the manifest calls an
// executable, and on nothing else.
//
// From the signed manifest rather than from the file server, which
// publishes no modes. Anything else in the bundle stays unreadable to
// other users and non-executable: a bundle should not be able to bring
// a setuid helper along by naming it convincingly.
func markExecutable(dir string, manifest *Manifest) error {
	for _, rel := range manifest.Executables {
		// Through internal/fileperm rather than os.Chmod. A mode is
		// the whole answer on unix; on Windows what makes a file
		// runnable is its name, which the manifest check enforces,
		// and what keeps it from other accounts is an access control
		// list. 0700 asks for both, and this carries out whichever
		// half the platform has.
		if err := fileperm.Apply(filepath.Join(dir, filepath.FromSlash(rel)), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// install puts a verified bundle in place.
//
// The old one is moved aside and removed after the new one lands, so a
// failure part-way leaves the previous bundle rather than nothing.
func install(staging, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	previous := ""
	if _, err := os.Stat(target); err == nil {
		previous = target + ".replacing"
		_ = os.RemoveAll(previous)
		if err := atomicfile.Rename(target, previous); err != nil {
			return err
		}
	}
	if err := atomicfile.Rename(staging, target); err != nil {
		if previous != "" {
			_ = atomicfile.Rename(previous, target)
		}
		return err
	}
	if previous != "" {
		_ = os.RemoveAll(previous)
	}
	// The staging directory has been renamed away, so the caller's
	// deferred cleanup finds nothing. Recreate it so that cleanup is
	// not an error on a path nobody reads.
	return os.MkdirAll(staging, 0o700)
}

// groupBundles sorts the file server's listing into `<name>/<version>`.
func groupBundles(files []SourceFile) map[string][]SourceFile {
	out := map[string][]SourceFile{}
	for _, file := range files {
		name, version, _, ok := splitExtPath(file.Path)
		if !ok {
			continue
		}
		out[name+"/"+version] = append(out[name+"/"+version], file)
	}
	return out
}

// splitExtPath reads `_ext/<name>/<version>/<rest>`.
func splitExtPath(p string) (name, version, rest string, ok bool) {
	trimmed, found := strings.CutPrefix(path.Clean(p), ExtPrefix)
	if !found {
		return "", "", "", false
	}
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	// The name and version become directories under the cache, so they
	// are checked before they are used as ones.
	if err := checkPath(parts[0]); err != nil {
		return "", "", "", false
	}
	if err := checkPath(parts[1]); err != nil {
		return "", "", "", false
	}
	if err := checkPath(parts[2]); err != nil {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// relativeTo answers with a file's path inside its bundle.
func relativeTo(name, version, full string) (string, bool) {
	gotName, gotVersion, rest, ok := splitExtPath(full)
	if !ok || gotName != name || gotVersion != version {
		return "", false
	}
	return rest, true
}

// rootOf reads a cached bundle's Merkle root without verifying it, for
// reporting an unchanged bundle's root.
func rootOf(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ManifestName)))
	if err != nil {
		return "", err
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return "", err
	}
	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", root), nil
}
