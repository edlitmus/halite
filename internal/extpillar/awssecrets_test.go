package extpillar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
)

// fakeSecrets is a Secrets Manager that answers GetSecretValue from a
// map, and records what was asked for.
type fakeSecrets struct {
	values map[string]string
	server *httptest.Server
	calls  []string
	// status, when set, is answered instead of a value.
	status int
	// unsigned records whether a request arrived without a signature.
	unsigned bool
}

func newFakeSecrets(t *testing.T, values map[string]string) *fakeSecrets {
	t.Helper()
	f := &fakeSecrets{values: values}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), awsauth.Algorithm+" ") {
			f.unsigned = true
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req struct {
			SecretId     string `json:"SecretId"`
			VersionStage string `json:"VersionStage"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.calls = append(f.calls, req.SecretId)

		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"Secrets Manager can't find the specified secret."}`))
			return
		}
		val, ok := f.values[req.SecretId]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"no such secret"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Name": req.SecretId, "SecretString": val,
		})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeSecrets) source(t *testing.T, opts AWSSecretsOptions) *AWSSecrets {
	t.Helper()
	opts.Endpoint = f.server.URL
	opts.Client = f.server.Client()
	if opts.Provider == nil {
		opts.Provider = &awsauth.Provider{
			Explicit: awsauth.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"},
			Environ:  func(string) string { return "" },
		}
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	src, err := NewAWSSecrets(opts)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func mustPillar(t *testing.T, src *AWSSecrets, req pillar.ExtRequest) *value.Map {
	t.Helper()
	out, err := src.Pillar(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A JSON secret is parsed, and a dotted key nests, so that
// `pillar.get('aws_secrets:base:ubuntu_pro:token')` resolves — which is
// exactly what the Salt module produced.
func TestADottedKeyNestsAndJSONIsParsed(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"vmop/dev/ubuntu-pro": `{"token":"C1234","enabled":true}`,
	})
	src := f.source(t, AWSSecretsOptions{Secrets: []Secret{
		{Key: "base.ubuntu_pro", SecretID: "vmop/dev/ubuntu-pro"},
	}})

	out := mustPillar(t, src, pillar.ExtRequest{NodeID: "n1"})
	got, ok := value.Traverse(out, "aws_secrets:base:ubuntu_pro:token", ":")
	if !ok {
		t.Fatalf("the token is absent; got %v", out)
	}
	if got != "C1234" {
		t.Errorf("the token is %v", got)
	}
	if enabled, _ := value.Traverse(out, "aws_secrets:base:ubuntu_pro:enabled", ":"); enabled != true {
		t.Errorf("enabled is %v", enabled)
	}
}

// A secret that is not JSON stays the string it is.
func TestAPlainSecretStaysAString(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"api-key": "plain-value"})
	src := f.source(t, AWSSecretsOptions{Secrets: []Secret{
		{Key: "api_key", SecretID: "api-key"},
	}})
	out := mustPillar(t, src, pillar.ExtRequest{NodeID: "n1"})
	if got, _ := value.Traverse(out, "aws_secrets:api_key", ":"); got != "plain-value" {
		t.Errorf("it is %#v", got)
	}
}

// Region precedence: the secret's own setting, then the ARN, then the
// node's `region` grain, then the configured default.
func TestRegionPrecedence(t *testing.T) {
	f := newFakeSecrets(t, nil)
	src := f.source(t, AWSSecretsOptions{Region: "us-east-1"})
	grains := value.MapOf("region", "eu-west-2")

	cases := []struct {
		name   string
		secret Secret
		grains *value.Map
		want   string
	}{
		{"explicit wins", Secret{Region: "ap-south-1",
			SecretID: "arn:aws:secretsmanager:us-gov-east-1:1:secret:x"}, grains, "ap-south-1"},
		{"then the ARN", Secret{
			SecretID: "arn:aws-us-gov:secretsmanager:us-gov-east-1:1:secret:x"}, grains, "us-gov-east-1"},
		{"then the grain", Secret{SecretID: "a-name"}, grains, "eu-west-2"},
		{"then the default", Secret{SecretID: "a-name"}, value.NewMap(0), "us-east-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := src.regionFor(c.secret, pillar.ExtRequest{Grains: c.grains})
			if got != c.want {
				t.Errorf("it chose %s, want %s", got, c.want)
			}
		})
	}
}

