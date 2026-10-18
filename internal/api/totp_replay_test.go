package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/account"
)

// rfc6238 is the code for a step, computed here from the RFC rather than
// through the package under test.
func rfc6238(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// **A TOTP code logs in once** (issue 321, section 5). There was no record
// of the steps used, so the same code -- read over a shoulder, or out of a
// proxy's log -- logged in again for as long as its window lasted, up to
// ninety seconds with the one step of skew either side. RFC 6238 5.2 says
// the second attempt MUST NOT be accepted.
func TestATOTPCodeLogsInOnce(t *testing.T) {
	if !account.TOTPAvailable() {
		t.Skip("TOTP is unavailable in this build (FIPS)")
	}
	l := newLab(t)
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	hash, err := account.Hash("hunter2", account.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	l.server.Accounts.Accounts["mfa"] = &account.Account{Name: "mfa", Hash: hash, TOTP: secret}
	base := time.Unix(1_800_000_000, 0)
	l.server.Now = func() time.Time { return base }
	step := base.Unix() / 30
	login := func(code string) int {
		t.Helper()
		res, _ := l.post(t, PathLogin, `{"username":"mfa","password":"hunter2","code":"`+code+`"}`, "")
		return res.StatusCode
	}
	if got := login(rfc6238(t, secret, step)); got != http.StatusOK {
		t.Fatalf("the first login with a good code answered %d", got)
	}
	if got := login(rfc6238(t, secret, step)); got != http.StatusUnauthorized {
		t.Errorf("the same code logged in a second time: %d", got)
	}
	// After the backoff the failure earned, the next step's code, inside
	// the skew, is a new code and is good.
	l.passTheLoginBackoff()
	now := l.server.now().Unix() / 30
	if got := login(rfc6238(t, secret, now+1)); got != http.StatusOK {
		t.Errorf("the next step's code answered %d", got)
	}
	// And an earlier step, still inside the skew, is refused once a later
	// one has been used.
	if got := login(rfc6238(t, secret, now)); got != http.StatusUnauthorized {
		t.Errorf("an earlier step's code logged in after a later one: %d", got)
	}
}
