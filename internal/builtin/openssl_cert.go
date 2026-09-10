package builtin

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerOpenSSLCert installs the `openssl_cert` module of SPEC 15.3's
// Common Linux row.
//
// # Why this is not a second `x509`
//
// `x509` is this build's certificate module and it uses no external
// program at all: crypto/x509 makes keys, signs certificates and reads
// them, which is faster, has no parsing to get wrong, and works on a
// node with no openssl installed. Nothing here duplicates it, and a tree
// that wants a certificate should still use `x509`.
//
// What crypto/x509 will not do is the four things below, and each of
// them is a reason an operator reaches for the command line today:
//
//   - **Chain verification against the machine's own trust store.** Go
//     has no portable way to ask "would this platform trust this
//     certificate", and `openssl verify` with no -CAfile is exactly that
//     question. It is also the question an expiring internal CA is
//     found by.
//   - **PKCS#12.** Go can read a `.p12` only through a third-party
//     package, and this build has no dependencies. `security(1)` on
//     macOS and every Windows tool speak PKCS#12, so a fleet that moves
//     a certificate between platforms moves it in a bundle.
//   - **Certificate revocation lists.** crypto/x509's CRL support parses
//     one; it does not tell an operator when the CRL was issued, when it
//     expires, or which serials are on it, and a CRL that quietly went
//     stale is a fleet that stops trusting everything at once.
//   - **What openssl this node actually has.** LibreSSL is not OpenSSL
//     with a different banner: it does not have `-show_chain`, and a
//     module that assumed otherwise would fail on every OpenBSD node.
//
// # The password never goes on the command line
//
// Every argument vector on this node is readable by every account on it,
// through `ps`, `/proc/<pid>/cmdline` or `procstat -c`. So
// `-passin pass:secret` publishes the passphrase of the bundle it is
// opening to anybody who runs `ps` in the second it takes. Both
// functions that take a password pass `stdin` instead and write it down
// the pipe, and openSSLPasswordArgv is a table a test holds to that.
func registerOpenSSLCert(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "openssl_cert", Function: "version",
				Doc:      "Report the openssl this node has, and which of its options this module can use.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return openSSLVersion(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "openssl_cert", Function: "verify",
				Doc: "Verify a certificate's chain, and say which check failed when it does not verify.",
				Params: []signature.Param{
					req("certificate", signature.Path, "The certificate to verify."),
					opt("ca_file", signature.Path, "", "A file of trusted certificates. Empty means this machine's own trust store."),
					opt("ca_path", signature.Path, "", "A directory of trusted certificates, hashed the way openssl expects."),
					opt("untrusted", signature.Path, "", "A file of intermediate certificates that are not themselves trusted."),
					opt("crl_file", signature.Path, "", "A file of revocation lists. Setting this checks the whole chain for revocation."),
					opt("purpose", signature.String, "", "What the certificate is for: sslserver, sslclient, smimesign and the rest openssl names."),
					opt("hostname", signature.String, "", "Require the certificate to be valid for this name."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.3",
			},
			Fn: openSSLVerify,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "openssl_cert", Function: "crl_info",
				Doc: "Read a certificate revocation list: who issued it, when it expires, and what is on it.",
				Params: []signature.Param{
					req("path", signature.Path, "The revocation list, in PEM or DER."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.3",
			},
			Fn: openSSLCRLInfo,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "openssl_cert", Function: "pkcs12_info",
				Doc: "Read what a PKCS#12 bundle holds, without unpacking it.",
				Params: []signature.Param{
					req("path", signature.Path, "The bundle."),
					opt("password", signature.String, "", "The bundle's passphrase. Sent on standard input, never as an argument."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.3",
			},
			Fn: openSSLPKCS12Info,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "openssl_cert", Function: "pkcs12_create",
				Doc: "Pack a certificate, its key and any chain into a PKCS#12 bundle.",
				Params: []signature.Param{
					req("path", signature.Path, "The bundle to write."),
					req("certificate", signature.Path, "The certificate to pack."),
					req("private_key", signature.Path, "Its private key, unencrypted."),
					opt("ca_certs", signature.Path, "", "A file of chain certificates to pack alongside it."),
					opt("password", signature.String, "", "The passphrase to protect the bundle with. Sent on standard input, never as an argument."),
					opt("friendly_name", signature.String, "", "The name the bundle carries, which is what an importing tool shows."),
				},
				Mutates:   true,
				TestMode:  signature.TestReliable,
				Platforms: unixOnly,
				Section:   "15.3",
			},
			Fn: openSSLPKCS12Create,
		},
	)
}

