// Command sbom writes SPEC 4.3's software bill of materials for each
// release archive: CycloneDX, generated from the shipped binaries
// themselves rather than from the source tree -- "what linked, not what
// was declared".
//
// What linked is what `go version -m` prints, and that is
// debug/buildinfo: the module list, the toolchain and the build settings
// the linker embedded in the binary. This reads it from each bare binary
// `make cross` left in dist/, the same bytes disttar packs, so the SBOM
// describes exactly the binaries the archive carries. A go.mod or a
// vendor/modules.txt would describe what was asked for; a dependency
// that is required and never linked is not in a binary, and a binary is
// what runs.
//
// One document per archive, `halite-<version>-<os>-<arch>.cdx.json`
// beside it: the archive is the published artifact, so it is what an
// SBOM is checked against, and the three binaries in it are its
// components, each with the modules linked into it. Written before
// `make dist` sums dist/, so SHA256SUMS lists them, the release's
// attestation covers them, and `release-sign` signs them.
//
// Deterministic, because two builders must produce the same bytes or the
// release stops: the timestamp is SOURCE_DATE_EPOCH, the serial number is
// derived from the archive's digest, and every list is sorted.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

type options struct {
	dist, version, epoch, targets, binaries, fipsTargets string
}

func main() {
	var o options
	flag.StringVar(&o.dist, "dist", "dist", "directory holding the bare binaries and the archives")
	flag.StringVar(&o.version, "version", "", "the version, as stamped into the binaries")
	flag.StringVar(&o.epoch, "epoch", "", "SOURCE_DATE_EPOCH, in seconds; the documents' timestamp")
	flag.StringVar(&o.targets, "targets", "", "space-separated GOOS/GOARCH pairs")
	flag.StringVar(&o.binaries, "binaries", "", "space-separated binary names")
	flag.StringVar(&o.fipsTargets, "fips-targets", "", "space-separated GOOS/GOARCH pairs that also have a -fips archive")
	flag.Parse()
	made, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sbom:", err)
		os.Exit(1)
	}
	fmt.Printf("sbom: %d documents\n", len(made))
}

func run(o options) ([]string, error) {
	if o.version == "" {
		return nil, errors.New("-version is empty")
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(o.epoch), 10, 64)
	if err != nil || epoch <= 0 {
		return nil, fmt.Errorf("-epoch %q is not a positive SOURCE_DATE_EPOCH; refusing to fall back to the clock", o.epoch)
	}
	stamp := time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	targets, binaries := strings.Fields(o.targets), strings.Fields(o.binaries)
	if len(targets) == 0 || len(binaries) == 0 {
		return nil, errors.New("-targets and -binaries are both required")
	}

	type variant struct{ target, suffix string }
	var variants []variant
	for _, t := range targets {
		variants = append(variants, variant{t, ""})
	}
	for _, t := range strings.Fields(o.fipsTargets) {
		variants = append(variants, variant{t, "-fips"})
	}

	var made []string
	for _, v := range variants {
		t := v.target
		goos, goarch, ok := strings.Cut(t, "/")
		if !ok {
			return nil, fmt.Errorf("target %q is not GOOS/GOARCH", t)
		}
		archive := fmt.Sprintf("halite-%s-%s-%s%s.tar.gz", o.version, goos, goarch, v.suffix)
		ext := ""
		if goos == "windows" {
			archive = strings.TrimSuffix(archive, ".tar.gz") + ".zip"
			ext = ".exe"
		}
		archiveDigest, err := fileSHA256(filepath.Join(o.dist, archive))
		if err != nil {
			return nil, fmt.Errorf("%w; run disttar first", err)
		}
		var bins []binary
		for _, b := range binaries {
			path := filepath.Join(o.dist, fmt.Sprintf("%s%s-%s-%s%s", b, v.suffix, goos, goarch, ext))
			bin, err := readBinary(path, b+v.suffix+ext)
			if err != nil {
				return nil, err
			}
			bins = append(bins, bin)
		}
		doc := document(archive, archiveDigest, o.version, goos, goarch, stamp, bins)
		body, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimSuffix(archive, ".tar.gz"), ".zip") + ".cdx.json"
		if err := os.WriteFile(filepath.Join(o.dist, name), append(body, '\n'), 0o644); err != nil {
			return nil, err
		}
		made = append(made, name)
	}
	return made, nil
}

// binary is one shipped executable and what linked into it.
type binary struct {
	name   string // as it appears in the archive's bin/
	digest string
	info   *debug.BuildInfo
}

func readBinary(path, name string) (binary, error) {
	digest, err := fileSHA256(path)
	if err != nil {
		return binary{}, fmt.Errorf("%w; run make cross first", err)
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return binary{}, fmt.Errorf("%s: no Go build information (what `go version -m` reads): %w", path, err)
	}
	return binary{name: name, digest: digest, info: info}, nil
}

