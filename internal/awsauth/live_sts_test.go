package awsauth

import (
	"context"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Issue 613's findings against the real services, from an EC2 instance:
// its metadata service and STS. Gated on HALITE_SYSTEM_LIVE=1, and skipped,
// saying why, anywhere the metadata service does not answer. Nothing here
// changes the instance: the metadata reads are reads, and the STS call is
// GetCallerIdentity. Nothing logged is a credential; the STS answers are
// reduced to their status and AWS error code.

func liveEC2(t *testing.T) (*IMDS, string) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on an EC2 instance to reach its metadata service and STS")
	}
	m := &IMDS{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	region, err := m.Get(ctx, "latest/meta-data/placement/region")
	if err != nil {
		t.Skipf("this is not an EC2 instance whose metadata service answers IMDSv2: %v", err)
	}
	return m, strings.TrimSpace(string(region))
}

var awsErrorCode = regexp.MustCompile(`<Code>([^<]+)</Code>`)

// Item 3: STS checks the
// signature over the query as SigV4 orders it. The control is the same
// call without the keys that sort differently encoded.
func TestLiveSTSAcceptsASignedQueryWhoseKeysSortDifferentlyEncoded(t *testing.T) {
	_, region := liveEC2(t)
	p := &Provider{Partition: "aws", Region: region}
	creds, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("the instance's credentials: %v", err)
	}
	suffix, err := PartitionSuffix("aws")
	if err != nil {
		t.Fatal(err)
	}
	base, err := ServiceURL("sts", region, suffix)
	if err != nil {
		t.Fatal(err)
	}
	call := func(extra string) (int, string) {
		u := *base
		u.Path = "/"
		u.RawQuery = "Action=GetCallerIdentity&Version=2011-06-15" + extra
		req, err := http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := (Signer{Region: region, Service: "sts"}).Sign(req, creds, HexSHA256(nil), time.Now()); err != nil {
			t.Fatal(err)
		}
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		code := "-"
		if m := awsErrorCode.FindSubmatch(body); m != nil {
			code = string(m[1])
		}
		return res.StatusCode, code
	}

	status, code := call("")
	t.Logf("control, GetCallerIdentity: %d %s", status, code)
	if status != http.StatusOK {
		t.Fatalf("the control call failed (%d %s), so nothing below would say anything about the query's order", status, code)
	}
	for _, extra := range []string{"&ab=2&a%7B=1", "&z=ab&z=a%7B"} {
		status, code := call(extra)
		t.Logf("with %s: %d %s", extra, status, code)
		if code == "SignatureDoesNotMatch" {
			t.Errorf("with %s STS computed a different signature: the canonical query is not in SigV4's order", extra)
		}
	}
}