// ---- the tool ----

// openSSLInfo is what this node's openssl is and can do.
type openSSLInfo struct {
	Path string
	// Banner is the whole of `openssl version`, which names the flavour
	// as well as the number.
	Banner string
	// LibreSSL is set for OpenBSD's fork, which is a different program
	// with the same name: it has no `-show_chain` and its `verify` says
	// less on failure.
	LibreSSL bool
}

// openSSLFind locates the tool and asks what it is.
//
// The flavour is read from the banner rather than assumed from the
// platform. FreeBSD has OpenSSL in base and LibreSSL in ports, and a
// node can have either on its PATH; deciding from runtime.GOOS would be
// deciding from the wrong fact.
func openSSLFind(c *exec.Context) (openSSLInfo, error) {
	path := c.Which("openssl")
	if path == "" {
		return openSSLInfo{}, errors.New(
			"this node has no `openssl`. The `x509` module needs none and does key generation, " +
				"signing and inspection without it; this module is for the four things it cannot do")
	}
	res, err := c.Run(exec.Command{Argv: []string{path, "version"}})
	if err != nil {
		return openSSLInfo{}, fmt.Errorf("`openssl version` could not be run: %w", err)
	}
	banner := strings.TrimSpace(res.Stdout)
	return openSSLInfo{
		Path:     path,
		Banner:   banner,
		LibreSSL: strings.HasPrefix(banner, "LibreSSL"),
	}, nil
}

func openSSLVersion(c *exec.Context) (any, error) {
	info, err := openSSLFind(c)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(4)
	out.Set("path", info.Path)
	out.Set("version", info.Banner)
	out.Set("libressl", info.LibreSSL)
	// Reported rather than merely acted on, because "the chain was not
	// shown" and "there was no chain" look the same to a caller who was
	// not told which openssl answered.
	out.Set("show_chain", !info.LibreSSL)
	return out, nil
}

// ---- verify ----

// openSSLVerifyArgv builds the command.
//
// Kept separate from running it so that every flag this module will ever
// pass can be checked without an openssl, on a platform that has none.
// `-show_chain` is the one option that depends on the flavour, and it is
// the reason `info` is an argument rather than something this reads for
// itself.
func openSSLVerifyArgv(info openSSLInfo, cert, caFile, caPath, untrusted, crlFile, purpose, hostname string) []string {
	argv := []string{info.Path, "verify"}
	if caFile != "" {
		argv = append(argv, "-CAfile", caFile)
	}
	if caPath != "" {
		argv = append(argv, "-CApath", caPath)
	}
	if untrusted != "" {
		argv = append(argv, "-untrusted", untrusted)
	}
	if crlFile != "" {
		// `-crl_check_all` rather than `-crl_check`: checking only the
		// leaf leaves a revoked intermediate trusted, and an
		// intermediate is what gets revoked when a CA is compromised.
		argv = append(argv, "-CRLfile", crlFile, "-crl_check_all")
	}
	if purpose != "" {
		argv = append(argv, "-purpose", purpose)
	}
	if hostname != "" {
		argv = append(argv, "-verify_hostname", hostname)
	}
	if !info.LibreSSL {
		argv = append(argv, "-show_chain")
	}
	return append(argv, cert)
}

func openSSLVerify(c *exec.Context, args *value.Map) (any, error) {
	cert := strings.TrimSpace(states.Str(args, "certificate", ""))
	if cert == "" {
		return nil, errors.New("a certificate must be named")
	}
	if _, err := os.Stat(cert); err != nil {
		return nil, fmt.Errorf("%s cannot be read: %w", cert, err)
	}
	info, err := openSSLFind(c)
	if err != nil {
		return nil, err
	}

	argv := openSSLVerifyArgv(info, cert,
		strings.TrimSpace(states.Str(args, "ca_file", "")),
		strings.TrimSpace(states.Str(args, "ca_path", "")),
		strings.TrimSpace(states.Str(args, "untrusted", "")),
		strings.TrimSpace(states.Str(args, "crl_file", "")),
		strings.TrimSpace(states.Str(args, "purpose", "")),
		strings.TrimSpace(states.Str(args, "hostname", "")),
	)
	// A certificate that does not verify is an answer, not a failure of
	// the command, so the exit code is read rather than raised.
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`openssl verify` could not be run: %w", err)
	}

	code, reason := openSSLVerifyFailure(res.Stdout + "\n" + res.Stderr)

	// A command that failed without a numbered verification error did
	// not verify anything: it could not load one of the files it was
	// given. An empty or unreadable -CAfile produces exactly this, and
	// openssl exits 1 before it looks at the certificate at all.
	//
	// Reporting that as `verified: false` would tell an operator their
	// certificate is not trusted, which is a different problem with a
	// different fix, and would send them to renew a certificate that is
	// fine. It is an error here rather than an answer.
	if res.Code != 0 && !openSSLExamined(res.Stdout, res.Stderr) {
		return nil, fmt.Errorf(
			"`openssl verify` could not check %s -- it exited %d without reporting a verification error, "+
				"which is what an unreadable or empty trust file produces rather than an untrusted "+
				"certificate: %s",
			cert, res.Code, strings.TrimSpace(firstLine(res.Stderr+res.Stdout)))
	}

	out := value.NewMap(6)
	out.Set("certificate", cert)
	out.Set("verified", res.Code == 0)
	out.Set("error_code", code)
	out.Set("reason", reason)
	out.Set("chain", openSSLVerifyChain(res.Stdout))
	out.Set("output", strings.TrimSpace(res.Stdout+res.Stderr))
	return out, nil
}

