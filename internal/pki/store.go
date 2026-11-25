package pki

import (
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
)

// Files is a directory of key material.
//
// Keys are written 0600 and certificates 0644, and the directory is
// 0700: a certificate is public and a key is not, and the difference
// should be visible in `ls -l` rather than only in a document.
//
// **On Windows a mode is not that difference**, and this package said the
// sentence above for a year while not keeping it there: a file mode is the
// read-only attribute and a directory mode is nothing at all, so the CA key
// and the directory holding it were reachable by any account on the machine.
// Both now go through internal/fileperm, which turns "no group, no other"
// into an access control list granting the owner, SYSTEM and Administrators
// and nobody else. `fileperm.Others` names anyone beyond those, on either
// platform, so a test can assert the restriction rather than read the source
// and believe it. plan.md 19e.
type Files struct{ Dir string }

// The names this layout uses, so that the hub and the node agree
// without a string appearing twice.
const (
	CAKeyFile    = "ca.key"
	CACertFile   = "ca.crt"
	HubKeyFile   = "hub.key"
	HubCertFile  = "hub.crt"
	NodeKeyFile  = "node.key"
	NodeCertFile = "node.crt"

	// OperatorPrefix names an operator or service credential:
	// operator-<name>.crt, whose CN is <name> and whose RBAC principal
	// is cert:CN=<name>.
	OperatorPrefix = "operator-"
)

// OperatorCertFile is the certificate for an operator called name.
func OperatorCertFile(name string) string { return OperatorPrefix + name + ".crt" }

// OperatorNames lists the operators this key directory holds a
// certificate for, sorted.
//
// One lister, because two of them would disagree. `run` uses it to
// decide which certificate to present when `--as` is not given, and
// `doctor` uses it to decide which certificates to check the expiry of;
// a hub where those two sets differ is a hub that authenticates with a
// credential nothing is watching. A missing or unreadable directory
// yields no names rather than an error: the callers both treat "no
// operator certificates" as a state to report, not a failure.
func (f Files) OperatorNames() []string {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, OperatorPrefix) || !strings.HasSuffix(name, ".crt") {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(name, OperatorPrefix), ".crt"))
	}
	sort.Strings(names)
	return names
}

func (f Files) Path(name string) string { return filepath.Join(f.Dir, name) }

// Ensure creates the directory, restricted to the account that owns it.
//
// `MkdirAll` with 0o700 is the whole answer on unix and none of it on Windows,
// where a directory mode is not an access control decision at all -- so the
// directory holding the enrollment CA's private key was readable by any
// account on the machine, while this package's own documentation says the
// directory is 0700 because "a certificate is public and a key is not". That
// is the one file in an estate whose disclosure cannot be undone.
//
// internal/fileperm is what expresses the intent on both platforms: a chmod
// here, an inheriting ACL granting the owner, SYSTEM and Administrators there.
// Only the leaf is restricted, deliberately -- `MkdirAll` may have created
// parents, and those belong to whoever laid out the tree rather than to this
// package. DIVERGENCE 5.144, plan.md 19e.
func (f Files) Ensure() error {
	if f.Dir == "" {
		return errors.New("no key directory was given")
	}
	if err := fileperm.MkdirAll(f.Dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", f.Dir, err)
	}
	// Applied on every call and not only on creation. A directory somebody
	// made by hand, or one left by an older build, is the case that matters:
	// it is the one nobody will go back and check.
	if err := fileperm.Apply(f.Dir, 0o700); err != nil {
		return fmt.Errorf("restricting %s to its owner: %w", f.Dir, err)
	}
	return nil
}

// Exists reports whether a file is present.
func (f Files) Exists(name string) bool {
	_, err := os.Stat(f.Path(name))
	return err == nil
}

