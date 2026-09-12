package builtin

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// tlsDefaultCADays is how long a CA this module creates lasts when a
// tree does not say. Ten years is Salt's own `tls.create_ca` default,
// and a CA that outlives every certificate it will ever sign is the
// point of a CA in the first place.
const tlsDefaultCADays = 3650

// registerTLS installs the `tls` module of SPEC 15.2: a self-managed
// certificate authority, kept as a directory this module owns, that
// issues and revokes certificates under it.
//
// # This is not a second certificate engine
//
// Every byte of X.509 in this file goes through createCertificate,
// generateKey, encodePrivateKey, loadPrivateKey, and loadCertificate —
// the same unexported functions x509.go and x509_module.go already
// export to this package for `x509.create_certificate` and its
// siblings. Salt's own `tls` and `x509` modules duplicate a great deal
// of certificate-building code between them because they come from
// different eras of the project; there is no reason to reproduce that
// duplication here just because Salt has it, and doing so would leave
// two independent places a certificate-generation bug could hide in
// one binary.
//
// What `tls` adds instead is everything `x509` deliberately does not
// do: a *place* a CA lives (a directory under `cert_base_path`, named
// by `ca_name`, holding `ca_cert.crt` and `ca_cert.key`), an idempotent
// `create_ca` that returns the CA already there instead of minting a
// new one on every call, a ledger of what a CA has issued
// (`index.json`, read by `list_certs`), and revocation
// (`revoke_cert`, which `x509` and `openssl_cert` both stop short of:
// `openssl_cert.crl_info` only *reads* a CRL that `openssl` produced,
// it does not create one).
//
// # What was deliberately not built
//
// `create_csr`, `create_self_signed_cert`, `cert_info`, and
// `get_public_key` would each be a thin rename of an `x509` function
// that already takes an explicit `path`, including one inside a CA's
// own directory. A tree that wants a CSR or a self-signed certificate
// filed under `cert_base_path` can already ask `x509` to write it
// there; a second name for the same call is not a feature, and SPEC
// 15.6's signature registry would carry two entries that do the same
// thing for no reason a caller could point to.
//
// # Revocation, and the one new primitive it needs
//
// `revoke_cert` is the one place this file reaches past what `x509`
// exposes, because revocation needs a CRL and nothing in this tree
// builds one: `crypto/x509.CreateRevocationList` is called directly,
// signed by the CA's own key, over every certificate `index.json`
// currently marks revoked — not only the one a single call adds — so
// the CRL on disk is always the complete list a verifier needs, never
// a diff of one call's effect on the last file.
//
// # Index format
//
// `index.json` is a JSON array of small objects (serial_number,
// common_name, not_after, status, revoked_at), not the semicolon
// format OpenSSL's own `ca` command writes: nothing else in this
// binary or in a halite tree reads OpenSSL's CA database, and
// SPEC 6.4 already settled JSON as the form a halite component writes
// when it has no other consumer to match.
func registerTLS(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "tls", Function: "create_ca",
				Doc: "Create a self-signed certificate authority under cert_base_path, " +
					"or return the one already there.",
				Params:   tlsCreateCAParams(),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: tlsCreateCAFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tls", Function: "ca_exists",
				Doc:      "Report whether a CA's certificate and key are both present.",
				Params:   tlsCANameParams(),
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: tlsCAExistsFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tls", Function: "get_ca",
				Doc:      "Return a CA's certificate, as a path and as PEM.",
				Params:   tlsCANameParams(),
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: tlsGetCAFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tls", Function: "create_ca_signed_cert",
				Doc:      "Issue a certificate signed by a CA this module manages, and record it in the CA's index.",
				Params:   tlsCreateCASignedCertParams(),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: tlsCreateCASignedCertFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tls", Function: "revoke_cert",
				Doc: "Revoke every currently valid certificate a CA issued for a common " +
					"name, and regenerate its CRL.",
				Params: append(tlsCANameParams(),
					req("common_name", signature.String, "The certificate's common name."),
					opt("crl_days", signature.Int, int64(30), "How long the regenerated CRL is valid for."),
				),
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: tlsRevokeCertFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tls", Function: "list_certs",
				Doc:      "Return every certificate a CA's index records, valid and revoked.",
				Params:   tlsCANameParams(),
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: tlsListCertsFn,
		},
	)
}

// ---- shared parameter lists ----

func tlsCANameParams() []signature.Param {
	return []signature.Param{
		req("ca_name", signature.String, "The CA's name, which names its directory under cert_base_path."),
		req("cert_base_path", signature.Path, "The directory every CA this module manages lives under."),
	}
}