// openSSLExamined reports whether openssl got as far as looking at a
// certificate.
//
// It is the discriminator between "this certificate is not trusted" and
// "I could not read one of the files you gave me", and those are
// different problems with different fixes: the first sends an operator
// to the certificate, the second to the trust file. An empty or
// unreadable -CAfile produces the second, and openssl exits before it
// examines anything.
//
// Keyed on the *presence of a verdict* rather than on the wording of a
// load error, because the load errors are a different message per
// flavour and per version while the verdict is not: openssl prints
// `<file>: OK` for a certificate that passed and an `error` line for one
// that did not, and prints neither when it never got that far. Both
// streams are read, because which one carries the verdict has moved
// between versions.
func openSSLExamined(stdout, stderr string) bool {
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "error ") ||
			strings.HasSuffix(line, ": OK") ||
			strings.Contains(line, "verification failed") ||
			strings.Contains(line, "verify error") {
			return true
		}
	}
	return false
}

// openSSLVerifyFailure pulls the numbered reason out of openssl's
// report.
//
// The number is the part worth having: `error 10` is an expired
// certificate and `error 20` is a missing issuer, and those want
// different people woken up. Two spellings are handled because both are
// in the field — OpenSSL 3 writes `lookup: unable to...` with a space
// and 1.1.1 writes `lookup:unable to...` without one — and a reader that
// knew only one would return an empty reason on half the estate while
// reporting the failure correctly, which is the shape that gets noticed
// late.
//
// The zero code with an empty reason means openssl reported no numbered
// error, which is what a successful verification looks like.
func openSSLVerifyFailure(out string) (int64, string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "error ")
		if !ok {
			continue
		}
		numText, after, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(numText, 10, 64)
		if err != nil {
			// `error leaf.crt: verification failed` is the summary line,
			// not a numbered reason, and has no code to read.
			continue
		}
		_, reason, ok := strings.Cut(after, "lookup:")
		if !ok {
			return n, strings.TrimSpace(after)
		}
		return n, strings.TrimSpace(reason)
	}
	return 0, ""
}

// openSSLVerifyChain reads `-show_chain`'s listing.
//
// Each line is `depth=0: CN=leaf.example (untrusted)`, and the
// `(untrusted)` marker is kept as a field rather than left in the name:
// it says the certificate came from the file being verified rather than
// from the trust store, which is the difference between "the CA is
// installed" and "the CA travelled with the certificate that claims it".
func openSSLVerifyChain(out string) []any {
	var chain []any
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "depth=")
		if !ok {
			continue
		}
		depthText, subject, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		depth, err := strconv.ParseInt(strings.TrimSpace(depthText), 10, 64)
		if err != nil {
			continue
		}
		subject = strings.TrimSpace(subject)
		trusted := true
		if trimmed, found := strings.CutSuffix(subject, "(untrusted)"); found {
			subject, trusted = strings.TrimSpace(trimmed), false
		}
		entry := value.NewMap(3)
		entry.Set("depth", depth)
		entry.Set("subject", subject)
		entry.Set("trusted", trusted)
		chain = append(chain, entry)
	}
	return chain
}

// ---- revocation lists ----

