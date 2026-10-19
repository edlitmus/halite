package apitoken

import (
	"fmt"
	"testing"
)

// What a request with a made-up bearer token costs, by the number of
// tokens in the store. The API answers that request without
// authentication, so it is what anybody who can reach the port can make
// it do. Issue 321, M-07: before the digest index it read and parsed
// every record, about 4.2 s a request at 5,000 tokens on macOS.
func BenchmarkRedeemAGarbageToken(b *testing.B) {
	for _, n := range []int{10, 1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			s, err := Open(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < n; i++ {
				if _, _, err := s.Issue(Options{Principal: "local:ed"}); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Redeem("not-a-token", "10.0.0.1:1"); err == nil {
					b.Fatal("a made-up token was redeemed")
				}
			}
		})
	}
}