func tlsCreateCAParams() []signature.Param {
	p := append(tlsCANameParams(),
		opt("days", signature.Int, int64(tlsDefaultCADays), "How long the CA certificate lasts."),
		opt("force", signature.Bool, false,
			"Replace an existing CA even though every certificate it already signed "+
				"would stop verifying against the new one."),
	)
	p = append(p, keyParams()...)
	p = append(p, subjectParams()...)
	return p
}

func tlsCreateCASignedCertParams() []signature.Param {
	p := append(tlsCANameParams(),
		opt("days", signature.Int, int64(defaultCertDays), "How long the certificate lasts."),
		opt("overwrite", signature.Bool, false, "Reissue even though this common name already has a certificate."),
	)
	p = append(p, keyParams()...)
	p = append(p, subjectParams()...)
	return p
}

// ---- the CA directory convention ----

func tlsCADir(certBasePath, caName string) string { return filepath.Join(certBasePath, caName) }
func tlsCACertPath(dir string) string             { return filepath.Join(dir, "ca_cert.crt") }
func tlsCAKeyPath(dir string) string              { return filepath.Join(dir, "ca_cert.key") }
func tlsCertsDir(dir string) string               { return filepath.Join(dir, "certs") }
func tlsIndexPath(dir string) string              { return filepath.Join(dir, "index.json") }
func tlsCRLPath(dir string) string                { return filepath.Join(dir, "crl.pem") }

func tlsRequireCAName(args *value.Map) (caName, certBasePath string, err error) {
	caName = strings.TrimSpace(states.Str(args, "ca_name", ""))
	if caName == "" {
		return "", "", errors.New("a ca_name must be given")
	}
	certBasePath = strings.TrimSpace(states.Str(args, "cert_base_path", ""))
	if certBasePath == "" {
		return "", "", errors.New("a cert_base_path must be given")
	}
	return caName, certBasePath, nil
}

func tlsCAFilesExist(dir string) bool {
	_, certErr := os.Stat(tlsCACertPath(dir))
	_, keyErr := os.Stat(tlsCAKeyPath(dir))
	return certErr == nil && keyErr == nil
}

// ---- the issuance index ----

// tlsNow is the clock revoke_cert and create_ca_signed_cert stamp their
// index entries with, overridable so a test can hold it still.
var tlsNow = time.Now

func tlsReadIndex(path string) ([]*value.Map, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s could not be read: %w", path, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	decoded, err := value.DecodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s does not hold valid JSON: %w", path, err)
	}
	items, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("%s holds a %s, not a list", path, value.TypeName(decoded))
	}
	out := make([]*value.Map, 0, len(items))
	for _, item := range items {
		m, ok := item.(*value.Map)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func tlsWriteIndex(path string, entries []*value.Map) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%s could not be created: %w", filepath.Dir(path), err)
	}
	list := make([]any, len(entries))
	for i, e := range entries {
		list[i] = e
	}
	encoded, err := value.EncodeJSON(list, 2)
	if err != nil {
		return err
	}
	return writeAtomic(path, encoded, 0o644)
}

// ---- mutate reporting ----

// tlsMutateResult follows the changed/comment/changes shape every
// mutating exec-only module in this package uses, with room for the
// extra fields a certificate operation hands back — a path, a PEM
// blob, a serial number — that a caller needs and a bare boolean
// cannot carry.
func tlsMutateResult(c *exec.Context, changed bool, comment string, change, extra *value.Map) *value.Map {
	out := value.NewMap(3 + extra.Len())
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	for _, e := range extra.Entries() {
		out.Set(value.KeyString(e.Key), e.Val)
	}
	return out
}

// ---- create_ca ----

func tlsCreateCAFn(c *exec.Context, args *value.Map) (any, error) {
	caName, certBasePath, err := tlsRequireCAName(args)
	if err != nil {
		return nil, err
	}
	dir := tlsCADir(certBasePath, caName)
	certPath, keyPath := tlsCACertPath(dir), tlsCAKeyPath(dir)
	force := states.Bool(args, "force", false)
	exists := tlsCAFilesExist(dir)

	if exists && !force {
		return tlsMutateResult(c, false, fmt.Sprintf("the %s CA already exists at %s.", caName, dir), nil,
			value.MapOf("certificate", certPath, "private_key", keyPath)), nil
	}

	spec, err := keySpecFrom(args)
	if err != nil {
		return nil, err
	}

	verb := "created"
	if exists {
		verb = "replaced"
	}
	if c.Test {
		return tlsMutateResult(c, true, fmt.Sprintf("the %s CA would be %s at %s.", caName, verb, dir), nil, nil), nil
	}

	key, err := generateKey(spec)
	if err != nil {
		return nil, err
	}
	keyPEM, err := encodePrivateKey(key)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(tlsCertsDir(dir), 0o755); err != nil {
		return nil, fmt.Errorf("%s could not be created: %w", tlsCertsDir(dir), err)
	}
	if err := writeAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	certArgs := args.Clone()
	certArgs.Set("private_key", string(keyPEM))
	certArgs.Set("ca", true)
	certArgs.Set("days_valid", states.Int(args, "days", tlsDefaultCADays))
	if _, err := createCertificate(certArgs, certPath, 0o644); err != nil {
		return nil, err
	}

	change := value.MapOf(dir, states.Change(nil, "a certificate authority"))
	return tlsMutateResult(c, true, fmt.Sprintf("the %s CA was %s at %s.", caName, verb, dir), change,
		value.MapOf("certificate", certPath, "private_key", keyPath)), nil
}