func openSSLCRLInfo(c *exec.Context, args *value.Map) (any, error) {
	path := strings.TrimSpace(states.Str(args, "path", ""))
	if path == "" {
		return nil, errors.New("a revocation list must be named")
	}
	info, err := openSSLFind(c)
	if err != nil {
		return nil, err
	}
	form, err := openSSLCRLForm(c, info, path)
	if err != nil {
		return nil, err
	}

	// The header comes from the flags that print one field each, because
	// each of those prints `name=value` and nothing else. Only the
	// revoked list has no such flag, and that is the one part read out of
	// `-text`.
	head, err := c.Run(exec.Command{Argv: []string{
		info.Path, "crl", "-in", path, "-inform", form, "-noout",
		"-issuer", "-lastupdate", "-nextupdate", "-crlnumber",
	}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`openssl crl` could not be run: %w", err)
	}
	if head.Code != 0 {
		return nil, fmt.Errorf("%s is not a revocation list openssl can read: %s",
			path, strings.TrimSpace(firstLine(head.Stderr)))
	}

	out := value.NewMap(6)
	out.Set("path", path)
	out.Set("format", form)
	for _, line := range strings.Split(head.Stdout, "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch name {
		case "issuer":
			out.Set("issuer", val)
		case "lastUpdate":
			out.Set("last_update", val)
		case "nextUpdate":
			out.Set("next_update", val)
		case "crlNumber":
			out.Set("crl_number", val)
		}
	}

	body, err := c.Run(exec.Command{Argv: []string{
		info.Path, "crl", "-in", path, "-inform", form, "-noout", "-text",
	}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`openssl crl -text` could not be run: %w", err)
	}
	out.Set("revoked", openSSLRevoked(body.Stdout))
	return out, nil
}

// openSSLCRLForm decides whether a file is PEM or DER by trying it.
//
// openssl 3 ignores `-inform` for a CRL and sniffs, and openssl 1.1.1
// does not — so a DER list read on 1.1.1 without the flag fails, and the
// same file read on 3 succeeds, which is a difference an operator would
// find in production rather than here. Trying PEM and falling back to
// DER makes both behave the same, and the answer is reported so nobody
// has to guess which happened.
func openSSLCRLForm(c *exec.Context, info openSSLInfo, path string) (string, error) {
	for _, form := range []string{"PEM", "DER"} {
		res, err := c.Run(exec.Command{
			Argv:           []string{info.Path, "crl", "-in", path, "-inform", form, "-noout"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", fmt.Errorf("`openssl crl` could not be run: %w", err)
		}
		if res.Code == 0 {
			return form, nil
		}
		c.Logf("debug", "%s is not a %s revocation list: %s", path, form, strings.TrimSpace(firstLine(res.Stderr)))
	}
	return "", fmt.Errorf("%s is not a revocation list openssl can read, as PEM or as DER", path)
}

// openSSLRevoked reads the revoked entries out of `openssl crl -text`.
//
// There is no machine-readable alternative for this one: openssl has no
// flag that prints the serials, so the text is the interface. What makes
// it safe enough to parse is that the two lines are labelled and
// adjacent — `Serial Number:` then `Revocation Date:` — so the reader
// keys on the labels and never on a column position.
//
// An empty list is an empty list. openssl prints `No Revoked
// Certificates.` for one, and returning no entries for that is correct;
// what would be wrong is failing, because a CRL with nothing on it is
// the normal state of a healthy CA.
func openSSLRevoked(text string) []any {
	var (
		out     []any
		current *value.Map
	)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Serial Number:"):
			current = value.NewMap(2)
			current.Set("serial", strings.TrimSpace(strings.TrimPrefix(line, "Serial Number:")))
			current.Set("revoked", "")
			out = append(out, current)
		case strings.HasPrefix(line, "Revocation Date:") && current != nil:
			current.Set("revoked", strings.TrimSpace(strings.TrimPrefix(line, "Revocation Date:")))
			current = nil
		}
	}
	return out
}

// ---- PKCS#12 ----

// openSSLPasswordArgv is how a passphrase reaches openssl.
//
// It is a function of one line so that a test can hold the whole module
// to it. `pass:` puts the passphrase in the argument vector, which every
// account on the node can read out of `ps` for as long as the process
// lives; `env:` puts it in the environment, which is readable on Linux
// through /proc for the same window. `stdin` is the only one of
// openssl's four that never leaves the pipe.
//
// The `role` is `-passin` or `-passout`. Both are never passed at once,
// because both would read the same standard input and openssl would take
// the first line for whichever it asked for first.
func openSSLPasswordArgv(role string) []string { return []string{role, "stdin"} }

func openSSLPKCS12Info(c *exec.Context, args *value.Map) (any, error) {
	path := strings.TrimSpace(states.Str(args, "path", ""))
	if path == "" {
		return nil, errors.New("a bundle must be named")
	}
	info, err := openSSLFind(c)
	if err != nil {
		return nil, err
	}

	argv := append([]string{info.Path, "pkcs12", "-in", path, "-info", "-nokeys", "-nodes"},
		openSSLPasswordArgv("-passin")...)
	res, err := c.Run(exec.Command{
		Argv:           argv,
		Stdin:          states.Str(args, "password", "") + "\n",
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("`openssl pkcs12` could not be run: %w", err)
	}
	if res.Code != 0 {
		// The message is openssl's own. A wrong passphrase and a
		// corrupt bundle are different problems and openssl already
		// distinguishes them; restating either here would only be a
		// second, staler wording of the same thing.
		return nil, fmt.Errorf("%s could not be opened: %s", path,
			strings.TrimSpace(firstLine(res.Stderr)))
	}

	// `-info` writes its description to standard error and the
	// certificates to standard output, which is worth knowing: reading
	// only stdout loses the encryption and iteration counts, and reading
	// only stderr loses the certificates.
	out := value.NewMap(5)
	out.Set("path", path)
	out.Set("friendly_name", openSSLBagAttribute(res.Stdout, "friendlyName"))
	out.Set("subjects", openSSLBagLines(res.Stdout, "subject="))
	out.Set("issuers", openSSLBagLines(res.Stdout, "issuer="))
	out.Set("certificates", int64(strings.Count(res.Stdout, "-----BEGIN CERTIFICATE-----")))
	out.Set("description", strings.TrimSpace(res.Stderr))
	return out, nil
}

// openSSLBagAttribute reads one `Bag Attributes` line.
func openSSLBagAttribute(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// openSSLBagLines collects every `subject=` or `issuer=` line, in the
// order the bundle holds them.
func openSSLBagLines(text, prefix string) []any {
	var out []any
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	return out
}

func openSSLPKCS12Create(c *exec.Context, args *value.Map) (any, error) {
	path := strings.TrimSpace(states.Str(args, "path", ""))
	cert := strings.TrimSpace(states.Str(args, "certificate", ""))
	key := strings.TrimSpace(states.Str(args, "private_key", ""))
	chain := strings.TrimSpace(states.Str(args, "ca_certs", ""))
	name := strings.TrimSpace(states.Str(args, "friendly_name", ""))
	if path == "" || cert == "" || key == "" {
		return nil, errors.New("a bundle, a certificate and a private key must all be named")
	}
	for label, p := range map[string]string{"certificate": cert, "private_key": key} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("the %s at %s cannot be read: %w", label, p, err)
		}
	}
	if chain != "" {
		if _, err := os.Stat(chain); err != nil {
			return nil, fmt.Errorf("the chain file at %s cannot be read: %w", chain, err)
		}
	}
	info, err := openSSLFind(c)
	if err != nil {
		return nil, err
	}

	argv := []string{info.Path, "pkcs12", "-export", "-out", path, "-in", cert, "-inkey", key}
	if chain != "" {
		argv = append(argv, "-certfile", chain)
	}
	if name != "" {
		argv = append(argv, "-name", name)
	}
	argv = append(argv, openSSLPasswordArgv("-passout")...)

	_, existed := os.Stat(path)
	if c.Test {
		out := value.NewMap(3)
		out.Set("changed", true)
		out.Set("command", exec.Command{Argv: argv}.String())
		if existed == nil {
			out.Set("comment", fmt.Sprintf(
				"%s would be replaced. Nothing was written: this was a test run.", path))
		} else {
			out.Set("comment", fmt.Sprintf(
				"%s would be written. Nothing was written: this was a test run.", path))
		}
		return out, nil
	}

	res, err := c.Run(exec.Command{
		Argv:           argv,
		Stdin:          states.Str(args, "password", "") + "\n",
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("`openssl pkcs12 -export` could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("%s could not be written: %s", path,
			strings.TrimSpace(firstLine(res.Stderr)))
	}
	// A bundle holds a private key, so it is nobody's business but its
	// owner's. openssl creates it with the process umask, which on a node
	// running a highstate is whatever the agent was started with.
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("%s was written and its mode could not be tightened to 0600: %w", path, err)
	}

	out := value.NewMap(3)
	out.Set("changed", true)
	out.Set("path", path)
	if existed == nil {
		out.Set("comment", fmt.Sprintf("%s was replaced.", path))
	} else {
		out.Set("comment", fmt.Sprintf("%s was written.", path))
	}
	return out, nil
}
