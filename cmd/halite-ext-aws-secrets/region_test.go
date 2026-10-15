package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/awsauth"
)

// DIVERGENCE 5.290. The region became part of the endpoint's host by
// concatenation, and could come from the node, so a node could send the
// hub's signed request -- and its access key ID and session token -- to
// a host of its choosing. These are the values that were shown to do
// it, as they would be parsed out of the old URL:
//
//	x@evil.example/      -> host evil.example
//	evil.example:8443/   -> host secretsmanager.evil.example:8443
//	10.0.0.5:8443/p?     -> host secretsmanager.10.0.0.5:8443
//
// plus the others a region must not be able to do: add a label, end the
// authority with a fragment, or be something other than lowercase.
var hostileRegions = []string{
	"x@evil.example/",
	"evil.example:8443/",
	"10.0.0.5:8443/p?",
	"us-east-1.evil.example",
	"evil.example#",
	"us-east-1/",
	"us-east-1\n",
	"US-EAST-1",
	"us-east-1@evil.example",
	"us-east-" + strings.Repeat("1", 40),
	strings.Repeat("a", 5000) + "@evil.example/",
}

// name is a subtest name for a hostile value, short enough to read.
func name(region string) string {
	if len(region) > 40 {
		return fmt.Sprintf("%s...(%d bytes)", region[:40], len(region))
	}
	return region
}

