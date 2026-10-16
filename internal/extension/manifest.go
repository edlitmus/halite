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
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/edlitmus/halite/ext"
)

// ManifestName is the file inside a bundle that describes it.
const ManifestName = "manifest.json"

// SignatureName is the detached signature over the bundle's signing
// root, which covers the manifest's fields as well as its files.
const SignatureName = "manifest.sig"

// Kinds are the extension kinds of SPEC 24.2.
//
// The host's list *is* the SDK's list, rather than a second copy of it.
// There were two, and a test that read like it held them together and
// could not: `TestTheKindsAreConsistent` walked `ext.Kinds` asserting
// `ext.ValidKind` of each, and `ValidKind` is `slices.Contains(Kinds, …)`
// -- so it could not fail whatever either list said, while its own comment
// claimed it checked that every kind the SDK names "is one the host will
// accept". It never looked at the host.
//
// One list rather than a better test, because `ext` imports nothing
// internal and several internal packages already import it, so there was
// never a reason for the second. A kind the host accepted and the SDK did
// not would have been an extension refused at handshake by a name its
// author read out of the SDK. DIVERGENCE 5.144.
var Kinds = ext.Kinds

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
	// the file half of the signing root is computed over.
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
		if err := checkRunnableName(platform, rel); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
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
// A manifest is signed (SigningRoot covers every field), so a hostile
// one needs a trusted key — but a *mistaken* one does not, and neither does a bundle whose author was
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

// Encode renders the manifest for the bundle: sorted keys, two-space
// indent, one trailing newline.
//
// Byte-stable on purpose. The signature is over SigningRoot, which is
// computed from the parsed fields rather than from these bytes, so the
// encoding does not have to be canonical for verification to work — but
// a manifest that re-encodes differently every time makes a diff between
// two bundles unreadable, and somebody will diff them.
func (m *Manifest) Encode() ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ParseManifest reads one and checks it.
//
// A field this build does not know is refused, not ignored. SigningRoot
// is computed from the fields this build parses, so a field it skips is
// outside the signature, and a later build that gave the field a meaning
// would act on something nobody signed. Before DIVERGENCE 5.331 the whole
// manifest was outside the signature, and tolerating unknown fields cost
// nothing because nothing was covered anyway.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the manifest is not readable: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("the manifest is not readable: it holds more than one JSON value")
	}
	if err := m.Check(); err != nil {
		return nil, err
	}
	return &m, nil
}

// SigningRoot is what a bundle's signature covers and what a pin's root
// fixes: the Merkle root of the files and a digest of every other field,
// combined under a domain of their own.
//
//	SHA-256("halite-extension-root-v2\x00" || MerkleRoot(files) || SHA-256(metadata))
//
// It used to be MerkleRoot(files) alone. That signed the bytes of every
// file and nothing the manifest said about them: `declares`, which is
// what the sandbox grants (`root`, `network`); `executables`, which signed
// file runs; and `name`, `version` and `kind`, which say what it is.
// Anyone who could write `manifest.json` could change any of those under a
// valid signature and a matching pin. Issue 321 finding H-02; DIVERGENCE
// 5.331.
//
// The file tree stays a tree of its own, so two bundles that share files
// still share subtrees (SPEC 24.4), and the metadata is one more node
// above it rather than a leaf among the files: no file path can be
// spelled so that its leaf collides with it.
func (m *Manifest) SigningRoot() ([]byte, error) {
	files, err := MerkleRoot(m.Files)
	if err != nil {
		return nil, err
	}
	meta := sha256.Sum256(m.metadata())
	h := sha256.New()
	h.Write([]byte("halite-extension-root-v2\x00"))
	h.Write(files)
	h.Write(meta[:])
	return h.Sum(nil), nil
}

// metadata is the canonical encoding of every field but Files.
//
// Built from the parsed values, never from the bytes on disk, so two
// manifests that mean the same thing sign the same way however they were
// formatted, and nothing outside these fields can be signed by accident.
// Each value is length-prefixed, so a byte cannot move across a field
// boundary without changing the encoding. `declares` and `executables`
// are sets to the code that reads them, so they are sorted: the order a
// publisher wrote them in is not something to sign.
//
// A field added to Manifest has to be added here. TestTheMetadataCoversEveryField
// fails until it is, because a field outside this encoding is a field
// outside the signature.
func (m *Manifest) metadata() []byte {
	var b bytes.Buffer
	b.WriteString("halite-extension-manifest-v2\x00")
	str := func(s string) {
		var n [binary.MaxVarintLen64]byte
		b.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
		b.WriteString(s)
	}
	count := func(n int) {
		var c [binary.MaxVarintLen64]byte
		b.Write(c[:binary.PutUvarint(c[:], uint64(n))])
	}
	str("name")
	str(m.Name)
	str("version")
	str(m.Version)
	str("kind")
	str(m.Kind)
	str("description")
	str(m.Description)
	str("declares")
	declares := append([]string(nil), m.Declares...)
	sort.Strings(declares)
	count(len(declares))
	for _, d := range declares {
		str(d)
	}
	str("executables")
	platforms := make([]string, 0, len(m.Executables))
	for p := range m.Executables {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	count(len(platforms))
	for _, p := range platforms {
		str(p)
		str(m.Executables[p])
	}
	return b.Bytes()
}

// windowsRunnable are the extensions Windows will start a file by.
//
// The list is fixed rather than read from PATHEXT, because this check
// runs where the bundle is built and PATHEXT there says nothing about
// the node that will run it. These four are the ones every Windows has
// had; a node with a longer PATHEXT loses nothing, since a bundle that
// passes here runs everywhere.
var windowsRunnable = []string{".exe", ".com", ".bat", ".cmd"}

// checkRunnableName refuses an executable the named platform cannot
// start.
//
// Windows decides what a file is by its extension: CreateProcess will
// not run `run`, and the error Go reports for it is
//
//	exec: "C:\...\run": executable file not found in %PATH%
//
// which names an absolute path and then says it is not on PATH. On a
// node that reads as a bug in halite rather than as a bundle that was
// built wrong, and it arrives at the worst moment — when a state calls
// the extension — rather than when the bundle was published. It is
// checkable at build time from the platform key alone, so it is checked
// there.
func checkRunnableName(platform, rel string) error {
	goos, _, _ := strings.Cut(platform, "/")
	if goos != "windows" {
		return nil
	}
	lower := strings.ToLower(rel)
	for _, ext := range windowsRunnable {
		if strings.HasSuffix(lower, ext) {
			return nil
		}
	}
	return fmt.Errorf(
		"the %s executable %q has no extension Windows can start; "+
			"name it %s.exe, or one of %s",
		platform, rel, strings.TrimSuffix(rel, path.Ext(rel)),
		strings.Join(windowsRunnable, ", "))
}
