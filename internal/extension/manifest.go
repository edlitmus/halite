// Package extension is the distribution and trust half of SPEC section
// 24: a signed bundle, verified on every load, pinned so that a file
// server change cannot silently swap it.
//
// SPEC 24.4's claim is that this is materially better than Salt, and
// the reasons are checkable: code delivered to a node must be signed by
// a key the node trusts, must be pinned by version and digest, runs out
// of process, and runs sandboxed. This package is the first two.
package extension

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ManifestName is the file inside a bundle that describes it.
const ManifestName = "manifest.json"

// SignatureName is the detached signature over the bundle's Merkle
// root.
const SignatureName = "manifest.sig"

// Kinds are the extension kinds of SPEC 24.2.
var Kinds = []string{
	"module", "state", "grain", "beacon", "returner", "pillar",
	"runner", "renderer", "auth", "roster", "fileserver", "signer",
}

// Manifest describes a bundle.
type Manifest struct {
	// Name is how the extension is referred to everywhere: in
	// configuration, in pillar pinning, and in
	// `sys.list_extensions`.
	Name string `json:"name"`
	// Version is the bundle's, and half of what pins it.
	Version string `json:"version"`
	// Kind is what it provides.
	Kind string `json:"kind"`
	// Executables maps `<goos>/<goarch>` to the path inside the bundle.
	// A bundle may carry one platform or several; a node that finds
	// none for itself refuses rather than running something built for
	// another machine.
	Executables map[string]string `json:"executables"`
	// Declares is what the extension needs — `root`, `network`. The
	// sandbox grants nothing else, and SPEC 24.3 makes this visible.
	Declares []string `json:"declares,omitempty"`
	// Files is every file in the bundle with its SHA-256, which is what
	// the Merkle root is computed over.
	Files map[string]string `json:"files"`
	// Description is for a person reading `sys.list_extensions`.
	Description string `json:"description,omitempty"`
}

// Check reports whether a manifest is well formed.
//
// Before anything is verified or run: a manifest naming a path outside
// the bundle, or a kind this build has never heard of, is a bundle to
// refuse rather than to unpack and find out about.
func (m *Manifest) Check() error {
	if m.Name == "" {
		return fmt.Errorf("the manifest names no extension")
	}
	if strings.ContainsAny(m.Name, `/\ `+"\t") || m.Name == "." || m.Name == ".." {
		// The name becomes a directory under the cache.
		return fmt.Errorf("%q is not a usable extension name", m.Name)
	}
	if m.Version == "" {
		return fmt.Errorf("%s names no version, and a version is half of what pins it", m.Name)
	}
	if strings.ContainsAny(m.Version, `/\ `) {
		return fmt.Errorf("%q is not a usable version", m.Version)
	}
	if !validKind(m.Kind) {
		return fmt.Errorf("%q is not an extension kind; SPEC 24.2 names %s",
			m.Kind, strings.Join(Kinds, ", "))
	}
	if len(m.Executables) == 0 {
		return fmt.Errorf("%s carries no executable", m.Name)
	}
	for platform, rel := range m.Executables {
		if !strings.Contains(platform, "/") {
			return fmt.Errorf("%s: %q is not a platform; it is <goos>/<goarch>", m.Name, platform)
		}
		if err := checkPath(rel); err != nil {
			return fmt.Errorf("%s: the %s executable: %w", m.Name, platform, err)
		}
		if _, listed := m.Files[rel]; !listed {
			// An executable outside the file list is one outside the
			// Merkle root, which is to say one that is not signed.
			return fmt.Errorf("%s: the %s executable %q is not in the file list, so it is not signed",
				m.Name, platform, rel)
		}
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("%s lists no files", m.Name)
	}
	for rel, digest := range m.Files {
		if err := checkPath(rel); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		if len(digest) != 64 {
			return fmt.Errorf("%s: %q has a digest that is not a SHA-256", m.Name, rel)
		}
	}
	for _, declared := range m.Declares {
		switch declared {
		case "root", "network":
		default:
			return fmt.Errorf("%s declares %q, which this build does not understand", m.Name, declared)
		}
	}
	return nil
}

// checkPath refuses anything that would write outside the bundle.
//
// A manifest is signed, so a hostile one needs a trusted key — but a
// *mistaken* one does not, and neither does a bundle whose author was
// careless with a build script. The check costs nothing.
func checkPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("an empty path")
	}
	if path.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("%q is absolute", rel)
	}
	if rel != path.Clean(rel) {
		return fmt.Errorf("%q is not a clean path", rel)
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == ".." {
			return fmt.Errorf("%q leaves the bundle", rel)
		}
	}
	if strings.ContainsRune(rel, '\\') {
		// A backslash is a path separator on the platform this may be
		// unpacked on, and a separator that this check did not treat as
		// one is how a path escapes.
		return fmt.Errorf("%q holds a backslash", rel)
	}
	return nil
}

func validKind(kind string) bool {
	for _, k := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Platform is the `<goos>/<goarch>` key for this build.
func Platform(goos, goarch string) string { return goos + "/" + goarch }

// ExecutableFor answers with the path inside the bundle for a platform,
// or says that the bundle carries none.
func (m *Manifest) ExecutableFor(platform string) (string, error) {
	if rel, ok := m.Executables[platform]; ok {
		return rel, nil
	}
	have := make([]string, 0, len(m.Executables))
	for p := range m.Executables {
		have = append(have, p)
	}
	sort.Strings(have)
	return "", fmt.Errorf("%s %s carries no executable for %s; it has %s",
		m.Name, m.Version, platform, strings.Join(have, ", "))
}

// Encode renders the manifest as it is signed: sorted keys, two-space
// indent, one trailing newline.
//
// Byte-stable on purpose. The signature is over the Merkle root rather
// than over this, so the encoding does not have to be canonical for
// verification to work — but a manifest that re-encodes differently
// every time makes a diff between two bundles unreadable, and somebody
// will diff them.
func (m *Manifest) Encode() ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ParseManifest reads one and checks it.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the manifest is not readable: %w", err)
	}
	if err := m.Check(); err != nil {
		return nil, err
	}
	return &m, nil
}
