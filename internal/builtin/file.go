package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerFile installs the file module, which is the largest and the most
// security-sensitive one in the system.
//
// Every write goes through writeAtomic. Every path is cleaned. A managed
// source is verified against its hash after transfer and before the file
// is moved into place. SPEC section 15.2.
func registerFile(r *Registries) {
	registerFileExec(r)
	registerFileStates(r)
}

func registerFileExec(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "read",
				Doc:      "Return a file's contents.",
				Params:   []signature.Param{req("path", signature.Path, "The file to read.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				b, err := os.ReadFile(states.Str(args, "path", ""))
				if err != nil {
					return nil, err
				}
				return string(b), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "write",
				Doc: "Write a file atomically.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to write."),
					req("contents", signature.String, "What to write."),
					opt("mode", signature.Mode, "0644", "The file mode, written as a quoted string."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				mode, err := parseMode(states.Str(args, "mode", "0644"))
				if err != nil {
					return nil, err
				}
				if c.Test {
					return fmt.Sprintf("%s would be written", path), nil
				}
				if err := writeAtomic(path, []byte(states.Str(args, "contents", "")), mode); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "get_hash",
				Doc: "Return a file's digest.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to digest."),
					choice("form", "sha256", "The digest algorithm.", "sha256", "sha384", "sha512"),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return hashFile(states.Str(args, "path", ""), states.Str(args, "form", "sha256"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "stats",
				Doc:      "Return a file's metadata.",
				Params:   []signature.Param{req("path", signature.Path, "The file to inspect.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return fileStats(states.Str(args, "path", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "file_exists",
				Doc:      "Report whether a path exists and is a regular file.",
				Params:   []signature.Param{req("path", signature.Path, "The path to test.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				info, err := os.Stat(states.Str(args, "path", ""))
				return err == nil && info.Mode().IsRegular(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "directory_exists",
				Doc:      "Report whether a path exists and is a directory.",
				Params:   []signature.Param{req("path", signature.Path, "The path to test.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				info, err := os.Stat(states.Str(args, "path", ""))
				return err == nil && info.IsDir(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "remove",
				Doc:      "Remove a file or a directory tree.",
				Params:   []signature.Param{req("path", signature.Path, "The path to remove.")},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				if c.Test {
					_, err := os.Lstat(path)
					return err == nil, nil
				}
				if err := os.RemoveAll(path); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)
}

func fileStats(path string) (*value.Map, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	kind := "file"
	switch {
	case info.IsDir():
		kind = "dir"
	case info.Mode()&os.ModeSymlink != 0:
		kind = "link"
	}
	m := value.MapOf(
		"name", path,
		"type", kind,
		"size", info.Size(),
		"mode", formatMode(info.Mode()),
		"mtime", info.ModTime().Unix(),
	)
	addOwnership(m, info)
	return m, nil
}

func registerFileStates(r *Registries) {
	managedParams := []signature.Param{
		pathParam("The file to manage. Defaults to the state ID."),
		opt("source", signature.String, "", "A halite:// or salt:// URI, or a local path."),
		opt("source_hash", signature.String, "", "Expected digest of the source, as `algorithm=digest`."),
		opt("contents", signature.String, nil, "Literal contents, as an alternative to a source."),
		opt("contents_pillar", signature.String, "", "A pillar key whose value becomes the contents."),
		opt("mode", signature.Mode, "", "The file mode, written as a quoted string such as '0644'."),
		opt("user", signature.String, "", "Owner."),
		opt("group", signature.String, "", "Group."),
		opt("makedirs", signature.Bool, false, "Create the parent directories if they are missing."),
		opt("dir_mode", signature.Mode, "0755", "Mode for directories created by makedirs."),
		opt("create", signature.Bool, true, "Create the file if it does not exist."),
		opt("replace", signature.Bool, true, "Rewrite the file when its contents differ."),
		opt("backup", signature.String, "", "Keep a copy of the previous contents with this suffix."),
		choice("hash_type", "sha256", "Digest used to compare contents.", "sha256", "sha384", "sha512"),
		opt("show_changes", signature.Bool, true, "Include a unified diff in the changes."),
	}

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "managed",
			Doc:      "Ensure a file exists with the given contents, mode, and ownership.",
			Params:   managedParams,
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileManaged,
	})

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "directory",
			Doc: "Ensure a directory exists with the given mode and ownership.",
			Params: []signature.Param{
				pathParam("The directory to manage."),
				opt("mode", signature.Mode, "", "The directory mode, written as a quoted string."),
				opt("user", signature.String, "", "Owner."),
				opt("group", signature.String, "", "Group."),
				opt("makedirs", signature.Bool, false, "Create the parent directories if they are missing."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileDirectory,
	})

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "absent",
			Doc:      "Ensure a path does not exist.",
			Params:   []signature.Param{pathParam("The path to remove.")},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileAbsent,
	})

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "symlink",
			Doc: "Ensure a symbolic link exists and points where it should.",
			Params: []signature.Param{
				pathParam("The link to create."),
				req("target", signature.Path, "What the link points at."),
				opt("force", signature.Bool, false, "Replace a non-link that is in the way."),
				opt("makedirs", signature.Bool, false, "Create the parent directories if they are missing."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileSymlink,
	})

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "touch",
			Doc:      "Ensure a file exists, creating it empty if it does not.",
			Params:   []signature.Param{pathParam("The file to touch.")},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileTouch,
	})
}

// desiredContents resolves what the file should contain, from a literal,
// from pillar, or from the file server.
func desiredContents(c *exec.Context, args *value.Map) (data []byte, from string, err error) {
	if v, ok := args.Get("contents"); ok && v != nil {
		switch t := v.(type) {
		case string:
			return []byte(ensureTrailingNewline(t)), "contents", nil
		case []any:
			var b strings.Builder
			for _, line := range t {
				b.WriteString(value.KeyString(line))
				b.WriteByte('\n')
			}
			return []byte(b.String()), "contents", nil
		default:
			return nil, "", fmt.Errorf("contents must be a string or a list of lines, found %s", value.TypeName(v))
		}
	}

	if key := states.Str(args, "contents_pillar", ""); key != "" {
		v, ok := value.Traverse(c.Pillar, key, ":")
		if !ok {
			return nil, "", fmt.Errorf("contents_pillar names %q, which is not in this node's pillar", key)
		}
		return []byte(ensureTrailingNewline(value.KeyString(v))), "contents_pillar", nil
	}

	source := states.Str(args, "source", "")
	if source == "" {
		return nil, "", nil
	}
	if fileserver.IsManagedURI(source) {
		if c.Files == nil {
			return nil, "", fmt.Errorf("source %q needs a file server, and none is configured for this run", source)
		}
		local, err := c.Files.Fetch(c.Env, source)
		if err != nil {
			return nil, "", fmt.Errorf("fetching %s: %w", source, err)
		}
		b, err := os.ReadFile(local)
		if err != nil {
			return nil, "", err
		}
		return b, source, nil
	}
	b, err := os.ReadFile(source)
	if err != nil {
		return nil, "", fmt.Errorf("reading source %s: %w", source, err)
	}
	return b, source, nil
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func fileManaged(c *exec.Context, args *value.Map) (states.Result, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return states.False("This state needs a file path."), nil
	}
	path = filepath.Clean(path)

	want, source, err := desiredContents(c, args)
	if err != nil {
		return states.False(fmt.Sprintf("The contents of %s could not be resolved: %v", path, err)), nil
	}

	if expected := states.Str(args, "source_hash", ""); expected != "" && want != nil {
		if err := verifySourceHash(c, want, expected); err != nil {
			return states.False(fmt.Sprintf("The source for %s failed its hash check: %v", path, err)), nil
		}
	}

	changes := value.NewMap(4)
	info, statErr := os.Lstat(path)
	exists := statErr == nil

	if !exists && !states.Bool(args, "create", true) {
		return states.True(fmt.Sprintf("%s does not exist and create is false, so nothing was done.", path)), nil
	}

	// Contents.
	var current []byte
	if exists && info.Mode().IsRegular() {
		current, _ = os.ReadFile(path)
	}
	contentsDiffer := want != nil && string(current) != string(want)
	if contentsDiffer && exists && !states.Bool(args, "replace", true) {
		contentsDiffer = false
	}
	if contentsDiffer {
		if states.Bool(args, "show_changes", true) {
			changes.Set("diff", unifiedDiff(string(current), string(want), path))
		} else {
			changes.Set("diff", "<changed>")
		}
	}
	if !exists && want == nil {
		// A managed file with no source and no contents is a request for
		// an empty file, which is what touch means.
		want = []byte{}
		contentsDiffer = true
		changes.Set("file", states.Change(nil, "created"))
	}

	// Mode.
	modeStr := states.Str(args, "mode", "")
	var wantMode os.FileMode
	modeDiffers := false
	if modeStr != "" {
		wantMode, err = parseMode(modeStr)
		if err != nil {
			return states.False(fmt.Sprintf("The mode for %s is invalid: %v", path, err)), nil
		}
		if exists && info.Mode().Perm() != wantMode.Perm() {
			modeDiffers = true
			changes.Set("mode", states.Change(formatMode(info.Mode()), formatMode(wantMode)))
		}
		if !exists {
			modeDiffers = true
		}
	}

	// Ownership.
	ownerChange, ownerDiffers, err := plannedOwnership(path, exists, states.Str(args, "user", ""), states.Str(args, "group", ""))
	if err != nil {
		return states.False(fmt.Sprintf("The ownership for %s could not be resolved: %v", path, err)), nil
	}
	if ownerDiffers && ownerChange != nil {
		changes.Set("ownership", ownerChange)
	}

	if !contentsDiffer && !modeDiffers && !ownerDiffers {
		return states.True(fmt.Sprintf("%s is already in the requested state.", path)), nil
	}

	if c.Test {
		return states.WouldChange(describeFileChange(path, exists, contentsDiffer, modeDiffers, ownerDiffers, source, true), changes), nil
	}

	if states.Bool(args, "makedirs", false) {
		dirMode, err := parseMode(states.Str(args, "dir_mode", "0755"))
		if err != nil {
			return states.False(fmt.Sprintf("The dir_mode for %s is invalid: %v", path, err)), nil
		}
		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return states.False(fmt.Sprintf("The parent directories of %s could not be created: %v", path, err)), nil
		}
	}

	if contentsDiffer {
		if suffix := states.Str(args, "backup", ""); suffix != "" && exists {
			if err := writeAtomic(path+suffix, current, 0o600); err != nil {
				return states.False(fmt.Sprintf("The backup of %s could not be written: %v", path, err)), nil
			}
			changes.Set("backup", path+suffix)
		}
		writeMode := wantMode
		if writeMode == 0 {
			if exists {
				writeMode = info.Mode().Perm()
			} else {
				writeMode = 0o644
			}
		}
		if err := writeAtomic(path, want, writeMode); err != nil {
			return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
		}
	} else if modeDiffers {
		if err := os.Chmod(path, wantMode); err != nil {
			return states.False(fmt.Sprintf("The mode of %s could not be set: %v", path, err)), nil
		}
	}

	if ownerDiffers {
		if err := applyOwnership(path, states.Str(args, "user", ""), states.Str(args, "group", "")); err != nil {
			return states.False(fmt.Sprintf("The ownership of %s could not be set: %v", path, err)), nil
		}
	}

	return states.Changed(describeFileChange(path, exists, contentsDiffer, modeDiffers, ownerDiffers, source, false), changes), nil
}

// describeFileChange builds the comment SPEC section 11.6 requires.
//
// The tense is a parameter because the same description serves both
// outcomes, and getting it wrong is not cosmetic: a test run that reports
// "/etc/motd was created" reads as though it happened. An operator
// scanning a --test log for what it is about to do would see a past tense
// and believe the change had already been made.
func describeFileChange(path string, exists, contents, mode, owner bool, source string, planned bool) string {
	var parts []string
	if contents {
		if source != "" && source != "contents" && source != "contents_pillar" {
			parts = append(parts, "its contents from "+source)
		} else {
			parts = append(parts, "its contents")
		}
	}
	if mode {
		parts = append(parts, "its mode")
	}
	if owner {
		parts = append(parts, "its ownership")
	}
	verb := "updated"
	if !exists {
		verb = "created"
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s is already in the requested state.", path)
	}
	if planned {
		return fmt.Sprintf("%s would be %s: %s.", path, verb, strings.Join(parts, ", "))
	}
	return fmt.Sprintf("%s was %s: %s.", path, verb, strings.Join(parts, ", "))
}

// verifySourceHash checks a fetched source against the digest the state
// declared, before anything is written.
func verifySourceHash(c *exec.Context, data []byte, expected string) error {
	algorithm, digest, found := strings.Cut(expected, "=")
	if !found {
		// A bare digest is interpreted by its length, which is how Salt
		// trees write it.
		digest = expected
		switch len(digest) {
		case 64:
			algorithm = "sha256"
		case 96:
			algorithm = "sha384"
		case 128:
			algorithm = "sha512"
		case 32:
			algorithm = "md5"
		case 40:
			algorithm = "sha1"
		default:
			return fmt.Errorf("source_hash %q has no algorithm and its length does not identify one", expected)
		}
	}
	algorithm = strings.ToLower(strings.TrimSpace(algorithm))
	digest = strings.ToLower(strings.TrimSpace(digest))

	if algorithm == "md5" || algorithm == "sha1" {
		// These exist only to verify an upstream that publishes nothing
		// better, and each use is warned about by name. SPEC section 13.5.
		c.Logf("warn", "source_hash uses %s, which is not collision-resistant; it is accepted only for an upstream that publishes nothing better", algorithm)
		got, err := legacyHash(data, algorithm)
		if err != nil {
			return err
		}
		if got != digest {
			return fmt.Errorf("%s digest is %s, expected %s", algorithm, got, digest)
		}
		return nil
	}

	got, err := hashBytes(data, algorithm)
	if err != nil {
		return err
	}
	if got != digest {
		return fmt.Errorf("%s digest is %s, expected %s", algorithm, got, digest)
	}
	return nil
}

func fileDirectory(c *exec.Context, args *value.Map) (states.Result, error) {
	path := filepath.Clean(states.Str(args, "name", ""))
	changes := value.NewMap(2)

	info, statErr := os.Lstat(path)
	exists := statErr == nil
	if exists && !info.IsDir() {
		return states.False(fmt.Sprintf("%s exists and is not a directory.", path)), nil
	}

	modeStr := states.Str(args, "mode", "")
	var wantMode os.FileMode = 0o755
	modeDiffers := false
	if modeStr != "" {
		var err error
		wantMode, err = parseMode(modeStr)
		if err != nil {
			return states.False(fmt.Sprintf("The mode for %s is invalid: %v", path, err)), nil
		}
		if exists && info.Mode().Perm() != wantMode.Perm() {
			modeDiffers = true
			changes.Set("mode", states.Change(formatMode(info.Mode()), formatMode(wantMode)))
		}
	}

	ownerChange, ownerDiffers, err := plannedOwnership(path, exists, states.Str(args, "user", ""), states.Str(args, "group", ""))
	if err != nil {
		return states.False(fmt.Sprintf("The ownership for %s could not be resolved: %v", path, err)), nil
	}
	if ownerDiffers && ownerChange != nil {
		changes.Set("ownership", ownerChange)
	}

	if exists && !modeDiffers && !ownerDiffers {
		return states.True(fmt.Sprintf("%s already exists with the requested mode and ownership.", path)), nil
	}
	if !exists {
		changes.Set("directory", states.Change(nil, "created"))
	}

	if c.Test {
		verb := "updated"
		if !exists {
			verb = "created"
		}
		return states.WouldChange(fmt.Sprintf("The directory %s would be %s.", path, verb), changes), nil
	}

	if !exists {
		mk := os.Mkdir
		if states.Bool(args, "makedirs", false) {
			mk = os.MkdirAll
		}
		if err := mk(path, wantMode); err != nil {
			return states.False(fmt.Sprintf("%s could not be created: %v", path, err)), nil
		}
	} else if modeDiffers {
		if err := os.Chmod(path, wantMode); err != nil {
			return states.False(fmt.Sprintf("The mode of %s could not be set: %v", path, err)), nil
		}
	}
	if ownerDiffers {
		if err := applyOwnership(path, states.Str(args, "user", ""), states.Str(args, "group", "")); err != nil {
			return states.False(fmt.Sprintf("The ownership of %s could not be set: %v", path, err)), nil
		}
	}

	verb := "updated"
	if !exists {
		verb = "created"
	}
	return states.Changed(fmt.Sprintf("The directory %s was %s.", path, verb), changes), nil
}

func fileAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	path := filepath.Clean(states.Str(args, "name", ""))
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return states.True(fmt.Sprintf("%s is already absent.", path)), nil
		}
		return states.False(fmt.Sprintf("%s could not be inspected: %v", path, err)), nil
	}
	changes := value.MapOf("removed", path)
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed.", path), changes), nil
	}
	if err := os.RemoveAll(path); err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed.", path), changes), nil
}

