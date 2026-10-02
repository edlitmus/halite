package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pki"
)

func certLiving(from time.Time, life time.Duration) *x509.Certificate {
	return &x509.Certificate{NotBefore: from, NotAfter: from.Add(life)}
}

func quietNode(t *testing.T) *node {
	t.Helper()
	logger, err := hlog.New(hlog.Options{Level: hlog.Error, Format: hlog.JSON, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return &node{log: logger, identityMu: &sync.Mutex{}}
}

// The interval scales with the certificate, inside its bounds.
func TestRenewalCheckEveryScalesWithTheLifetime(t *testing.T) {
	now := time.Now()
	for life, want := range map[time.Duration]time.Duration{
		90 * 24 * time.Hour: time.Hour,        // the default: hourly
		20 * time.Hour:      time.Hour,        // a twentieth exactly
		10 * time.Minute:    30 * time.Second, // scaled down
		time.Minute:         10 * time.Second, // floored
	} {
		if got := renewalCheckEvery(certLiving(now, life)); got != want {
			t.Errorf("life %s: every %s, want %s", life, got, want)
		}
	}
}

// The loop renews a certificate past half its life, at once, and not one
// before it; a failed renewal is tried again rather than ending the loop.
func TestRenewalsRenewWhenDueAndRetryAFailure(t *testing.T) {
	n := quietNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Past halfway of a one-minute certificate: due now, checked every
	// ten seconds. The first renewal fails, the second succeeds.
	var mu sync.Mutex
	cert := certLiving(time.Now().Add(-40*time.Second), time.Minute)
	attempts := 0
	done := make(chan struct{})
	go func() {
		n.runRenewals(ctx, func() (*x509.Certificate, error) {
			mu.Lock()
			defer mu.Unlock()
			return cert, nil
		}, func() (*x509.Certificate, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts == 1 {
				return nil, errors.New("the hub was not there")
			}
			cert = certLiving(time.Now(), time.Hour)
			close(done)
			return cert, nil
		})
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("no successful renewal after a failure (attempts %d)", attempts)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2: one failure, then one retry that worked", attempts)
	}
}

func TestRenewalsLeaveACertificateThatIsNotDue(t *testing.T) {
	n := quietNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	renewed := false
	n.runRenewals(ctx, func() (*x509.Certificate, error) {
		return certLiving(time.Now(), 90*24*time.Hour), nil
	}, func() (*x509.Certificate, error) {
		renewed = true
		return nil, nil
	})
	if renewed {
		t.Error("a certificate a day into its 90 was renewed")
	}
}

// A renewal keeps the node's key algorithm.
func TestAlgorithmOfNamesTheCurve(t *testing.T) {
	for curve, want := range map[elliptic.Curve]pki.KeyAlgorithm{
		elliptic.P256(): pki.ECDSAP256,
		elliptic.P384(): pki.ECDSAP384,
	} {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := pki.AlgorithmOf(key); err != nil || got != want {
			t.Errorf("%s: %q, %v", curve.Params().Name, got, err)
		}
	}
}