// recorder is an http.RoundTripper that never touches the network. It
// records the host of every request and answers it locally, or, when
// refuse is set, fails the test for having been asked at all.
type recorder struct {
	t      *testing.T
	refuse bool
	mu     sync.Mutex
	hosts  []string
	auth   []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hosts = append(r.hosts, req.URL.Host)
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.mu.Unlock()
	if r.refuse {
		r.t.Errorf("a request was built and sent to %q, with Authorization %q and a session token %q",
			req.URL.Host, req.Header.Get("Authorization"), req.Header.Get("X-Amz-Security-Token"))
	}
	body := `{"Name":"x","SecretString":"from-` + req.URL.Host + `"}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (r *recorder) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.hosts...)
}

// callNoEndpoint drives the handler with no `endpoint` in the block --
// the configuration in which the region is part of the host -- and with
// every request answered by rec rather than by anything on a network.
func callNoEndpoint(t *testing.T, rec *recorder, block, grains string) (map[string]any, error) {
	t.Helper()
	cache = newSecretCache(time.Now)
	handlerClient = &http.Client{Transport: rec}
	t.Cleanup(func() { handlerClient = nil })
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "session-token")

	if grains == "" {
		grains = "{}"
	}
	kwargs, err := json.Marshal(request{
		NodeID: "web1.prod",
		Env:    "base",
		Grains: json.RawMessage(grains),
		Pillar: json.RawMessage(`{}`),
		Config: json.RawMessage(block),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handle(ext.Call{Function: "ext_pillar", Kwargs: kwargs, Log: func(string, string) {}})
	if err != nil || out == nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

// grainsJSON encodes a grains mapping, so a hostile value with a
// newline or a quote in it arrives exactly as a node would send it.
func grainsJSON(t *testing.T, g map[string]any) string {
	t.Helper()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkBounded fails when an error carries more than a bounded prefix
// of the hostile value.
func checkBounded(t *testing.T, err error, region string) {
	t.Helper()
	if len(region) > 200 && strings.Contains(err.Error(), region[:200]) {
		t.Errorf("the error echoes %d bytes of the node's value", len(region))
	}
}

// The audit's reproduction: a node's `region` grain is refused before
// any request is built, so nothing is signed and nothing is sent.
func TestAHostileRegionGrainIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for _, region := range hostileRegions {
		t.Run(name(region), func(t *testing.T) {
			rec := &recorder{t: t, refuse: true}
			_, err := callNoEndpoint(t, rec,
				`{"region":"us-east-1","secrets":[{"name":"k","secret_id":"vmop/a"}]}`,
				grainsJSON(t, map[string]any{"region": region}))
			if err == nil {
				t.Fatalf("the grain %q was used as a region", region)
			}
			checkBounded(t, err, region)
		})
	}
}

// The wider half: with `node_grain` on, a node names the secret and
// its region, and the allow list only ever looked at the secret's ID.
func TestAHostileRegionInANodeNamedSecretIsRefused(t *testing.T) {
	for _, region := range hostileRegions {
		t.Run(name(region), func(t *testing.T) {
			rec := &recorder{t: t, refuse: true}
			grains := grainsJSON(t, map[string]any{"wants": []any{
				map[string]any{"name": "k", "secret_id": "vmop/a", "region": region},
			}})
			_, err := callNoEndpoint(t, rec,
				`{"region":"us-east-1","node_grain":"wants","node_grain_allow":["vmop/*"]}`, grains)
			if err == nil {
				t.Fatalf("the node-named region %q was used", region)
			}
			checkBounded(t, err, region)
		})
	}
}

// And the same through an ARN a node names, whose fourth field is the
// region. A colon would move the fields, so these are the hostile
// values without one.
func TestAHostileRegionInANodeNamedARNIsRefused(t *testing.T) {
	for _, region := range []string{"x@evil.example/", "evil.example#", "us-east-1.evil.example"} {
		t.Run(name(region), func(t *testing.T) {
			rec := &recorder{t: t, refuse: true}
			arn := "arn:aws:secretsmanager:" + region + ":123456789012:secret:vmop/a"
			if got := regionFromARN(arn); got != region {
				t.Fatalf("the ARN's region read as %q; the test is not testing what it says", got)
			}
			grains := grainsJSON(t, map[string]any{"wants": []any{
				map[string]any{"name": "k", "secret_id": arn},
			}})
			if _, err := callNoEndpoint(t, rec, `{"region":"us-east-1","node_grain":"wants"}`, grains); err == nil {
				t.Fatalf("the ARN region %q was used", region)
			}
		})
	}
}

// A partition the extension cannot reach, named by a node's ARN, is
// refused rather than defaulted to the commercial suffix.
func TestAnUnknownPartitionInAnARNIsRefused(t *testing.T) {
	rec := &recorder{t: t, refuse: true}
	grains := grainsJSON(t, map[string]any{"wants": []any{
		map[string]any{"name": "k", "secret_id": "arn:evil:secretsmanager:us-east-1:1:secret:vmop/a"},
	}})
	if _, err := callNoEndpoint(t, rec, `{"region":"us-east-1","node_grain":"wants"}`, grains); err == nil {
		t.Fatal("an unknown partition was reached")
	}
}

// A well-formed region a node supplies is used only when the block
// allows it: the block's own region, or one in `node_region_allow`.
func TestANodeSuppliedRegionMustBeAllowed(t *testing.T) {
	t.Run("a grain the block does not allow", func(t *testing.T) {
		rec := &recorder{t: t, refuse: true}
		_, err := callNoEndpoint(t, rec,
			`{"region":"us-east-1","secrets":[{"name":"k","secret_id":"vmop/a"}]}`,
			`{"region":"eu-west-1"}`)
		if err == nil || !strings.Contains(err.Error(), "node_region_allow") {
			t.Fatalf("an unlisted grain region gave %v", err)
		}
	})
	t.Run("a node-named secret's region the block does not allow", func(t *testing.T) {
		rec := &recorder{t: t, refuse: true}
		grains := `{"wants":[{"name":"k","secret_id":"vmop/a","region":"eu-west-1"}]}`
		_, err := callNoEndpoint(t, rec, `{"region":"us-east-1","node_grain":"wants"}`, grains)
		if err == nil || !strings.Contains(err.Error(), "node_region_allow") {
			t.Fatalf("an unlisted node-named region gave %v", err)
		}
	})

	allowed := map[string]struct{ block, grains string }{
		"a grain that is the block's own region": {
			`{"region":"us-east-1","secrets":[{"name":"k","secret_id":"vmop/a"}]}`,
			`{"region":"us-east-1"}`},
		"a grain in node_region_allow": {
			`{"region":"us-east-1","node_region_allow":["eu-west-1"],"secrets":[{"name":"k","secret_id":"vmop/a"}]}`,
			`{"region":"eu-west-1"}`},
		"a node-named secret's region in node_region_allow": {
			`{"region":"us-east-1","node_region_allow":["eu-west-1"],"node_grain":"wants"}`,
			`{"wants":[{"name":"k","secret_id":"vmop/a","region":"eu-west-1"}]}`},
	}
	want := map[string]string{
		"a grain that is the block's own region":            "secretsmanager.us-east-1.amazonaws.com",
		"a grain in node_region_allow":                      "secretsmanager.eu-west-1.amazonaws.com",
		"a node-named secret's region in node_region_allow": "secretsmanager.eu-west-1.amazonaws.com",
	}
	for name, c := range allowed {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{t: t}
			if _, err := callNoEndpoint(t, rec, c.block, c.grains); err != nil {
				t.Fatal(err)
			}
			if got := rec.sent(); len(got) != 1 || got[0] != want[name] {
				t.Errorf("it sent to %v, want exactly %s", got, want[name])
			}
		})
	}

	// The hub's own writing is held to the grammar and nothing else: an
	// operator who names a region in the block has chosen it.
	t.Run("the hub's own region needs no allowance", func(t *testing.T) {
		rec := &recorder{t: t}
		_, err := callNoEndpoint(t, rec,
			`{"region":"us-east-1","secrets":[{"name":"k","secret_id":"vmop/a","region":"ap-southeast-4"}]}`, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := rec.sent(); len(got) != 1 || got[0] != "secretsmanager.ap-southeast-4.amazonaws.com" {
			t.Errorf("it sent to %v", got)
		}
	})
}

// Valid regions build exactly the host meant, in each partition, and
// the request that is actually sent goes there.
func TestAValidRegionReachesExactlyItsEndpoint(t *testing.T) {
	cases := []struct{ partition, region, host string }{
		{"", "us-east-1", "secretsmanager.us-east-1.amazonaws.com"},
		{"aws", "eu-central-2", "secretsmanager.eu-central-2.amazonaws.com"},
		{"aws", "ap-southeast-4", "secretsmanager.ap-southeast-4.amazonaws.com"},
		{"aws", "il-central-1", "secretsmanager.il-central-1.amazonaws.com"},
		{"aws-us-gov", "us-gov-west-1", "secretsmanager.us-gov-west-1.amazonaws.com"},
		{"aws-us-gov", "us-gov-east-1", "secretsmanager.us-gov-east-1.amazonaws.com"},
		{"aws-cn", "cn-north-1", "secretsmanager.cn-north-1.amazonaws.com.cn"},
		{"aws-cn", "cn-northwest-1", "secretsmanager.cn-northwest-1.amazonaws.com.cn"},
	}
	for _, c := range cases {
		t.Run(c.region, func(t *testing.T) {
			rec := &recorder{t: t}
			client := &secretsClient{
				Provider:  &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}},
				Client:    &http.Client{Transport: rec},
				Partition: c.partition,
			}
			got, err := client.GetSecretValue(t.Context(), "vmop/a", c.region, "")
			if err != nil {
				t.Fatal(err)
			}
			if sent := rec.sent(); len(sent) != 1 || sent[0] != c.host {
				t.Errorf("it sent to %v, want exactly %s", sent, c.host)
			}
			if got != "from-"+c.host {
				t.Errorf("the answer was %q", got)
			}
		})
	}
}

// The client itself refuses a hostile region, whoever handed it one, so
// a future caller that skips regionFor is still covered.
func TestTheClientRefusesAHostileRegion(t *testing.T) {
	for _, region := range hostileRegions {
		t.Run(name(region), func(t *testing.T) {
			rec := &recorder{t: t, refuse: true}
			client := &secretsClient{
				Provider: &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}},
				Client:   &http.Client{Transport: rec},
			}
			_, err := client.GetSecretValue(t.Context(), "vmop/a", region, "")
			if err == nil {
				t.Fatalf("the region %q reached a request", region)
			}
			checkBounded(t, err, region)
		})
	}
}

// The block's own values are checked when it is read, so a typo is
// reported against the block.
func TestTheBlockRefusesABadRegionOrPartition(t *testing.T) {
	for _, block := range []string{
		`{"region":"x@evil.example/"}`,
		`{"node_region_allow":["evil.example:8443/"]}`,
		`{"partition":"evil"}`,
	} {
		if _, err := parseConfig(json.RawMessage(block)); err == nil {
			t.Errorf("%s was accepted", block)
		}
	}
	if _, err := parseConfig(json.RawMessage(
		`{"region":"us-gov-west-1","partition":"aws-us-gov","node_region_allow":["us-gov-east-1"]}`)); err != nil {
		t.Errorf("a sound block was refused: %v", err)
	}
}
