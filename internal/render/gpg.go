package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// The gpg renderer decrypts an existing Salt tree's encrypted pillar.
// SPEC section 12.6 fixes the shape of it: shell out to the system gpg
// binary, link no OpenPGP library, and pass the ciphertext on standard
// input and never on the command line.
//
// That last rule is the same one the account password obeys, for the
// same reason: an argument vector is readable by every account on the
// machine for as long as the process lives. Here it also keeps the
// ciphertext out of any audit log that records command lines.
//
// Nothing in this file logs a plaintext, and no error message quotes a
// value. A decryption failure names the file and the key path, which is
// what an operator needs to find it, and nothing of what was in it.

const pgpHeader = "-----BEGIN PGP MESSAGE-----"

// GPGOptions configure the decryption. Every field has a working default,
// so a tree that says nothing gets the system gpg and its own keyring.
type GPGOptions struct {
	// Binary is the gpg to run. Empty looks for gpg on PATH.
	Binary string
	// Home is GNUPGHOME. Empty leaves the environment's.
	Home string
	// Timeout bounds one decryption. Zero means 30 seconds.
	Timeout time.Duration
}

func (o GPGOptions) binary() string {
	if o.Binary != "" {
		return o.Binary
	}
	return "gpg"
}

func (o GPGOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 30 * time.Second
}

// decryptGPG walks parsed data and replaces every PGP message with its
// plaintext, leaving everything else alone.
func decryptGPG(v any, opts Options) (any, error) {
	// SPEC 12.6: absent gpg fails at load rather than at first use, so
	// the binary is resolved once before the walk rather than at the
	// first encrypted value found.
	g := opts.GPG
	path, err := exec.LookPath(g.binary())
	if err != nil {
		return nil, fmt.Errorf("the gpg renderer needs the %s binary, which is not on PATH: %w", g.binary(), err)
	}
	return walkDecrypt(v, path, g, nil)
}

// walkDecrypt rebuilds the tree, decrypting as it goes. `path` is the key
// path so far, so a failure can say which value failed without saying
// what was in it.
func walkDecrypt(v any, gpgPath string, opts GPGOptions, at []string) (any, error) {
	switch t := v.(type) {
	case *value.Map:
		out := value.NewMap(t.Len())
		for _, e := range t.Entries() {
			key := value.KeyString(e.Key)
			decrypted, err := walkDecrypt(e.Val, gpgPath, opts, append(at, key))
			if err != nil {
				return nil, err
			}
			out.SetAt(e.Key, decrypted, e.KeyPos, e.ValPos)
		}
		return out, nil

	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			decrypted, err := walkDecrypt(item, gpgPath, opts, append(at, fmt.Sprintf("[%d]", i)))
			if err != nil {
				return nil, err
			}
			out[i] = decrypted
		}
		return out, nil

	case string:
		if !strings.Contains(t, pgpHeader) {
			return t, nil
		}
		plain, err := runGPG(gpgPath, opts, t)
		if err != nil {
			return nil, fmt.Errorf("%s could not be decrypted: %w", pathOf(at), err)
		}
		return plain, nil
	}
	return v, nil
}

func pathOf(at []string) string {
	if len(at) == 0 {
		return "the document"
	}
	return strings.Join(at, ":")
}

// runGPG decrypts one message. The ciphertext goes on standard input.
func runGPG(gpgPath string, opts GPGOptions, ciphertext string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, gpgPath,
		"--decrypt",
		// No terminal is attached, so a passphrase prompt would hang
		// until the timeout rather than fail. An agent that already
		// holds the key still works.
		"--batch", "--no-tty",
		// Nothing here should reach the operator's screen or the job
		// return; only the plaintext on stdout is wanted.
		"--quiet", "--yes",
	)
	if opts.Home != "" {
		cmd.Env = append(os.Environ(), "GNUPGHOME="+opts.Home)
	}
	cmd.Stdin = strings.NewReader(ciphertext)

	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("gpg did not finish within %s", opts.timeout())
	}
	if err != nil {
		// gpg's diagnostics name the key and the reason, and do not
		// carry the plaintext. They are the useful half of the failure.
		return "", fmt.Errorf("gpg exited non-zero: %s", lastLines(errb.String(), 2))
	}
	// Salt strips the trailing newline gpg adds, and a pillar value that
	// silently gained one would differ from the same value under Salt.
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// lastLines keeps the end of gpg's diagnostics. It warns about the
// configuration on startup and reports the actual failure at the point
// of it, so the first lines are usually about something else entirely.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