// ---- ca_exists, get_ca ----

func tlsCAExistsFn(c *exec.Context, args *value.Map) (any, error) {
	caName, certBasePath, err := tlsRequireCAName(args)
	if err != nil {
		return nil, err
	}
	return tlsCAFilesExist(tlsCADir(certBasePath, caName)), nil
}

func tlsGetCAFn(c *exec.Context, args *value.Map) (any, error) {
	caName, certBasePath, err := tlsRequireCAName(args)
	if err != nil {
		return nil, err
	}
	dir := tlsCADir(certBasePath, caName)
	certPath := tlsCACertPath(dir)
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("the %s CA does not exist at %s: %w", caName, dir, err)
	}
	out := value.NewMap(2)
	out.Set("path", certPath)
	out.Set("certificate", string(pemBytes))
	return out, nil
}

// ---- create_ca_signed_cert ----

func tlsCreateCASignedCertFn(c *exec.Context, args *value.Map) (any, error) {
	caName, certBasePath, err := tlsRequireCAName(args)
	if err != nil {
		return nil, err
	}
	cn := strings.TrimSpace(states.Str(args, "CN", ""))
	if cn == "" {
		return nil, errors.New("a CN must be given for the certificate being issued")
	}
	dir := tlsCADir(certBasePath, caName)
	if !tlsCAFilesExist(dir) {
		return nil, fmt.Errorf("the %s CA does not exist at %s; call tls.create_ca first", caName, dir)
	}

	certsDir := tlsCertsDir(dir)
	certPath := filepath.Join(certsDir, cn+".crt")
	keyPath := filepath.Join(certsDir, cn+".key")
	overwrite := states.Bool(args, "overwrite", false)
	if _, err := os.Stat(certPath); err == nil && !overwrite {
		return tlsMutateResult(c, false, fmt.Sprintf("%s already has a certificate at %s.", cn, certPath), nil,
			value.MapOf("certificate", certPath, "private_key", keyPath)), nil
	}

	spec, err := keySpecFrom(args)
	if err != nil {
		return nil, err
	}
	if c.Test {
		return tlsMutateResult(c, true, fmt.Sprintf("a certificate for %s would be issued by %s.", cn, caName), nil, nil), nil
	}

	caCertPEM, err := os.ReadFile(tlsCACertPath(dir))
	if err != nil {
		return nil, fmt.Errorf("%s could not be read: %w", tlsCACertPath(dir), err)
	}
	caKeyPEM, err := os.ReadFile(tlsCAKeyPath(dir))
	if err != nil {
		return nil, fmt.Errorf("%s could not be read: %w", tlsCAKeyPath(dir), err)
	}

	key, err := generateKey(spec)
	if err != nil {
		return nil, err
	}
	keyPEM, err := encodePrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return nil, fmt.Errorf("%s could not be created: %w", certsDir, err)
	}
	if err := writeAtomic(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	certArgs := args.Clone()
	certArgs.Set("private_key", string(keyPEM))
	certArgs.Set("signing_cert", string(caCertPEM))
	certArgs.Set("signing_private_key", string(caKeyPEM))
	certArgs.Set("ca", false)
	certArgs.Set("days_valid", states.Int(args, "days", defaultCertDays))
	if _, err := createCertificate(certArgs, certPath, 0o644); err != nil {
		return nil, err
	}

	issued, err := loadCertificate(certPath)
	if err != nil {
		return nil, fmt.Errorf("the certificate just written: %w", err)
	}
	indexPath := tlsIndexPath(dir)
	entries, err := tlsReadIndex(indexPath)
	if err != nil {
		return nil, err
	}
	entry := value.NewMap(4)
	entry.Set("serial_number", issued.SerialNumber.Text(16))
	entry.Set("common_name", cn)
	entry.Set("not_after", issued.NotAfter.UTC().Format(time.RFC3339))
	entry.Set("status", "valid")
	entries = append(entries, entry)
	if err := tlsWriteIndex(indexPath, entries); err != nil {
		return nil, err
	}

	change := value.MapOf(cn, states.Change(nil, "issued"))
	return tlsMutateResult(c, true, fmt.Sprintf("a certificate for %s was issued by %s.", cn, caName), change,
		value.MapOf("certificate", certPath, "private_key", keyPath, "serial_number", issued.SerialNumber.Text(16))), nil
}

