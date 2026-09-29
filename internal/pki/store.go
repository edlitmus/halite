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
func (f Files) WriteKey(name string, key crypto.Signer) error {
	if err := f.Ensure(); err != nil {
		return err
	}
	pemBytes, err := EncodeKey(key)
	if err != nil {
		return err
	}
	path := f.Path(name)
	// fileperm.OpenFile restricts the handle before anything is written
	// through it, so there is no instant at which the key exists on disk
	// reachable by another account. The 0o600 alone is the whole answer on
	// unix and, on Windows, the read-only attribute and whatever list the
	// file inherited -- the mode said private and the file was not (5.161).
	//
	// This was an os.OpenFile and an ApplyFile beside it until the sweep in
	// 5.164 gave the pair a name. Two calls that must both happen are one
	// call now, which is the whole point of the sweep: the second half is
	// what gets forgotten.
	file, err := fileperm.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already holds a private key; move it aside deliberately if it is to be replaced", path)
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer file.Close()
	if _, err := file.Write(pemBytes); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return file.Sync()
}

// ReadKey loads a private key.
func (f Files) ReadKey(name string) (crypto.Signer, error) {
	raw, err := os.ReadFile(f.Path(name))
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
	raw, err := os.ReadFile(f.Path(name))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", f.Path(name), err)
	}
	return DecodeCert(raw)
}

// KeyPair loads a certificate and its key as a TLS pair, checking that
// they belong together rather than failing later in a handshake.
func (f Files) KeyPair(certName, keyName string) (tls.Certificate, error) {
	certPEM, err := os.ReadFile(f.Path(certName))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", f.Path(certName), err)
	}
	keyPEM, err := os.ReadFile(f.Path(keyName))
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
func (f Files) CreateCA(alg KeyAlgorithm, commonName string, lifetime time.Duration) (*CA, error) {
	if f.Exists(CACertFile) || f.Exists(CAKeyFile) {
		return nil, fmt.Errorf("%s already holds an enrollment CA; `keys rotate-ca` is how one is replaced", f.Dir)
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
}

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