func fileSHA256(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// The CycloneDX 1.5 JSON shapes this writes, and only the fields it
// fills; `omitempty` keeps an absent field absent rather than null.
type (
	bom struct {
		BOMFormat    string       `json:"bomFormat"`
		SpecVersion  string       `json:"specVersion"`
		SerialNumber string       `json:"serialNumber"`
		Version      int          `json:"version"`
		Metadata     metadata     `json:"metadata"`
		Components   []component  `json:"components"`
		Dependencies []dependency `json:"dependencies"`
	}
	metadata struct {
		Timestamp string    `json:"timestamp"`
		Tools     tools     `json:"tools"`
		Component component `json:"component"`
	}
	tools struct {
		Components []component `json:"components"`
	}
	component struct {
		Type       string     `json:"type"`
		BOMRef     string     `json:"bom-ref,omitempty"`
		Name       string     `json:"name"`
		Version    string     `json:"version,omitempty"`
		PURL       string     `json:"purl,omitempty"`
		Hashes     []hash     `json:"hashes,omitempty"`
		Properties []property `json:"properties,omitempty"`
	}
	hash struct {
		Alg     string `json:"alg"`
		Content string `json:"content"`
	}
	property struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	dependency struct {
		Ref       string   `json:"ref"`
		DependsOn []string `json:"dependsOn,omitempty"`
	}
)

func document(archive, archiveDigest, version, goos, goarch, stamp string, bins []binary) bom {
	archiveRef := archive
	doc := bom{
		BOMFormat:    "CycloneDX",
		SpecVersion:  "1.5",
		SerialNumber: serialFor(archiveDigest),
		Version:      1,
		Metadata: metadata{
			Timestamp: stamp,
			Tools: tools{Components: []component{{
				Type: "application", Name: "halite tools/sbom", Version: version,
			}}},
			Component: component{
				Type: "file", BOMRef: archiveRef, Name: archive, Version: version,
				Hashes: []hash{{Alg: "SHA-256", Content: archiveDigest}},
				Properties: []property{
					{Name: "halite:goos", Value: goos},
					{Name: "halite:goarch", Value: goarch},
				},
			},
		},
	}

	modules := map[string]component{}
	archiveDeps := dependency{Ref: archiveRef}
	for _, b := range bins {
		ref := "bin/" + b.name
		archiveDeps.DependsOn = append(archiveDeps.DependsOn, ref)
		props := []property{{Name: "golang:goversion", Value: b.info.GoVersion}}
		if b.info.Path != "" {
			props = append(props, property{Name: "golang:path", Value: b.info.Path})
		}
		if b.info.Main.Version != "" {
			props = append(props, property{Name: "golang:mainversion", Value: b.info.Main.Version})
		}
		for _, s := range b.info.Settings {
			props = append(props, property{Name: "golang:build:" + s.Key, Value: s.Value})
		}
		doc.Components = append(doc.Components, component{
			Type: "application", BOMRef: ref, Name: b.name, Version: version,
			Hashes:     []hash{{Alg: "SHA-256", Content: b.digest}},
			Properties: props,
		})
		binDeps := dependency{Ref: ref}
		for _, m := range linked(b.info, version) {
			mref := purl(m)
			binDeps.DependsOn = append(binDeps.DependsOn, mref)
			if _, seen := modules[mref]; seen {
				continue
			}
			c := component{Type: "library", BOMRef: mref, Name: m.Path, Version: m.Version, PURL: mref}
			if m.Sum != "" {
				// The go.sum hash, a hash over the module's whole tree
				// rather than over one file, so it is a property and not a
				// CycloneDX `hashes` entry claiming to be SHA-256 of
				// something.
				c.Properties = []property{{Name: "golang:sum", Value: m.Sum}}
			}
			modules[mref] = c
		}
		sort.Strings(binDeps.DependsOn)
		doc.Dependencies = append(doc.Dependencies, binDeps)
	}
	refs := make([]string, 0, len(modules))
	for r := range modules {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	for _, r := range refs {
		doc.Components = append(doc.Components, modules[r])
		doc.Dependencies = append(doc.Dependencies, dependency{Ref: r})
	}
	sort.Strings(archiveDeps.DependsOn)
	doc.Dependencies = append([]dependency{archiveDeps}, doc.Dependencies...)
	return doc
}

// linked is every module the binary links, the main module included, with
// a replacement standing for what it replaced: what was compiled is the
// replacement.
//
// The main module is given the release's version. What Go records for it
// is not the product's: "(devel)" without version control, and otherwise
// a pseudo-version derived from the nearest tag in the clone, which in a
// pre-rewrite clone is the proof of concept's v0.12.0. The version the
// binaries are stamped with, and the archives named by, is this
// release's. What Go recorded is kept as the binary's
// `golang:mainversion` property.
func linked(info *debug.BuildInfo, version string) []debug.Module {
	var out []debug.Module
	if info.Main.Path != "" {
		main := info.Main
		main.Version = "v" + version
		out = append(out, main)
	}
	for _, d := range info.Deps {
		if d.Replace != nil {
			out = append(out, *d.Replace)
			continue
		}
		out = append(out, *d)
	}
	return out
}

// purl is the package URL of a Go module, the identifier CycloneDX tools
// match a component on.
func purl(m debug.Module) string {
	v := m.Version
	if v == "" {
		v = "(devel)"
	}
	return "pkg:golang/" + m.Path + "@" + v
}

// serialFor is a CycloneDX serial number derived from the archive's
// digest: unique to the archive, and the same on every builder that
// produced the same archive. The UUID is RFC 9562 version 8, the version
// for an application-defined layout.
func serialFor(digestHex string) string {
	b, _ := hex.DecodeString(digestHex)
	u := make([]byte, 16)
	copy(u, b)
	u[6] = (u[6] & 0x0f) | 0x80 // version 8
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	h := hex.EncodeToString(u)
	return fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
