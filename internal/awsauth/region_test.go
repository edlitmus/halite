package awsauth

import (
	"strings"
	"testing"
)

// The grammar admits the regions of the three partitions and nothing
// that can change which host a URL names (DIVERGENCE 5.324).
func TestValidRegion(t *testing.T) {
	for _, r := range []string{
		"us-east-1", "eu-central-2", "us-gov-west-1", "cn-north-1", "cn-northwest-1",
		"ap-southeast-4", "il-central-1", "us-isob-east-1", "me-central-1", "ca-west-1",
	} {
		if err := ValidRegion(r); err != nil {
			t.Errorf("%s was refused: %v", r, err)
		}
	}
	for _, r := range []string{
		"", "x@evil.example/", "evil.example:8443/", "10.0.0.5:8443/p?", "us-east-1.evil.example",
		"us-east-1#", "us-east-1/", "us-east-1\n", "US-EAST-1", "us-east", "useast1", "u-east-1",
		"us-east-123", "us--east-1", "-us-east-1", "us-east-1-",
		"us-east-" + strings.Repeat("a", 40) + "-1",
	} {
		if err := ValidRegion(r); err == nil {
			t.Errorf("%q was accepted", r)
		}
	}
}

// The error quotes a bounded prefix, not everything it was given.
func TestValidRegionBoundsItsError(t *testing.T) {
	hostile := strings.Repeat("a", 10000) + "@evil.example/"
	err := ValidRegion(hostile)
	if err == nil {
		t.Fatal("it was accepted")
	}
	if len(err.Error()) > 300 {
		t.Errorf("the error is %d bytes", len(err.Error()))
	}
}

// ServiceURL's host is exactly service.region.suffix.
func TestServiceURLHost(t *testing.T) {
	u, err := ServiceURL("secretsmanager", "us-gov-west-1", "amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "secretsmanager.us-gov-west-1.amazonaws.com" || u.String() !=
		"https://secretsmanager.us-gov-west-1.amazonaws.com/" {
		t.Errorf("it built %s", u)
	}
	if _, err := ServiceURL("secretsmanager", "x@evil.example/", "amazonaws.com"); err == nil {
		t.Error("a hostile region built a URL")
	}
}

// PartitionSuffix answers the three partitions and refuses the rest.
func TestPartitionSuffix(t *testing.T) {
	want := map[string]string{"": "amazonaws.com", "aws": "amazonaws.com",
		"aws-us-gov": "amazonaws.com", "aws-cn": "amazonaws.com.cn"}
	for p, w := range want {
		if got, err := PartitionSuffix(p); err != nil || got != w {
			t.Errorf("%q gave %q (%v)", p, got, err)
		}
	}
	for _, p := range []string{"evil", "aws-iso", "aws.evil.example"} {
		if _, err := PartitionSuffix(p); err == nil {
			t.Errorf("%q was accepted", p)
		}
	}
}

// The STS endpoint refuses a region that is not one, rather than
// building a host out of it.
func TestTheSTSEndpointRefusesAHostileRegion(t *testing.T) {
	p := &Provider{Region: "x@evil.example/"}
	if got, err := p.stsEndpoint(); err == nil {
		t.Errorf("it built %s", got)
	}
}
