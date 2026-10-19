package keystore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// crlNumberName is the file holding the last CRL number issued. It is not
// a .json file, so List and countPending do not read it as a node.
const crlNumberName = "crl-number"

// CRLEntries is every serial the hub denies that a CRL should name: the
// handshake's denylist, as LoadDenylist rebuilds it, in the form a CRL
// carries.
//
// It used to be the serials of records in state revoked and nothing else.
// The denylist has two more sources -- the serials renewals replaced
// (5.392) and the certificates of deleted records (5.393) -- and a load
// balancer or an OpenSSL command line reading the CRL accepted both,
// where the hub refused them. A deleted record that had been revoked was
// on the CRL until the delete and off it afterwards.
//
// Revoked records are listed whatever their notAfter, as they always
// were. The other two are listed while their certificates are live, which
// is as long as the store keeps them anyway.
func (a *Authority) CRLEntries() ([]pki.RevokedCert, error) {
	a.Store.mu.Lock()
	defer a.Store.mu.Unlock()
	return a.crlEntries()
}

// crlEntries is CRLEntries for a caller that holds the store's lock.
func (a *Authority) crlEntries() ([]pki.RevokedCert, error) {
	records, err := a.Store.list()
	if err != nil {
		return nil, err
	}
	deleted, err := a.Store.listDeleted()
	if err != nil {
		return nil, err
	}
	now := a.now()
	seen := map[string]bool{}
	var out []pki.RevokedCert
	add := func(r pki.RevokedCert) {
		if !seen[r.Serial] {
			seen[r.Serial] = true
			out = append(out, r)
		}
	}
	for _, rec := range records {
		if rec.State == Revoked && rec.Serial != "" {
			add(pki.RevokedCert{Serial: rec.Serial, At: rec.Updated})
		}
		for _, sup := range rec.Superseded {
			if sup.Live(now) {
				add(pki.RevokedCert{Serial: sup.Serial, At: sup.At, Reason: pki.ReasonSuperseded})
			}
		}
	}
	for _, d := range deleted {
		if !d.Live(now) {
			continue
		}
		switch {
		case d.Reason == ReasonSuperseded:
			add(pki.RevokedCert{Serial: d.Serial, At: d.Deleted, Reason: pki.ReasonSuperseded})
		case !d.Revoked.IsZero():
			add(pki.RevokedCert{Serial: d.Serial, At: d.Revoked})
		default:
			add(pki.RevokedCert{Serial: d.Serial, At: d.Deleted, Reason: pki.ReasonCessationOfOperation})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out, nil
}

// CRL signs a revocation list of CRLEntries, and returns it with the
// number of entries and the CRL number it was given.
//
// The number was the count of revoked records plus one. Its comment said
// that rises whenever the list changes and never falls, and it fell: a
// deleted revoked record took one off it, and so would every superseded
// or deleted serial that expired out of the list. RFC 5280 section 5.2.3
// has the number increase monotonically, because that is how a reader
// tells a newer list from an older one; a reader holding number 6 that
// is offered number 5 keeps the stale list.
//
// So the number is kept in the store, under its lock, and each list is
// given one more than the last -- or the current Unix time in seconds, if
// that is larger. The floor is for the first list after this change: the
// count-based numbers already handed out are unknown, but every one of
// them is smaller than a time, so the new sequence starts above them all.
// After that the stored number carries it, so a clock stepped backwards
// cannot make it fall either.
func (a *Authority) CRL(lifetime time.Duration) ([]byte, int, int64, error) {
	var (
		pem    []byte
		n      int
		number int64
	)
	err := a.Store.locked(func() error {
		entries, err := a.crlEntries()
		if err != nil {
			return err
		}
		last, err := a.Store.crlNumber()
		if err != nil {
			return err
		}
		number = max(last+1, a.now().Unix())
		pem, err = a.CA.CRL(entries, number, lifetime)
		if err != nil {
			return err
		}
		n = len(entries)
		return a.Store.putCRLNumber(number)
	})
	return pem, n, number, err
}

func (s *Store) crlNumber() (int64, error) {
	path := filepath.Join(s.dir, crlNumberName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the last CRL number: %w", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || n < 0 {
		// Refused rather than restarted from zero: a number that went
		// backwards is the defect this file exists to prevent.
		return 0, fmt.Errorf("the last CRL number at %s is not a number: %q", path, raw)
	}
	return n, nil
}

func (s *Store) putCRLNumber(n int64) error {
	return writeAtomic(filepath.Join(s.dir, crlNumberName), []byte(strconv.FormatInt(n, 10)+"\n"), 0o600)
}
