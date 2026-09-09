package builtin

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// mac_keychain, SPEC section 15.3's macOS row.
//
// It drives `security(1)`. SPEC 15.5 names no state for it, so it ships
// as execution functions, matching Salt's `keychain` module: a tree that
// wants a certificate present reaches `install`/`uninstall` from
// `module.run` with an `unless` that calls `list_certs` or `get_hash`.
//
// **`security import` takes the passphrase in the argument vector.**
// There is no standard-input form — `-P <passphrase>` is the only way in
// — so for as long as the import runs the passphrase of the PKCS#12 file
// is readable in the process table by any account on the machine. That
// is `security`'s design, not a choice here, and it is the same shape
// `mac_shadow.set_password` and Salt's own `keychain.install` carry. A
// tree that cannot accept that installs the certificate out of band.
//
// **Names come from the `labl` attribute.** `security find-certificate`
// prints each certificate's attributes as `"labl"<blob>="<name>"`, or
// as `"labl"<blob>=0x...` hex when the name is not plain ASCII; both are
// read. `list_certs` sorts and de-duplicates, because a keychain
// routinely holds a leaf and its issuer under related names and a caller
// asking "what is in here" wants the set.
func registerMacKeychain(r *Registries) {
	keychain := opt("keychain", signature.String, "",
		"The keychain file. The search list is used when omitted.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "list_keychains",
				Doc: "Return the keychain files on the search list, or of one domain.",
				Params: []signature.Param{
					opt("domain", signature.String, "", "user, system, common or dynamic. The search list when omitted."),
				},
				Returns:   "a list of keychain file paths",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macKeychainList(c, states.Str(args, "domain", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "default_keychain",
				Doc:       "Return the path of the default keychain.",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				paths, err := macKeychainPaths(c, "default-keychain")
				if err != nil {
					return nil, err
				}
				if len(paths) == 0 {
					return "", nil
				}
				return paths[0], nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "list_certs",
				Doc:       "Return the certificate names in a keychain, sorted and de-duplicated.",
				Params:    []signature.Param{keychain},
				Returns:   "a list of certificate label names",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				certs, err := macKeychainCerts(c, states.Str(args, "keychain", ""), "")
				if err != nil {
					return nil, err
				}
				seen := map[string]bool{}
				var names []string
				for _, c := range certs {
					if !seen[c.Name] {
						seen[c.Name] = true
						names = append(names, c.Name)
					}
				}
				sort.Strings(names)
				out := make([]any, len(names))
				for i, n := range names {
					out[i] = n
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "get_hash",
				Doc: "Return the SHA-1 hash of the certificate with the given name. " +
					"Empty when there is no such certificate.",
				Params:    []signature.Param{req("name", signature.String, "The certificate name."), keychain},
				Returns:   "the SHA-1 hash as uppercase hex, or an empty string",
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				certs, err := macKeychainCerts(c, states.Str(args, "keychain", ""), states.Str(args, "name", ""))
				if err != nil {
					return nil, err
				}
				for _, cert := range certs {
					if cert.Hash != "" {
						return cert.Hash, nil
					}
				}
				return "", nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "friendly_name",
				Doc: "Return the friendlyName inside a PKCS#12 file. This shells out to " +
					"`openssl`, and the file's passphrase is in the process table while it runs.",
				Params: []signature.Param{
					req("path", signature.Path, "The .p12 or .pfx file."),
					req("password", signature.String, "The file's passphrase, in plaintext."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: macOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return macKeychainFriendlyName(c, states.Str(args, "path", ""), states.Str(args, "password", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "install",
				Doc: "Import a PKCS#12 certificate and key into a keychain.",
				Params: []signature.Param{
					req("path", signature.Path, "The .p12 or .pfx file."),
					req("password", signature.String, "The file's passphrase, in plaintext. See the note above about the process table."),
					keychain,
					opt("allow_any", signature.Bool, false, "Pass -A, so any application may use the key without prompting. A broad grant; off by default."),
					opt("apps", signature.List, nil, "Absolute paths of applications allowed to use the key, each passed as -T."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macKeychainInstall(c,
					states.Str(args, "path", ""), states.Str(args, "password", ""),
					states.Str(args, "keychain", ""), states.Bool(args, "allow_any", false),
					states.Strings(args, "apps"))
				return err == nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mac_keychain", Function: "uninstall",
				Doc: "Remove a certificate from a keychain by name.",
				Params: []signature.Param{
					req("name", signature.String, "The certificate name, as `list_certs` returns it."),
					keychain,
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  macOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := macKeychainUninstall(c, states.Str(args, "name", ""), states.Str(args, "keychain", ""))
				return err == nil, err
			},
		},
	)
}

func macSecurityBin(c *exec.Context) string { return c.Which("security") }

func macSecurityRequire(c *exec.Context) error {
	if macSecurityBin(c) == "" {
		return fmt.Errorf("mac_keychain: `security` was not found; this build's macOS keychain module needs it")
	}
	return nil
}

// macKeychainPaths runs a `security` verb that prints quoted keychain
// paths and returns them.
func macKeychainPaths(c *exec.Context, verb string, extra ...string) ([]any, error) {
	if err := macSecurityRequire(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: append([]string{"security", verb}, extra...), IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("security %s: %s", verb, firstLine(res.Stderr+res.Stdout))
	}
	var out []any
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`) && len(line) >= 2 {
			out = append(out, line[1:len(line)-1])
		}
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

func macKeychainList(c *exec.Context, domain string) ([]any, error) {
	extra := []string{}
	if domain != "" {
		extra = append(extra, "-d", domain)
	}
	return macKeychainPaths(c, "list-keychains", extra...)
}

// keychainCert is one certificate read from `security find-certificate`.
type keychainCert struct {
	Name string
	Hash string
}

// macKeychainCerts runs `security find-certificate -a -Z` and pairs each
// SHA-1 hash line with the `labl` attribute that follows it. A non-empty
// name limits the search to that certificate.
func macKeychainCerts(c *exec.Context, keychain, name string) ([]keychainCert, error) {
	if err := macSecurityRequire(c); err != nil {
		return nil, err
	}
	argv := []string{"security", "find-certificate", "-a", "-Z"}
	if name != "" {
		argv = []string{"security", "find-certificate", "-c", name, "-Z"}
	}
	if keychain != "" {
		argv = append(argv, keychain)
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	// Exit 44 is "nothing matched", which is an answer, not a failure.
	if res.Code != 0 && res.Code != 44 && !strings.Contains(res.Stderr+res.Stdout, "could not be found") {
		return nil, fmt.Errorf("security find-certificate: %s", firstLine(res.Stderr+res.Stdout))
	}
	return parseFindCertificate(res.Stdout), nil
}

// parseFindCertificate reads the `SHA-1 hash:` and `"labl"` lines out of
// `security find-certificate -Z` output.
func parseFindCertificate(out string) []keychainCert {
	var certs []keychainCert
	var pendingHash string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if h, ok := strings.CutPrefix(trimmed, "SHA-1 hash:"); ok {
			pendingHash = strings.TrimSpace(h)
			continue
		}
		if v, ok := strings.CutPrefix(trimmed, `"labl"<blob>=`); ok {
			certs = append(certs, keychainCert{Name: keychainBlobValue(v), Hash: pendingHash})
			pendingHash = ""
		}
	}
	return certs
}

// keychainBlobValue decodes the right-hand side of a `security` attribute
// line: a quoted string, or `0x...` hex when the value is not ASCII.
func keychainBlobValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		v = strings.TrimPrefix(v, `"`)
		if i := strings.LastIndex(v, `"`); i >= 0 {
			v = v[:i]
		}
		return v
	}
	if h, ok := strings.CutPrefix(v, "0x"); ok {
		// The line is often `0x414243 "ABC"` — the hex then the ASCII
		// rendering. Take the hex up to the first space.
		if sp := strings.IndexByte(h, ' '); sp >= 0 {
			h = h[:sp]
		}
		if b, err := hex.DecodeString(h); err == nil {
			return string(b)
		}
	}
	return v
}

func macKeychainInstall(c *exec.Context, path, password, keychain string, allowAny bool, apps []string) error {
	if err := macSecurityRequire(c); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("mac_keychain.install needs a certificate file")
	}
	argv := []string{"security", "import", path}
	if keychain != "" {
		argv = append(argv, "-k", keychain)
	}
	argv = append(argv, "-P", password)
	if allowAny {
		argv = append(argv, "-A")
	}
	for _, app := range apps {
		argv = append(argv, "-T", app)
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	// "already exists" is the converged state, not a failure.
	if res.Code != 0 && !strings.Contains(res.Stderr+res.Stdout, "already exists") {
		return fmt.Errorf("security import %s: %s", path, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func macKeychainUninstall(c *exec.Context, name, keychain string) error {
	if err := macSecurityRequire(c); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("mac_keychain.uninstall needs a certificate name")
	}
	argv := []string{"security", "delete-certificate", "-c", name}
	if keychain != "" {
		argv = append(argv, keychain)
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 && !strings.Contains(res.Stderr+res.Stdout, "could not be found") {
		return fmt.Errorf("security delete-certificate -c %s: %s", name, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func macKeychainFriendlyName(c *exec.Context, path, password string) (any, error) {
	if c.Which("openssl") == "" {
		return nil, fmt.Errorf("mac_keychain.friendly_name needs `openssl`, which was not found")
	}
	if path == "" {
		return nil, fmt.Errorf("mac_keychain.friendly_name needs a file")
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"openssl", "pkcs12", "-in", path, "-passin", "pass:" + password, "-info", "-nodes", "-nokeys"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("openssl pkcs12 -in %s: %s", path, firstLine(res.Stderr+res.Stdout))
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if _, after, ok := strings.Cut(line, "friendlyName:"); ok {
			return strings.TrimSpace(after), nil
		}
	}
	return "", nil
}