// A secret with no region anywhere is an error rather than a guess at
// us-east-1, which would fail later and less legibly.
func TestASecretWithNoRegionIsAnError(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"a-name": "v"})
	src := f.source(t, AWSSecretsOptions{
		Region:  " ",
		Secrets: []Secret{{Key: "k", SecretID: "a-name"}},
	})
	src.opts.Region = ""
	_, err := src.Pillar(context.Background(), pillar.ExtRequest{Grains: value.NewMap(0)})
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "neither a region nor an ARN") {
		t.Errorf("the error is %q", err)
	}
}

// A secret that cannot be fetched fails the source. Salt logged and
// carried on, which is how a state comes to apply with an empty
// password.
func TestAFailedFetchFailsTheSource(t *testing.T) {
	f := newFakeSecrets(t, nil)
	f.status = http.StatusBadRequest
	src := f.source(t, AWSSecretsOptions{Secrets: []Secret{
		{Key: "k", SecretID: "missing"},
	}})
	_, err := src.Pillar(context.Background(), pillar.ExtRequest{NodeID: "n1"})
	if err == nil {
		t.Fatal("a missing secret was not an error")
	}
	// The service's own words, which say which secret and why.
	if !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Errorf("the error is %q", err)
	}
}

// Every request is signed. An unsigned one would work against a service
// that does not check, and fail in production.
func TestEveryRequestIsSigned(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"k": "v"})
	src := f.source(t, AWSSecretsOptions{Secrets: []Secret{{Key: "k", SecretID: "k"}}})
	mustPillar(t, src, pillar.ExtRequest{NodeID: "n1"})
	if f.unsigned {
		t.Error("a request arrived unsigned")
	}
}

// The cache holds for its TTL and is dropped after it.
func TestTheCacheHoldsForItsTTL(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"k": "v"})
	now := time.Now()
	src := f.source(t, AWSSecretsOptions{
		Secrets:  []Secret{{Key: "k", SecretID: "k"}},
		CacheTTL: time.Minute,
		Now:      func() time.Time { return now },
	})
	req := pillar.ExtRequest{NodeID: "n1"}
	mustPillar(t, src, req)
	mustPillar(t, src, req)
	if len(f.calls) != 1 {
		t.Errorf("it called Secrets Manager %d times inside the TTL", len(f.calls))
	}
	now = now.Add(2 * time.Minute)
	mustPillar(t, src, req)
	if len(f.calls) != 2 {
		t.Errorf("it called Secrets Manager %d times across the TTL", len(f.calls))
	}
}

// A negative TTL disables the cache, for an estate that rotates often
// enough to want every compilation to be current.
func TestANegativeTTLDisablesTheCache(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"k": "v"})
	src := f.source(t, AWSSecretsOptions{
		Secrets:  []Secret{{Key: "k", SecretID: "k"}},
		CacheTTL: -1,
	})
	req := pillar.ExtRequest{NodeID: "n1"}
	mustPillar(t, src, req)
	mustPillar(t, src, req)
	if len(f.calls) != 2 {
		t.Errorf("it called Secrets Manager %d times", len(f.calls))
	}
}

// The node's own grain adds secrets, which is Salt parity for the
// `aws_secrets_ext_pillar` list this estate's hub configuration
// renders from.
func TestANodeMayNameItsOwnSecretsThroughAGrain(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"vmop/shared":    "shared-value",
		"vmop/mongo/key": "mongo-value",
	})
	src := f.source(t, AWSSecretsOptions{
		Secrets:   []Secret{{Key: "shared", SecretID: "vmop/shared"}},
		NodeGrain: "aws_secrets_ext_pillar",
	})
	grains := value.MapOf("aws_secrets_ext_pillar", []any{
		value.MapOf("pillar_key", "mongodb.mongokey", "secret_id", "vmop/mongo/key", "region", "us-east-1"),
	})

	out := mustPillar(t, src, pillar.ExtRequest{NodeID: "n1", Grains: grains})
	if got, _ := value.Traverse(out, "aws_secrets:mongodb:mongokey", ":"); got != "mongo-value" {
		t.Errorf("the node's own secret is %v", got)
	}
	if got, _ := value.Traverse(out, "aws_secrets:shared", ":"); got != "shared-value" {
		t.Errorf("the configured secret is %v", got)
	}
}