// WriteKey stores a private key, refusing to overwrite one.
//
// Overwriting a private key is how an estate loses an identity, and no
// caller here has a reason to: a rotation writes a new name and the old
// one is retired deliberately.
//
// **The name holds the whole key or does not exist.** It was created with
// O_EXCL and then written, so a process killed between the two left an
// empty file under the key's name, which ReadKey cannot read and this
// refused to replace. A hub killed during its first start then never
// started again: it could not load the key, made another, and was refused
// writing it (DIVERGENCE 5.739). atomicfile.WriteNew writes a temporary
// file and links it into place, so that state no longer arises.
//
// An empty file already there, left by a build from before that, is
// replaced rather than refused. It holds no key, so replacing it cannot
// lose one, and the refusal's own message, "already holds a private key",
// was false about it. Anything with even one byte in it is still refused:
// a key in a form this package does not read is still somebody's key.
//
// The restriction is applied to the temporary file before the key is
// written into it, so there is no instant at which the key exists on disk
// reachable by another account (5.161, 5.164).
func (f Files) WriteKey(name string, key crypto.Signer) error {
	if err := f.Ensure(); err != nil {
		return err
	}
	pemBytes, err := EncodeKey(key)
	if err != nil {
		return err
	}
	path := f.Path(name)
	err = atomicfile.WriteNew(path, pemBytes, 0o600)
	if errors.Is(err, fs.ErrExist) && emptyRegularFile(path) {
		if rmErr := os.Remove(path); rmErr != nil {
			return fmt.Errorf("%s is empty, left by a write that never finished, and could not be removed: %w", path, rmErr)
		}
		err = atomicfile.WriteNew(path, pemBytes, 0o600)
	}
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already holds a private key; move it aside deliberately if it is to be replaced", path)
	}
	return err
}

// emptyRegularFile reports whether path is a regular file of no bytes.
// Lstat, so that a link named like a key is never taken for an empty one.
func emptyRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() == 0
}

// ReadKey loads a private key.
func (f Files) ReadKey(name string) (crypto.Signer, error) {
	raw, err := atomicfile.Read(f.Path(name))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", f.Path(name), err)
	}
	return DecodeKey(raw)
}

// WriteCert stores a certificate, replacing any earlier one: unlike a
// key, a certificate is meant to be replaced, and that is renewal.
func (f Files) WriteCert(name string, der []byte) error {
	if err := f.Ensure(); err != nil {
		return err
	}
	return writeFile(f.Path(name), EncodeCert(der), 0o644)
}

// WriteCertPEM stores a certificate already in PEM, as the hub sends it.
func (f Files) WriteCertPEM(name string, pemBytes []byte) error {
	if err := f.Ensure(); err != nil {
		return err
	}
	// Parsed before it is stored: a node that writes whatever arrived
	// and discovers next boot that it is not a certificate has lost the
	// only moment where the hub could have been asked again.
	if _, err := DecodeCert(pemBytes); err != nil {
		return fmt.Errorf("what the hub sent is not a certificate: %w", err)
	}
	return writeFile(f.Path(name), pemBytes, 0o644)
}

// ReadCert loads a certificate.
func (f Files) ReadCert(name string) (*x509.Certificate, error) {
	raw, err := atomicfile.Read(f.Path(name))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", f.Path(name), err)
	}
	return DecodeCert(raw)
}

// KeyPair loads a certificate and its key as a TLS pair, checking that
// they belong together rather than failing later in a handshake.
func (f Files) KeyPair(certName, keyName string) (tls.Certificate, error) {
	certPEM, err := atomicfile.Read(f.Path(certName))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", f.Path(certName), err)
	}
	keyPEM, err := atomicfile.Read(f.Path(keyName))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", f.Path(keyName), err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s and %s do not go together: %w", f.Path(certName), f.Path(keyName), err)
	}
	return pair, nil
}

// LoadCA reads an existing enrollment CA.
func (f Files) LoadCA(now func() time.Time) (*CA, error) {
	cert, err := f.ReadCert(CACertFile)
	if err != nil {
		return nil, err
	}
	key, err := f.ReadKey(CAKeyFile)
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("%s is not a CA certificate", f.Path(CACertFile))
	}
	return &CA{Cert: cert, Key: key, Now: now}, nil
}