// ---- revoke_cert ----

func tlsRevokeCertFn(c *exec.Context, args *value.Map) (any, error) {
	caName, certBasePath, err := tlsRequireCAName(args)
	if err != nil {
		return nil, err
	}
	cn := strings.TrimSpace(states.Str(args, "common_name", ""))
	if cn == "" {
		return nil, errors.New("a common_name must be given")
	}
	dir := tlsCADir(certBasePath, caName)
	if !tlsCAFilesExist(dir) {
		return nil, fmt.Errorf("the %s CA does not exist at %s; call tls.create_ca first", caName, dir)
	}

	indexPath := tlsIndexPath(dir)
	entries, err := tlsReadIndex(indexPath)
	if err != nil {
		return nil, err
	}

	revokedAt := tlsNow().UTC().Format(time.RFC3339)
	matched := 0
	for _, e := range entries {
		status, _ := e.GetString("status")
		name, _ := e.GetString("common_name")
		if name != cn || status != "valid" {
			continue
		}
		e.Set("status", "revoked")
		e.Set("revoked_at", revokedAt)
		matched++
	}
	if matched == 0 {
		return tlsMutateResult(c, false, fmt.Sprintf("no valid certificate for %s was found to revoke.", cn), nil, nil), nil
	}

	change := value.MapOf(cn, states.Change("valid", "revoked"))
	if c.Test {
		return tlsMutateResult(c, true,
			fmt.Sprintf("%d certificate(s) for %s would be revoked.", matched, cn), change, nil), nil
	}

	caCert, err := loadCertificate(tlsCACertPath(dir))
	if err != nil {
		return nil, fmt.Errorf("the CA certificate: %w", err)
	}
	caKeyPEM, err := os.ReadFile(tlsCAKeyPath(dir))
	if err != nil {
		return nil, fmt.Errorf("%s could not be read: %w", tlsCAKeyPath(dir), err)
	}
	caKey, err := loadPrivateKey(string(caKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("the CA private key: %w", err)
	}

	crlPath, err := tlsRegenerateCRL(dir, entries, caCert, caKey, states.Int(args, "crl_days", 30))
	if err != nil {
		return nil, err
	}
	if err := tlsWriteIndex(indexPath, entries); err != nil {
		return nil, err
	}

	return tlsMutateResult(c, true,
		fmt.Sprintf("%d certificate(s) for %s were revoked; the CRL was regenerated.", matched, cn), change,
		value.MapOf("crl", crlPath)), nil
}

// tlsRegenerateCRL rebuilds the whole CRL from the index's current
// revoked entries, rather than appending to whatever CRL was on disk:
// crypto/x509 has no notion of amending a CRL, and rebuilding from the
// index that is already this CA's record of every revocation means the
// two can never drift against each other.
func tlsRegenerateCRL(dir string, entries []*value.Map, caCert *x509.Certificate, caKey crypto.Signer, crlDays int64) (string, error) {
	var revoked []x509.RevocationListEntry
	for _, e := range entries {
		status, _ := e.GetString("status")
		if status != "revoked" {
			continue
		}
		serialHex, _ := e.GetString("serial_number")
		serial, ok := new(big.Int).SetString(value.KeyString(serialHex), 16)
		if !ok {
			continue
		}
		revokedAt, _ := e.GetString("revoked_at")
		when, err := time.Parse(time.RFC3339, value.KeyString(revokedAt))
		if err != nil {
			when = tlsNow()
		}
		revoked = append(revoked, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: when})
	}

	now := tlsNow()
	if crlDays <= 0 {
		crlDays = 30
	}
	tmpl := &x509.RevocationList{
		Number:                    big.NewInt(int64(len(revoked))),
		ThisUpdate:                now,
		NextUpdate:                now.Add(time.Duration(crlDays) * 24 * time.Hour),
		RevokedCertificateEntries: revoked,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, caCert, caKey)
	if err != nil {
		return "", fmt.Errorf("the CRL could not be created: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	crlPath := tlsCRLPath(dir)
	if err := writeAtomic(crlPath, encoded, 0o644); err != nil {
		return "", err
	}
	return crlPath, nil
}

// ---- list_certs ----

func tlsListCertsFn(c *exec.Context, args *value.Map) (any, error) {
	caName, certBasePath, err := tlsRequireCAName(args)
	if err != nil {
		return nil, err
	}
	entries, err := tlsReadIndex(tlsIndexPath(tlsCADir(certBasePath, caName)))
	if err != nil {
		return nil, err
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return out, nil
}
