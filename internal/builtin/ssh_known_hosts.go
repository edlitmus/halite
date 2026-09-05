package builtin

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSSHKnownHosts installs ssh_known_hosts, the pair to ssh_auth.
//
// The file is parsed into records rather than pattern-matched, for the
// same reason authorized_keys is: a careless edit either strands a host
// nobody can reach or leaves a key behind that should have gone, and the
// second is the one that matters here — a stale known_hosts entry is a
// host key an operator has already agreed to trust.
//
// Where a key comes from is the whole security question in this module,
// so it is a choice the state has to make rather than one made for it:
//
//   - `key` names the key outright. Nothing is fetched, nothing is
//     trusted that was not written down.
//   - `fingerprint` scans the host and accepts the key only if it
//     hashes to what was declared. The scan supplies the bytes; the
//     fingerprint supplies the trust.
//   - Neither is trust on first use, and it is refused rather than
//     silently performed. Salt scans and accepts, which is how a state
//     tree comes to pin whatever answered on the day it first ran. An
//     estate that wants that can ask for it by name.
func registerSSHKnownHosts(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "ssh", Function: "known_hosts",
				Doc: "Return an account's known hosts, keyed by host.",
				Params: []signature.Param{
					opt("user", signature.String, "", "Whose known_hosts; defaults to the caller's."),
					opt("config", signature.Path, "", "A known_hosts path other than the default."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				path, err := knownHostsPath(states.Str(args, "user", ""), states.Str(args, "config", ""))
				if err != nil {
					return nil, err
				}
				entries := parseKnownHosts(readLines(path))
				out := value.NewMap(len(entries))
				for _, e := range entries {
					if e.Verbatim {
						continue
					}
					out.Set(e.Host, value.MapOf(
						"enc", e.Type,
						"key", e.Key,
						"fingerprint", sha256Fingerprint(e.Key),
					))
				}
				return out, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "ssh_known_hosts", Function: "present",
				Doc: "Ensure a host's public key is in an account's known_hosts.",
				Params: []signature.Param{
					nameParam("The host. Defaults to the state ID."),
					opt("user", signature.String, "", "Whose known_hosts; defaults to the caller's."),
					opt("key", signature.String, "", "The base64 key blob, if it is being declared rather than scanned."),
					opt("fingerprint", signature.String, "", "The SHA256 fingerprint the scanned key must have."),
					opt("enc", signature.String, "", "The key type, such as ssh-ed25519."),
					opt("port", signature.Int, int64(22), "The port to scan, and to record for a non-standard one."),
					opt("config", signature.Path, "", "A known_hosts path other than the default."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"the target account, or root"},
				Section:    "15.5",
			},
			Fn: sshKnownHostsPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "ssh_known_hosts", Function: "absent",
				Doc: "Ensure a host is not in an account's known_hosts.",
				Params: []signature.Param{
					nameParam("The host. Defaults to the state ID."),
					opt("user", signature.String, "", "Whose known_hosts; defaults to the caller's."),
					opt("port", signature.Int, int64(22), "The port, for an entry recorded with a non-standard one."),
					opt("config", signature.Path, "", "A known_hosts path other than the default."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"the target account, or root"},
				Section:    "15.5",
			},
			Fn: sshKnownHostsAbsent,
		},
	)
}

// knownHost is one known_hosts record.
type knownHost struct {
	// Host is the host field as written, which may carry a port in the
	// `[name]:port` form and may list several names separated by commas.
	Host string
	Type string
	Key  string
	// Raw holds a comment, a blank line, or anything this build does not
	// understand, preserved verbatim. A hashed entry lands here: it is
	// not readable, and rewriting the file must not drop it.
	Raw string
	// Verbatim says Raw is the whole of this record. A bare `Raw != ""`
	// cannot say that, because a blank line has an empty Raw and is not
	// a host record; reading one as a record put an entry under the
	// empty string into `ssh.known_hosts`.
	Verbatim bool
}

func (e knownHost) String() string {
	if e.Verbatim {
		return e.Raw
	}
	return strings.Join([]string{e.Host, e.Type, e.Key}, " ")
}