// CreateCA generates an enrollment CA and stores it. It refuses to
// replace one, because a second CA in the same directory means every
// node in the estate has to enrol again.
//
// **A key with no certificate is finished rather than refused.** The key
// is written first and the certificate after it, so a hub killed between
// the two left ca.key alone, and every later start refused with "already
// holds an enrollment CA" -- a CA that had never issued anything, because
// nothing can be issued without the certificate. A stress loop killing a
// first `serve` at random moments left that state in 6 runs of 150
// (DIVERGENCE 5.739). The certificate is now made from the key that is
// there, which is the same thing the interrupted start would have written
// apart from its serial and dates, and the key is never replaced.
//
// The algorithm is the key's, not alg: alg chose it the first time.
func (f Files) CreateCA(alg KeyAlgorithm, commonName string, lifetime time.Duration) (*CA, error) {
	if f.Exists(CACertFile) {
		// It named `keys rotate-ca` as the way to replace one, and that
		// command has never existed: SPEC 7.5 describes it, and it is not
		// built (DIVERGENCE 6.1a). An operator following the message got
		// "unknown subcommand". What it says now is what is true: there is
		// no rotation, and a second CA means every node enrolls again, so
		// this will not make one over the first. DIVERGENCE 5.739.1.
		return nil, fmt.Errorf("%s already holds an enrollment CA (%s), and this will not make a second one over it: "+
			"every node would have to enroll again. There is no CA rotation yet (SPEC 7.5, not built)", f.Dir, CACertFile)
	}
	if f.Exists(CAKeyFile) && !emptyRegularFile(f.Path(CAKeyFile)) {
		key, err := f.ReadKey(CAKeyFile)
		if err != nil {
			return nil, fmt.Errorf("%s holds no certificate and a key that cannot be read, so it is neither a CA nor the start of one: %w", f.Dir, err)
		}
		ca, err := caFromKey(key, commonName, lifetime)
		if err != nil {
			return nil, err
		}
		if err := f.WriteCert(CACertFile, ca.Cert.Raw); err != nil {
			return nil, err
		}
		return ca, nil
	}
	ca, err := NewCA(alg, commonName, lifetime)
	if err != nil {
		return nil, err
	}
	if err := f.WriteKey(CAKeyFile, ca.Key); err != nil {
		return nil, err
	}
	if err := f.WriteCert(CACertFile, ca.Cert.Raw); err != nil {
		return nil, err
	}
	return ca, nil
}

// writeFile replaces a file atomically.
//
// This was a copy of atomicfile.Write, and it had drifted in the three
// ways such a copy does. It chmod'd the temporary file directly rather
// than going through internal/fileperm, so on Windows a mode was the
// read-only attribute and nothing else. It called os.Rename rather than
// atomicfile.Rename, so the Windows sharing race that atomicfile exists
// for -- a reader holding the destination open makes MoveFileEx fail, and
// while MoveFileEx holds it a reader's open fails -- applied to the key
// store. And it never synced the directory after the rename, so a power
// loss could leave a certificate with no name. DIVERGENCE 5.144.
func writeFile(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}

// ParseSerial reads a serial back from the spelling SerialString gives
// it, so that a record on disk can name a certificate.
func ParseSerial(s string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil, fmt.Errorf("%q is not a certificate serial", s)
	}
	return n, nil
}

// RevokedCert is one entry in a CRL.
type RevokedCert struct {
	Serial string
	At     time.Time
	// Reason is an RFC 5280 section 5.3.1 reason code; zero leaves the
	// extension out, which a reader takes as unspecified.
	Reason int
}

// The RFC 5280 reason codes this hub gives.
const (
	ReasonSuperseded           = 4
	ReasonCessationOfOperation = 5
)

// CRL signs a certificate revocation list.
//
// The list is the interoperable form of the revocation denylist, for
// anything that is not this hub's own handshake -- a load balancer, an
// audit, an OpenSSL command line. The handshake does not depend on it,
// per SPEC 7.4: a hub that waited for a CRL to propagate would have a
// revocation that takes effect at some point.
func (ca *CA) CRL(revoked []RevokedCert, number int64, lifetime time.Duration) ([]byte, error) {
	now := ca.now()
	list := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, r := range revoked {
		serial, err := ParseSerial(r.Serial)
		if err != nil {
			return nil, err
		}
		at := r.At
		if at.IsZero() {
			at = now
		}
		list = append(list, x509.RevocationListEntry{
			SerialNumber:   serial,
			RevocationTime: at.UTC(),
			ReasonCode:     r.Reason,
		})
	}
	tmpl := &x509.RevocationList{
		RevokedCertificateEntries: list,
		Number:                    big.NewInt(number),
		ThisUpdate:                now.UTC(),
		NextUpdate:                now.Add(lifetime).UTC(),
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, ca.Cert, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("signing the revocation list: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), nil
}
