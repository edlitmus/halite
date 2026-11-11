package keystore

import (
	"errors"
	"testing"
	"time"
)

// **TokenAdmits and Enroll say the same thing.** The hub lets a request
// past its enrollment limit uncharged when TokenAdmits says its token
// would spend (DIVERGENCE 5.656), so a case where TokenAdmits says yes
// and Enroll then does not issue is a request that filled a pending
// record for free. Each case asks TokenAdmits, then Enroll, and holds the
// two to one answer: every way a token is refused, and the one way it
// is not.
func TestTokenAdmitsAgreesWithEnroll(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   Mode
		opts   TokenOptions
		node   string
		from   string
		secret func(real string) string
		before func(a *Authority, tok *Token, secret string)
		want   bool
	}{
		{name: "in scope", node: "web1.example", from: "10.1.2.3:9000", want: true},
		{name: "outside the node scope", node: "db1.example", from: "10.1.2.3:9000"},
		{name: "outside the source scope", node: "web1.example", from: "192.0.2.1:9000"},
		{name: "made up", node: "web1.example", from: "10.1.2.3:9000",
			secret: func(string) string { return "not-a-token" }},
		{name: "no token", node: "web1.example", from: "10.1.2.3:9000",
			secret: func(string) string { return "" }},
		{name: "manual mode", mode: ModeManual, node: "web1.example", from: "10.1.2.3:9000"},
		{name: "revoked", node: "web1.example", from: "10.1.2.3:9000",
			before: func(a *Authority, tok *Token, secret string) {
				if err := a.Store.RevokeToken(tok.ID); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "spent", node: "web1.example", from: "10.1.2.3:9000",
			before: func(a *Authority, tok *Token, secret string) {
				if _, err := a.Store.SpendToken(secret, "web9.example", "10.0.0.1:1", time.Now()); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "expired", opts: TokenOptions{TTL: time.Minute}, node: "web1.example", from: "10.1.2.3:9000",
			before: func(a *Authority, _ *Token, _ string) {
				later := time.Now().Add(time.Hour)
				a.Now = func() time.Time { return later }
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAuthority(t)
			a.Mode = ModeToken
			if tc.mode != "" {
				a.Mode = tc.mode
			}
			opts := tc.opts
			if opts.TTL == 0 {
				opts.TTL = time.Hour
			}
			opts.NodeGlob, opts.CIDR = "web*.example", "10.0.0.0/8"
			tok, secret, err := a.Store.MintToken(opts, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if tc.before != nil {
				tc.before(a, tok, secret)
			}
			if tc.secret != nil {
				secret = tc.secret(secret)
			}
			csr, _ := request(t, tc.node)
			req := Request{CSR: csr, Token: secret, RemoteAddr: tc.from}

			id, left, ok := a.TokenAdmits(req)
			if ok != tc.want {
				t.Fatalf("TokenAdmits said %v, want %v", ok, tc.want)
			}
			if ok && (id != tok.ID || left != 1) {
				t.Errorf("TokenAdmits named %s with %d left, want %s with 1", id, left, tok.ID)
			}
			_, err = a.Enroll(req)
			if issued := err == nil; issued != ok {
				t.Errorf("TokenAdmits said %v and Enroll then gave %v", ok, err)
			}
			if !ok && err == nil {
				t.Error("Enroll issued on a token TokenAdmits refused")
			}
			if !ok && !errors.Is(err, ErrPending) && !errors.Is(err, ErrRefused) {
				t.Errorf("Enroll: %v", err)
			}
		})
	}
}
