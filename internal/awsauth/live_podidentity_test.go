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

// Issue 613, item 1, against the real thing: a pod on EKS with a Pod
// Identity association, whose agent answers on 169.254.170.23 and wants
// the pod's token. Gated on HALITE_SYSTEM_LIVE=1, and skipped, saying
// why, anywhere EKS has not set the two variables. Nothing here changes
// anything: the credential fetch is a read, and the STS call is
// GetCallerIdentity. Nothing logged is a credential.
//
// HALITE_POD_IDENTITY_ROLE, when set, is the role name the association
// grants; the caller's ARN must name it, which is what shows the
// credentials came from Pod Identity and not from the node's role
// through the metadata service.

var assumedRoleARN = regexp.MustCompile(`<Arn>([^<]+)</Arn>`)

func TestLivePodIdentityCredentialsAnswerSTS(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 in an EKS pod with a Pod Identity association")
	}
	uri := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	tokenFile := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")
	if uri == "" || tokenFile == "" {
		t.Skip("AWS_CONTAINER_CREDENTIALS_FULL_URI and AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE " +
			"are not both set, so this is not a pod EKS Pod Identity has configured")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		t.Skip("no AWS_REGION, which EKS sets in a pod with an association")
	}
	t.Logf("endpoint %s", uri)

	// Only the container path: the metadata service is reachable from a
	// pod too, and falling through to it would make this pass on the
	// node's role. checkFullURI and the fetch are what is under test.
	p := &Provider{Partition: "aws", Region: region}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	creds, err := p.fetchFullURICredentials(ctx, uri)
	if err != nil {
		t.Fatalf("the Pod Identity agent's credentials: %v", err)
	}
	t.Logf("credentials expire %s", creds.Expires.Format(time.RFC3339))

	suffix, err := PartitionSuffix("aws")
	if err != nil {
		t.Fatal(err)
	}
	u, err := ServiceURL("sts", region, suffix)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/"
	u.RawQuery = "Action=GetCallerIdentity&Version=2011-06-15"
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
	if res.StatusCode != http.StatusOK {
		code := "-"
		if m := awsErrorCode.FindSubmatch(body); m != nil {
			code = string(m[1])
		}
		t.Fatalf("STS answered %d %s", res.StatusCode, code)
	}
	m := assumedRoleARN.FindSubmatch(body)
	if m == nil {
		t.Fatalf("STS answered 200 with no Arn")
	}
	arn := string(m[1])
	t.Logf("caller %s", arn)
	if role := os.Getenv("HALITE_POD_IDENTITY_ROLE"); role != "" &&
		!strings.Contains(arn, ":assumed-role/"+role+"/") {
		t.Errorf("the caller is %s, not the association's role %s", arn, role)
	}
}