// The grain is read only when it is configured. A node that sets it on a
// hub that did not ask gets nothing, rather than the hub fetching on its
// say-so.
func TestTheNodeGrainIsIgnoredUnlessConfigured(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"vmop/mongo/key": "mongo-value"})
	src := f.source(t, AWSSecretsOptions{
		Secrets: []Secret{},
	})
	grains := value.MapOf("aws_secrets_ext_pillar", []any{
		value.MapOf("pillar_key", "mongodb.mongokey", "secret_id", "vmop/mongo/key"),
	})
	out := mustPillar(t, src, pillar.ExtRequest{NodeID: "n1", Grains: grains})
	if out != nil && out.Len() != 0 {
		t.Errorf("it fetched %v", out)
	}
	if len(f.calls) != 0 {
		t.Errorf("it made %d calls", len(f.calls))
	}
}

// The allow list bounds what a node may name, and a secret outside it is
// refused loudly rather than skipped.
func TestTheAllowListBoundsWhatANodeMayName(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"vmop/dev/allowed": "yes",
		"other/denied":     "no",
	})
	src := f.source(t, AWSSecretsOptions{
		NodeGrain:      "aws_secrets_ext_pillar",
		NodeGrainAllow: []string{"vmop/*/*"},
	})

	allowed := value.MapOf("aws_secrets_ext_pillar", []any{
		value.MapOf("pillar_key", "k", "secret_id", "vmop/dev/allowed", "region", "us-east-1"),
	})
	out := mustPillar(t, src, pillar.ExtRequest{NodeID: "n1", Grains: allowed})
	if got, _ := value.Traverse(out, "aws_secrets:k", ":"); got != "yes" {
		t.Errorf("the permitted secret is %v", got)
	}

	denied := value.MapOf("aws_secrets_ext_pillar", []any{
		value.MapOf("pillar_key", "k", "secret_id", "other/denied", "region", "us-east-1"),
	})
	_, err := src.Pillar(context.Background(), pillar.ExtRequest{NodeID: "n1", Grains: denied})
	if err == nil {
		t.Fatal("a secret outside the allow list was fetched")
	}
	if !strings.Contains(err.Error(), "no pattern in") {
		t.Errorf("the error is %q", err)
	}
	for _, call := range f.calls {
		if call == "other/denied" {
			t.Error("the denied secret was requested anyway")
		}
	}
}

// Every value reaches the redactor, including each leaf of a JSON
// secret, so that one cannot get into a log by being interpolated into a
// message.
func TestEverySecretIsOfferedToTheRedactor(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"creds": `{"username":"svc","password":"hunter2"}`,
	})
	var offered []string
	src := f.source(t, AWSSecretsOptions{
		Secrets:  []Secret{{Key: "creds", SecretID: "creds"}},
		OnSecret: func(v string) { offered = append(offered, v) },
	})
	mustPillar(t, src, pillar.ExtRequest{NodeID: "n1"})

	want := map[string]bool{"svc": false, "hunter2": false}
	for _, v := range offered {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	for v, seen := range want {
		if !seen {
			t.Errorf("%q was never offered to the redactor", v)
		}
	}
}

// A misspelt per-secret key is refused. A setting that does nothing is
// what this project's configuration handling exists to prevent.
func TestAMisspeltSecretKeyIsRefused(t *testing.T) {
	_, err := SecretFromMap(value.MapOf("pillar_key", "k", "secret_arn", "x"))
	if err == nil {
		t.Fatal("a misspelt key was accepted")
	}
	if !strings.Contains(err.Error(), "not a per-secret setting") {
		t.Errorf("the error is %q", err)
	}
}

// Both spellings the Salt module accepted still work.
func TestBothSpellingsOfTheKeyAndTheIDAreRead(t *testing.T) {
	for _, m := range []*value.Map{
		value.MapOf("name", "k", "secret_id", "x"),
		value.MapOf("pillar_key", "k", "secret_name", "x"),
	} {
		s, err := SecretFromMap(m)
		if err != nil {
			t.Fatal(err)
		}
		if s.Key != "k" || s.SecretID != "x" {
			t.Errorf("it read %+v", s)
		}
	}
}

