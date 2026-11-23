package doctor

import (
	"context"
	"crypto/x509"
	"strings"
	"testing"
	"time"
)

// TestEachCertificateKindHasItsOwnRemedy. The remedy for a certificate
// near its end was one sentence for every certificate -- `halite-node
// renew` on a node, `keys operator create` for an operator -- so the API's
// serving certificate, which neither renews, was sent to both. Issue 813.
//
// Each kind is checked alone, expiring and expired, for the command or
// fact that renews it and for the absence of the others' commands: a
// remedy that names every command is right for one certificate and wrong
// for the rest. That every command named exists is held separately, in
// internal/docsaudit, against each program's dispatch.
func TestEachCertificateKindHasItsOwnRemedy(t *testing.T) {
	cases := []struct {
		kind  CertificateKind
		names []string
		never []string
	}{
		{NodeCertificate,
			[]string{"`halite-node renew`", "`halite-node enroll --force`"},
			[]string{"keys operator create", "tls_cert"}},
		{HubCertificate,
			[]string{"`halite-hub serve`", "renewing the hub's certificate failed"},
			[]string{"halite-node renew", "keys operator create", "tls_cert"}},
		{OperatorCertificate,
			[]string{"`halite-hub keys operator create <name>`", "cert:CN=<name>"},
			[]string{"halite-node renew", "tls_cert", "no restart"}},
		{APIOperatorCertificate,
			[]string{"`halite-hub keys operator create <name>`", "no restart"},
			[]string{"halite-node renew", "tls_cert"}},
		{APIServingCertificate,
			[]string{"the CA that issued it", "`tls_cert`", "no restart", "no halite command issues it"},
			[]string{"halite-node", "keys operator create", "halite-hub"}},
		{EnrollmentCA,
			[]string{"no rotation", "chapter 37"},
			[]string{"halite-node renew", "keys operator create", "tls_cert"}},
	}
	covered := map[CertificateKind]bool{UnclassifiedCertificate: true}
	now := time.Now()
	for _, tc := range cases {
		covered[tc.kind] = true
		for _, window := range []struct {
			name     string
			notAfter time.Time
			status   Status
		}{
			{"expiring", now.Add(10 * 24 * time.Hour), Warn},
			{"expired", now.Add(-time.Hour), Fail},
		} {
			res := CertificateExpiry(map[string]Expected{
				"the certificate": {
					Kind:       tc.kind,
					Cert:       &x509.Certificate{NotBefore: now.Add(-60 * 24 * time.Hour), NotAfter: window.notAfter},
					WarnWithin: 30 * 24 * time.Hour,
				},
			}, now).Run(context.Background())
			if res.Status != window.status {
				t.Errorf("kind %d %s: status %s, want %s", tc.kind, window.name, res.Status, window.status)
			}
			for _, want := range tc.names {
				if !strings.Contains(res.Remedy, want) {
					t.Errorf("kind %d %s: the remedy does not name %q:\n%s",
						tc.kind, window.name, want, res.Remedy)
				}
			}
			for _, wrong := range tc.never {
				if strings.Contains(res.Remedy, wrong) {
					t.Errorf("kind %d %s: the remedy names %q, which does not renew it:\n%s",
						tc.kind, window.name, wrong, res.Remedy)
				}
			}
		}
	}
	for _, kind := range CertificateKinds() {
		if !covered[kind] {
			t.Errorf("kind %d has a remedy this test does not hold to anything", kind)
		}
	}
}

// Two kinds expiring together get both remedies, each once, and a kind
// that is healthy gets none: the remedy follows the findings.
func TestOnlyTheKindsFoundAreGivenRemedies(t *testing.T) {
	now := time.Now()
	soon := &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(5 * 24 * time.Hour)}
	fine := &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(900 * 24 * time.Hour)}
	res := CertificateExpiry(map[string]Expected{
		"serving":   {Kind: APIServingCertificate, Cert: soon, WarnWithin: 30 * 24 * time.Hour},
		"operator":  {Kind: APIOperatorCertificate, Cert: soon, WarnWithin: 7 * 24 * time.Hour},
		"operator2": {Kind: APIOperatorCertificate, Cert: soon, WarnWithin: 7 * 24 * time.Hour},
		"ca":        {Kind: EnrollmentCA, Cert: fine, WarnWithin: 30 * 24 * time.Hour},
	}, now).Run(context.Background())
	if res.Status != Warn {
		t.Fatalf("status %s, want warn", res.Status)
	}
	for kind, want := range map[CertificateKind]int{
		APIServingCertificate: 1, APIOperatorCertificate: 1, EnrollmentCA: 0,
	} {
		if got := strings.Count(res.Remedy, CertificateRemedy(kind)); got != want {
			t.Errorf("kind %d's remedy appears %d times, want %d:\n%s", kind, got, want, res.Remedy)
		}
	}
}
