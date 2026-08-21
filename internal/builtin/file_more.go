package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The rest of the file module's execution functions, SPEC section 15.2.
//
// `file` is the largest module and the most security-sensitive, and these
// are the functions a tree reaches for between the managed-file states:
// ownership and mode, directory creation and removal, path arithmetic,
// and the read-side queries a template asks before deciding what to do.
//
// Every path here is cleaned before use. None of them follows a symlink
// to somewhere the caller did not name without saying so.

func registerFileMore(r *Registries) {
	r.Exec.Add(
		// ---- ownership and mode ----
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "chown",
				Doc: "Set a path's owner and group.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
					opt("user", signature.String, "", "The owner. Empty leaves it alone."),
					opt("group", signature.String, "", "The group. Empty leaves it alone."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				uid, gid, err := resolveOwner(states.Str(args, "user", ""), states.Str(args, "group", ""))
				if err != nil {
					return nil, err
				}
				if err := os.Chown(path, uid, gid); err != nil {
					return nil, err
				}
				return path, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "chgrp",
				Doc: "Set a path's group.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
					req("group", signature.String, "The group."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				_, gid, err := resolveOwner("", states.Str(args, "group", ""))
				if err != nil {
					return nil, err
				}
				if err := os.Chown(path, -1, gid); err != nil {
					return nil, err
				}
				return path, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "chmod",
				Doc: "Set a path's mode.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
					req("mode", signature.Mode, "The mode, quoted, such as '0644'."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				mode, err := parseMode(states.Str(args, "mode", ""))
				if err != nil {
					return nil, err
				}
				if err := os.Chmod(path, mode); err != nil {
					return nil, err
				}
				return path, nil
			},
		},

		// ---- directories ----
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "mkdir",
				Doc: "Create a directory, and its parents.",
				Params: []signature.Param{
					req("path", signature.Path, "The directory."),
					opt("mode", signature.Mode, "0755", "The mode."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return makeDirs(args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "makedirs",
				Doc: "Create a path's parent directories, leaving the path itself alone.",
				Params: []signature.Param{
					req("path", signature.Path, "The path whose parents to create."),
					opt("mode", signature.Mode, "0755", "The mode."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				// Salt's makedirs creates the *parents* of the path, not
				// the path. A tree calling it with a file path expects the
				// directory it lives in to appear, and creating the file
				// path as a directory instead is how a later write fails
				// with "is a directory".
				mode, err := parseMode(states.Str(args, "mode", "0755"))
				if err != nil {
					return nil, err
				}
				parent := filepath.Dir(filepath.Clean(states.Str(args, "path", "")))
				if err := os.MkdirAll(parent, mode); err != nil {
					return nil, err
				}
				return parent, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "rmdir",
				Doc: "Remove an empty directory.",
				Params: []signature.Param{
					req("path", signature.Path, "The directory."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := filepath.Clean(states.Str(args, "path", ""))
				info, err := os.Lstat(path)
				if err != nil {
					return nil, err
				}
				if !info.IsDir() {
					return nil, fmt.Errorf("%s is not a directory; file.remove removes files", path)
				}
				// os.Remove on a directory removes it only when empty,
				// which is what rmdir means. A tree wanting the recursive
				// form asks for file.remove.
				if err := os.Remove(path); err != nil {
					return nil, err
				}
				return true, nil
			},
		},

		// ---- links and moves ----
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "move",
				Doc: "Move a path, across filesystems if need be.",
				Params: []signature.Param{
					req("src", signature.Path, "The source."),
					req("dst", signature.Path, "The destination."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				src, dst := states.Str(args, "src", ""), states.Str(args, "dst", "")
				if err := os.Rename(src, dst); err == nil {
					return dst, nil
				}
				// A rename across filesystems fails with EXDEV, and the
				// answer is a copy and a remove. Doing it silently is
				// right here: `mv` does the same, and a tree asking to
				// move a file does not care which filesystem it is on.
				data, err := os.ReadFile(src)
				if err != nil {
					return nil, err
				}
				info, err := os.Stat(src)
				if err != nil {
					return nil, err
				}
				if err := writeAtomic(dst, data, info.Mode().Perm()); err != nil {
					return nil, err
				}
				if err := os.Remove(src); err != nil {
					return nil, err
				}
				return dst, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "readlink",
				Doc: "Return what a symlink points at.",
				Params: []signature.Param{
					req("path", signature.Path, "The link."),
					opt("canonicalize", signature.Bool, false, "Resolve the whole chain to a real path."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				if states.Bool(args, "canonicalize", false) {
					return filepath.EvalSymlinks(path)
				}
				info, err := os.Lstat(path)
				if err != nil {
					return nil, err
				}
				if info.Mode()&os.ModeSymlink == 0 {
					return nil, fmt.Errorf("%s is not a symlink", path)
				}
				return os.Readlink(path)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "is_link",
				Doc: "Report whether a path is a symlink.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				info, err := os.Lstat(states.Str(args, "path", ""))
				if err != nil {
					return false, nil
				}
				return info.Mode()&os.ModeSymlink != 0, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "hardlink",
				Doc: "Create a hard link.",
				Params: []signature.Param{
					req("src", signature.Path, "The existing file."),
					req("path", signature.Path, "The new link."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				src, path := states.Str(args, "src", ""), states.Str(args, "path", "")
				if err := os.Link(src, path); err != nil {
					return nil, err
				}
				return path, nil
			},
		},

		// ---- queries ----
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "access",
				Doc: "Report whether a path exists and answers a test: f, d, r, w, x, or e.",
				Params: []signature.Param{
					req("path", signature.Path, "The path."),
					choice("mode", "e", "The test, as in test(1).", "e", "f", "d", "r", "w", "x", "l"),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return pathAccess(states.Str(args, "path", ""), states.Str(args, "mode", "e"))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "check_hash",
				Doc: "Report whether a file's hash matches. The algorithm is taken from the digest's length, or named as `sha256=...`.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					req("file_hash", signature.String, "The expected digest."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return checkFileHash(states.Str(args, "path", ""), states.Str(args, "file_hash", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "grep",
				Doc: "Return the lines of a file matching a regular expression.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					req("pattern", signature.String, "An RE2 regular expression. SPEC section 10.4."),
					opt("ignore_case", signature.Bool, false, "Match without regard to case."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				re, err := regexcompat.CompileWithFlags(
					states.Str(args, "pattern", ""), states.Bool(args, "ignore_case", false), false, false)
				if err != nil {
					return nil, err
				}
				data, err := os.ReadFile(states.Str(args, "path", ""))
				if err != nil {
					return nil, err
				}
				out := []any{}
				for _, ln := range strings.Split(string(data), "\n") {
					if re.MatchString(ln) {
						out = append(out, ln)
					}
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "find",
				Doc: "Find paths under a directory, by name pattern and type.",
				Params: []signature.Param{
					req("path", signature.Path, "Where to look."),
					opt("name", signature.String, "", "A shell glob the base name must match."),
					choice("type", "", "Restrict to one kind: f, d, or l.", "", "f", "d", "l"),
					opt("maxdepth", signature.Int, int64(-1), "How deep to go. -1 is unlimited."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return findPaths(args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "truncate",
				Doc: "Truncate a file to a length.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					opt("length", signature.Int, int64(0), "The new length in bytes."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path := states.Str(args, "path", "")
				if err := os.Truncate(path, states.Int(args, "length", 0)); err != nil {
					return nil, err
				}
				return path, nil
			},
		},

		// ---- path arithmetic, which a template uses far more than a
		// state does ----
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "join",
				Doc:      "Join path components, cleaning the result.",
				Params:   []signature.Param{req("parts", signature.List, "The components.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return filepath.Join(states.Strings(args, "parts")...), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "basename",
				Doc:      "Return the last component of a path.",
				Params:   []signature.Param{req("path", signature.Path, "The path.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return filepath.Base(states.Str(args, "path", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "dirname",
				Doc:      "Return everything but the last component of a path.",
				Params:   []signature.Param{req("path", signature.Path, "The path.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return filepath.Dir(states.Str(args, "path", "")), nil
			},
		},
	)
}

func makeDirs(args *value.Map) (any, error) {
	mode, err := parseMode(states.Str(args, "mode", "0755"))
	if err != nil {
		return nil, err
	}
	path := filepath.Clean(states.Str(args, "path", ""))
	if err := os.MkdirAll(path, mode); err != nil {
		return nil, err
	}
	// MkdirAll applies the mode only to directories it creates, and the
	// umask takes a bite out of that. The leaf gets the mode the caller
	// asked for either way.
	if err := os.Chmod(path, mode); err != nil {
		return nil, err
	}
	return path, nil
}

// pathAccess answers test(1)'s questions.
func pathAccess(path, mode string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, nil
	}
	switch mode {
	case "", "e":
		return true, nil
	case "l":
		return info.Mode()&os.ModeSymlink != 0, nil
	}
	// Everything else follows the link, as test(1) does.
	info, err = os.Stat(path)
	if err != nil {
		return false, nil
	}
	switch mode {
	case "f":
		return info.Mode().IsRegular(), nil
	case "d":
		return info.IsDir(), nil
	case "r":
		return accessible(path, os.O_RDONLY), nil
	case "w":
		return accessible(path, os.O_WRONLY), nil
	case "x":
		return info.Mode().Perm()&0o111 != 0, nil
	}
	return false, fmt.Errorf("unknown access test %q", mode)
}

// accessible answers by opening, rather than by reading the mode bits.
// The mode bits are not the answer: an ACL, a read-only mount, or a
// mandatory access control policy all decide this, and only the kernel
// knows about all three.
func accessible(path string, flag int) bool {
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// checkFileHash compares a file against a digest, taking the algorithm
// from an explicit prefix or from the digest's length.
func checkFileHash(path, expected string) (bool, error) {
	expected = strings.TrimSpace(expected)
	algorithm := ""
	if name, digest, ok := strings.Cut(expected, "="); ok {
		algorithm, expected = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(digest)
	}
	if algorithm == "" {
		switch len(expected) {
		case 64:
			algorithm = "sha256"
		case 96:
			algorithm = "sha384"
		case 128:
			algorithm = "sha512"
		case 32:
			return false, fmt.Errorf(
				"that is an MD5 digest, and MD5 collisions are cheap enough that it verifies nothing; " +
					"use the sha256 digest the same source almost certainly also publishes")
		case 40:
			return false, fmt.Errorf(
				"that is a SHA-1 digest, and SHA-1 collisions are within reach; " +
					"use the sha256 digest the same source almost certainly also publishes")
		default:
			return false, fmt.Errorf(
				"cannot tell which algorithm a %d-character digest is; write it as sha256=...", len(expected))
		}
	}
	got, err := hashFile(path, algorithm)
	if err != nil {
		return false, err
	}
	// A case-insensitive compare, because a digest copied from a vendor's
	// page is as likely to be upper case as lower.
	return strings.EqualFold(got, expected), nil
}

func findPaths(args *value.Map) (any, error) {
	root := filepath.Clean(states.Str(args, "path", ""))
	namePattern := states.Str(args, "name", "")
	kind := states.Str(args, "type", "")
	maxDepth := int(states.Int(args, "maxdepth", -1))

	if namePattern != "" {
		// A malformed glob is an error, not a pattern that matches
		// nothing: a tree with a typo should hear about it.
		if _, err := filepath.Match(namePattern, "probe"); err != nil {
			return nil, fmt.Errorf("name pattern %q: %w", namePattern, err)
		}
	}

	out := []any{}
	rootDepth := strings.Count(root, string(filepath.Separator))
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// An unreadable subtree is skipped rather than failing the
			// whole search: a find over /etc as a non-root user would
			// otherwise return nothing at all.
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if maxDepth >= 0 && strings.Count(p, string(filepath.Separator))-rootDepth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		switch kind {
		case "f":
			if !info.Mode().IsRegular() {
				return nil
			}
		case "d":
			if !info.IsDir() {
				return nil
			}
		case "l":
			if info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
		}
		if namePattern != "" {
			if ok, _ := filepath.Match(namePattern, filepath.Base(p)); !ok {
				return nil
			}
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