// A configuration that cannot work is refused at construction, not at
// the first node's compilation.
func TestAnUnusableConfigurationIsRefusedAtConstruction(t *testing.T) {
	provider := &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "a", SecretAccessKey: "b"}}
	if _, err := NewAWSSecrets(AWSSecretsOptions{
		Provider: provider,
		Secrets:  []Secret{{Key: "k"}},
	}); err == nil {
		t.Error("a secret with no secret_id was accepted")
	}
	if _, err := NewAWSSecrets(AWSSecretsOptions{
		Provider:       provider,
		NodeGrainAllow: []string{"["},
	}); err == nil {
		t.Error("an unusable pattern was accepted")
	}
}

// The endpoint is built from the partition. One built for the
// commercial partition is wrong in GovCloud, and wrong at the worst
// time.
func TestTheEndpointComesFromThePartition(t *testing.T) {
	cases := map[string]string{
		"":           "https://secretsmanager.us-gov-east-1.amazonaws.com",
		"aws":        "https://secretsmanager.us-gov-east-1.amazonaws.com",
		"aws-us-gov": "https://secretsmanager.us-gov-east-1.amazonaws.com",
		"aws-cn":     "https://secretsmanager.us-gov-east-1.amazonaws.com.cn",
	}
	for partition, want := range cases {
		c := &secretsClient{Partition: partition}
		if got := c.host("us-gov-east-1"); got != want {
			t.Errorf("%q gave %s, want %s", partition, got, want)
		}
	}
}

// The tree may name secrets too, which is what the Python module's own
// `aws_secrets_ext_pillar` pillar key did -- and which, unlike the
// grain, a node has no say in.
func TestTheTreeMayNameSecretsThroughPillar(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"vmop/mongo/key": "mongo-value"})
	src := f.source(t, AWSSecretsOptions{PillarList: "aws_secrets_ext_pillar"})
	compiled := value.MapOf("aws_secrets_ext_pillar", []any{
		value.MapOf("pillar_key", "mongodb.mongokey", "secret_id", "vmop/mongo/key", "region", "us-east-1"),
	})

	out := mustPillar(t, src, pillar.ExtRequest{NodeID: "n1", Pillar: compiled})
	if got, _ := value.Traverse(out, "aws_secrets:mongodb:mongokey", ":"); got != "mongo-value" {
		t.Errorf("the tree's secret is %v", got)
	}
}

// The allow list bounds the grain and not the pillar. A list that came
// from the tree is the hub's own writing; one that came from a grain is
// the node's.
func TestTheAllowListBoundsTheGrainAndNotTheTree(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"other/from-tree": "yes"})
	src := f.source(t, AWSSecretsOptions{
		PillarList:     "aws_secrets_ext_pillar",
		NodeGrain:      "aws_secrets_ext_pillar",
		NodeGrainAllow: []string{"vmop/*"},
	})
	spec := []any{
		value.MapOf("pillar_key", "k", "secret_id", "other/from-tree", "region", "us-east-1"),
	}

	out := mustPillar(t, src, pillar.ExtRequest{
		NodeID: "n1",
		Pillar: value.MapOf("aws_secrets_ext_pillar", spec),
		Grains: value.NewMap(0),
	})
	if got, _ := value.Traverse(out, "aws_secrets:k", ":"); got != "yes" {
		t.Errorf("the tree's secret was bounded by the node allow list: %v", got)
	}

	// The same specification in a grain is refused.
	_, err := src.Pillar(context.Background(), pillar.ExtRequest{
		NodeID: "n1",
		Grains: value.MapOf("aws_secrets_ext_pillar", spec),
	})
	if err == nil {
		t.Error("the same secret was accepted from a grain")
	}
}

// A list that is not a list says so, rather than being skipped.
func TestAMisshapenNodeListIsRefused(t *testing.T) {
	f := newFakeSecrets(t, nil)
	src := f.source(t, AWSSecretsOptions{NodeGrain: "aws_secrets_ext_pillar"})
	_, err := src.Pillar(context.Background(), pillar.ExtRequest{
		Grains: value.MapOf("aws_secrets_ext_pillar", "vmop/one-secret"),
	})
	if err == nil {
		t.Fatal("a string was accepted where a list belongs")
	}
	if !strings.Contains(err.Error(), "not a list of secrets") {
		t.Errorf("the error is %q", err)
	}
}
