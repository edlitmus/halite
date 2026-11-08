package keystore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// filledStore is a manual-mode Authority over a store already holding n
// accepted records, written directly so that filling it costs no
// signatures.
func filledStore(tb testing.TB, n int) *Authority {
	tb.Helper()
	store, err := Open(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	ca, err := pki.NewCA(pki.ECDSAP256, "bench CA", 365*24*time.Hour)
	if err != nil {
		tb.Fatal(err)
	}
	now := time.Now()
	err = store.locked(func() error {
		for i := range n {
			if err := store.put(&Record{
				NodeID: fmt.Sprintf("old%06d.example", i), State: Accepted, Source: SourceManual,
				Fingerprint: "SHA256:x", Serial: "1", NotAfter: now.Add(DefaultLifetime),
				FirstSeen: now, Updated: now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	return &Authority{Store: store, CA: ca, Mode: ModeManual, Lifetime: DefaultLifetime}
}

// BenchmarkIssueIntoAStoreOf is what `simfleet setup` does per identity
// -- enrol a new name, accept it, enrol again to collect -- into a store
// that already holds a given number of nodes. DIVERGENCE 5.279's AWS run
// measured this rising in a straight line with the store's size; the
// cost per op here should not depend on the size at all.
func BenchmarkIssueIntoAStoreOf(b *testing.B) {
	for _, n := range []int{500, 2000, 8000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			a := filledStore(b, n)
			// Build the pending index outside the timing: it reads every
			// record once, on the first count a store ever makes, and
			// 200 iterations would otherwise carry 1/200th of it each.
			if err := a.Store.locked(a.Store.ensurePendingIndex); err != nil {
				b.Fatal(err)
			}
			i := 0
			for b.Loop() {
				b.StopTimer()
				id := fmt.Sprintf("new%06d.example", i)
				i++
				csr := benchRequest(b, id)
				b.StartTimer()
				if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
					b.Fatal(err)
				}
				if _, err := a.Accept(id); err != nil {
					b.Fatal(err)
				}
				if _, err := a.Enroll(Request{CSR: csr}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchRequest(tb testing.TB, nodeID string) []byte {
	tb.Helper()
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		tb.Fatal(err)
	}
	der, err := pki.NewNodeCSR(key, nodeID)
	if err != nil {
		tb.Fatal(err)
	}
	return pki.EncodeCSR(der)
}