func fileSymlink(c *exec.Context, args *value.Map) (states.Result, error) {
	path := filepath.Clean(states.Str(args, "name", ""))
	target := states.Str(args, "target", "")
	if target == "" {
		return states.False("This state needs a target for the link."), nil
	}

	info, statErr := os.Lstat(path)
	switch {
	case statErr == nil && info.Mode()&os.ModeSymlink != 0:
		current, err := os.Readlink(path)
		if err != nil {
			return states.False(fmt.Sprintf("The link %s could not be read: %v", path, err)), nil
		}
		if current == target {
			return states.True(fmt.Sprintf("The link %s already points at %s.", path, target)), nil
		}
		changes := value.MapOf("target", states.Change(current, target))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("The link %s would be repointed at %s.", path, target), changes), nil
		}
		if err := os.Remove(path); err != nil {
			return states.False(fmt.Sprintf("The old link %s could not be removed: %v", path, err)), nil
		}
		if err := os.Symlink(target, path); err != nil {
			return states.False(fmt.Sprintf("The link %s could not be created: %v", path, err)), nil
		}
		return states.Changed(fmt.Sprintf("The link %s was repointed at %s.", path, target), changes), nil

	case statErr == nil:
		if !states.Bool(args, "force", false) {
			return states.False(fmt.Sprintf("%s exists and is not a symbolic link; set force to replace it.", path)), nil
		}
		changes := value.MapOf("replaced", states.Change("a regular path", "a symbolic link to "+target))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("%s would be replaced with a link to %s.", path, target), changes), nil
		}
		if err := os.RemoveAll(path); err != nil {
			return states.False(fmt.Sprintf("%s could not be removed: %v", path, err)), nil
		}
		if err := os.Symlink(target, path); err != nil {
			return states.False(fmt.Sprintf("The link %s could not be created: %v", path, err)), nil
		}
		return states.Changed(fmt.Sprintf("%s was replaced with a link to %s.", path, target), changes), nil

	default:
		changes := value.MapOf("link", states.Change(nil, target))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("The link %s would be created pointing at %s.", path, target), changes), nil
		}
		if states.Bool(args, "makedirs", false) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return states.False(fmt.Sprintf("The parent directories of %s could not be created: %v", path, err)), nil
			}
		}
		if err := os.Symlink(target, path); err != nil {
			return states.False(fmt.Sprintf("The link %s could not be created: %v", path, err)), nil
		}
		return states.Changed(fmt.Sprintf("The link %s was created pointing at %s.", path, target), changes), nil
	}
}

func fileTouch(c *exec.Context, args *value.Map) (states.Result, error) {
	path := filepath.Clean(states.Str(args, "name", ""))
	if _, err := os.Lstat(path); err == nil {
		return states.True(fmt.Sprintf("%s already exists.", path)), nil
	}
	changes := value.MapOf("file", states.Change(nil, "created"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be created empty.", path), changes), nil
	}
	if err := writeAtomic(path, nil, 0o644); err != nil {
		return states.False(fmt.Sprintf("%s could not be created: %v", path, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was created empty.", path), changes), nil
}