// parseKnownHosts reads the file into records.
//
// A hashed entry — one whose host field begins with `|1|` — is kept as
// Raw. Its host cannot be recovered without the salt and the name being
// looked for, so this build neither matches nor rewrites it, and says so
// rather than quietly adding a second plaintext entry beside it.
func parseKnownHosts(lines []string) []knownHost {
	var out []knownHost
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, knownHost{Raw: line, Verbatim: true})
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "|") {
			out = append(out, knownHost{Raw: line, Verbatim: true})
			continue
		}
		// A marker such as @cert-authority or @revoked leads the line
		// and changes what the entry means, so it is not something to
		// reformat or match against.
		if strings.HasPrefix(fields[0], "@") {
			out = append(out, knownHost{Raw: line, Verbatim: true})
			continue
		}
		out = append(out, knownHost{Host: fields[0], Type: fields[1], Key: fields[2]})
	}
	return out
}

// knownHostsPath resolves where an account's known hosts live.
func knownHostsPath(username, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	var u *user.User
	var err error
	if username == "" {
		u, err = user.Current()
	} else {
		u, err = user.Lookup(username)
	}
	if err != nil {
		return "", fmt.Errorf("the account %q could not be resolved: %w", username, err)
	}
	if u.HomeDir == "" {
		return "", fmt.Errorf("the account %q has no home directory", u.Username)
	}
	return filepath.Join(u.HomeDir, ".ssh", "known_hosts"), nil
}

// hostField renders the host as known_hosts records it, which is the
// bare name on port 22 and `[name]:port` on any other.
func hostField(host string, port int64) string {
	if port == 0 || port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.FormatInt(port, 10)
}

// matchesHost reports whether a record covers the host being managed.
//
// A record's host field may list several names against one key, which is
// what ssh writes when a name and an address are both known, so the
// comma-separated list is compared rather than the whole field.
func matchesHost(record, want string) bool {
	for _, name := range strings.Split(record, ",") {
		if strings.EqualFold(strings.TrimSpace(name), want) {
			return true
		}
	}
	return false
}

// sha256Fingerprint is the fingerprint ssh prints, which is what an
// operator has in front of them when they write one into a state.
func sha256Fingerprint(key string) string {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}

// sameFingerprint compares what a state declared against what a key
// hashes to, accepting the form an operator is most likely to paste.
func sameFingerprint(declared, actual string) bool {
	d := strings.TrimSpace(declared)
	if d == "" || actual == "" {
		return false
	}
	if !strings.Contains(d, ":") {
		// A bare base64 body, without the SHA256: prefix ssh prints.
		d = "SHA256:" + d
	}
	return strings.EqualFold(strings.TrimRight(d, "="), strings.TrimRight(actual, "="))
}

func sshKnownHostsPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	host := strings.TrimSpace(states.Str(args, "name", ""))
	if host == "" {
		return states.False("This state needs a host."), nil
	}
	port := states.Int(args, "port", 22)
	path, err := knownHostsPath(states.Str(args, "user", ""), states.Str(args, "config", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	field := hostField(host, port)

	want, res, ok := knownHostKey(c, args, host, port)
	if !ok {
		return res, nil
	}

	existing := parseKnownHosts(readLines(path))
	for i, e := range existing {
		if e.Verbatim || !matchesHost(e.Host, field) {
			continue
		}
		if e.Type == want.Type && e.Key == want.Key {
			return states.True(fmt.Sprintf("%s is already known in %s.", host, path)), nil
		}
		// The host is known with a different key. That is either a
		// rotation or the thing known_hosts exists to detect, and this
		// build cannot tell which — so the change set names both
		// fingerprints and an operator can.
		existing[i] = want
		return writeKnownHosts(c, path, existing, value.MapOf(
			host, states.Change(sha256Fingerprint(e.Key), sha256Fingerprint(want.Key))),
			fmt.Sprintf("The key for %s was replaced in %s; it had a different key already.", host, path))
	}

	existing = append(existing, want)
	return writeKnownHosts(c, path, existing, value.MapOf(
		host, states.Change(nil, sha256Fingerprint(want.Key))),
		fmt.Sprintf("%s was added to %s.", host, path))
}

// knownHostKey decides what key the state is asking for, and where it
// came from. The bool reports whether there is one; the Result carries
// the explanation when there is not.
func knownHostKey(c *exec.Context, args *value.Map, host string, port int64) (knownHost, states.Result, bool) {
	field := hostField(host, port)
	declared := strings.TrimSpace(states.Str(args, "key", ""))
	fingerprint := strings.TrimSpace(states.Str(args, "fingerprint", ""))
	enc := strings.TrimSpace(states.Str(args, "enc", ""))

	if declared != "" {
		if enc == "" {
			return knownHost{}, states.False(
				"A declared key needs `enc` as well, naming its type, such as ssh-ed25519."), false
		}
		return knownHost{Host: field, Type: enc, Key: declared}, states.Result{}, true
	}

	if fingerprint == "" {
		// Trust on first use, refused by name. Salt scans and accepts
		// whatever answers, which pins whatever was listening the day
		// the tree first ran; and a state that does that in test mode
		// has already made the trust decision it was asked to preview.
		return knownHost{}, states.False(fmt.Sprintf(
			"%s cannot be added without either `key` or `fingerprint`. "+
				"Scanning and trusting whatever answers is what known_hosts exists to prevent; "+
				"`ssh-keyscan %s` prints the key and `ssh-keygen -lf` its fingerprint.",
			host, host)), false
	}

	scanned, err := scanHostKeys(c, host, port)
	if err != nil {
		return knownHost{}, states.False(fmt.Sprintf("%s could not be scanned: %v", host, err)), false
	}
	if len(scanned) == 0 {
		return knownHost{}, states.False(fmt.Sprintf("%s offered no host keys.", host)), false
	}
	for _, k := range scanned {
		if enc != "" && k.Type != enc {
			continue
		}
		if sameFingerprint(fingerprint, sha256Fingerprint(k.Key)) {
			k.Host = field
			return k, states.Result{}, true
		}
	}
	// The mismatch is the whole point of declaring a fingerprint, so it
	// names what was offered rather than only what was wanted.
	var offered []string
	for _, k := range scanned {
		offered = append(offered, k.Type+" "+sha256Fingerprint(k.Key))
	}
	return knownHost{}, states.False(fmt.Sprintf(
		"%s offered no key matching %s; it offered %s.",
		host, fingerprint, strings.Join(offered, ", "))), false
}

// scanHostKeys asks the host for its keys through the system
// ssh-keyscan, for the reason SPEC 21.1 gives for the system ssh: it
// inherits the operating system's OpenSSH patch level and its
// configuration, and reimplementing the handshake here would be a second
// SSH implementation with its own defects.
func scanHostKeys(c *exec.Context, host string, port int64) ([]knownHost, error) {
	if c.Which("ssh-keyscan") == "" {
		return nil, fmt.Errorf("ssh-keyscan is not installed, and a fingerprint can only be checked against a scan")
	}
	argv := []string{"ssh-keyscan"}
	if port != 0 && port != 22 {
		argv = append(argv, "-p", strconv.FormatInt(port, 10))
	}
	argv = append(argv, host)

	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	var out []knownHost
	for _, e := range parseKnownHosts(strings.Split(res.Stdout, "\n")) {
		if !e.Verbatim {
			out = append(out, e)
		}
	}
	return out, nil
}

func sshKnownHostsAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	host := strings.TrimSpace(states.Str(args, "name", ""))
	if host == "" {
		return states.False("This state needs a host."), nil
	}
	path, err := knownHostsPath(states.Str(args, "user", ""), states.Str(args, "config", ""))
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	field := hostField(host, states.Int(args, "port", 22))

	existing := parseKnownHosts(readLines(path))
	kept := make([]knownHost, 0, len(existing))
	var removed []string
	for _, e := range existing {
		if !e.Verbatim && matchesHost(e.Host, field) {
			removed = append(removed, sha256Fingerprint(e.Key))
			continue
		}
		kept = append(kept, e)
	}
	if len(removed) == 0 {
		return states.True(fmt.Sprintf("%s is already absent from %s.", host, path)), nil
	}
	return writeKnownHosts(c, path, kept, value.MapOf(
		host, states.Change(strings.Join(removed, ", "), nil)),
		fmt.Sprintf("%s was removed from %s.", host, path))
}

// writeKnownHosts renders and writes the file, creating ~/.ssh with the
// mode ssh insists on.
func writeKnownHosts(c *exec.Context, path string, entries []knownHost, changes *value.Map, comment string) (states.Result, error) {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	if c.Test {
		return states.WouldChange(strings.Replace(comment, " was ", " would be ", 1), changes), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return states.False(fmt.Sprintf("%s could not be created: %v", filepath.Dir(path), err)), nil
	}
	// 0644 rather than authorized_keys' 0600: known_hosts is not a
	// secret, and ssh does not refuse a readable one.
	if err := writeAtomic(path, []byte(b.String()), 0o644); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
	}
	return states.Changed(comment, changes), nil
}
