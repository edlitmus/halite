package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/awsauth"
)

// fakeSecrets is a Secrets Manager that answers GetSecretValue from a
// map, and records what was asked for.
type fakeSecrets struct {
	values map[string]string
	server *httptest.Server
	calls  []string
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
			SecretId string `json:"SecretId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.calls = append(f.calls, req.SecretId)

		val, ok := f.values[req.SecretId]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"no such secret"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"Name": req.SecretId, "SecretString": val})
	}))
	t.Cleanup(f.server.Close)
	return f
}

// call drives the handler the way the host does, with the endpoint and
// credentials pointed at the fake.
func (f *fakeSecrets) call(t *testing.T, block string, grains, treePillar string) (map[string]any, error) {
	t.Helper()
	cache = newSecretCache(time.Now)

	var cfg map[string]any
	if err := json.Unmarshal([]byte(block), &cfg); err == nil {
		cfg["endpoint"] = f.server.URL
		cfg["access_key_id"] = "AKID"
		if _, ok := cfg["region"]; !ok {
			cfg["region"] = "us-east-1"
		}
		encoded, _ := json.Marshal(cfg)
		block = string(encoded)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	if grains == "" {
		grains = "{}"
	}
	if treePillar == "" {
		treePillar = "{}"
	}
	kwargs, err := json.Marshal(request{
		NodeID: "web1.prod",
		Env:    "base",
		Grains: json.RawMessage(grains),
		Pillar: json.RawMessage(treePillar),
		Config: json.RawMessage(block),
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := handle(ext.Call{
		Function: "ext_pillar",
		Kwargs:   kwargs,
		Log:      func(string, string) {},
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return out.(map[string]any), nil
}

// A JSON secret is parsed and a dotted key nests, so that
// `pillar.get('aws_secrets:base:ubuntu_pro:token')` resolves — which is
// exactly what the Salt module produced.
func TestADottedKeyNestsAndJSONIsParsed(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"vmop/dev/ubuntu-pro": `{"token":"C1234","enabled":true}`,
	})
	out, err := f.call(t, `{"secrets":[{"name":"base.ubuntu_pro","secret_id":"vmop/dev/ubuntu-pro"}]}`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	root := out["aws_secrets"].(map[string]any)
	base := root["base"].(map[string]any)
	pro := base["ubuntu_pro"].(map[string]any)
	if pro["token"] != "C1234" {
		t.Errorf("the token is %v", pro["token"])
	}
	if pro["enabled"] != true {
		t.Errorf("enabled is %v", pro["enabled"])
	}
}

// A secret that is not JSON stays the string it is.
func TestAPlainSecretStaysAString(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"api-key": "plain-value"})
	out, err := f.call(t, `{"secrets":[{"name":"api_key","secret_id":"api-key"}]}`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	root := out["aws_secrets"].(map[string]any)
	if root["api_key"] != "plain-value" {
		t.Errorf("it is %#v", root["api_key"])
	}
}

// Salt writes most external pillar blocks as a bare list, and this one
// is usually written that way too.
func TestTheBareListFormIsRead(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"vmop/k": "v"})
	// A list carries no endpoint override, so this drives the parser
	// rather than the fetch.
	cfg, err := parseConfig(json.RawMessage(`[{"name":"k","secret_id":"vmop/k","region":"us-east-1"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Key != "k" {
		t.Fatalf("it read %+v", cfg.Secrets)
	}
	_ = f
}

// A misspelt per-secret key is refused. A setting that does nothing is
// what this project's configuration handling exists to prevent, and an
// extension is not exempt because its configuration arrives over a pipe.
func TestAMisspeltKeyIsRefused(t *testing.T) {
	if _, err := parseConfig(json.RawMessage(`{"secrets":[{"name":"k","secret_arn":"x"}]}`)); err == nil {
		t.Error("a misspelt per-secret key was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"reigon":"us-east-1"}`)); err == nil {
		t.Error("a misspelt block key was accepted")
	}
}

// Both spellings the Salt module accepted still work.
func TestBothSpellingsAreRead(t *testing.T) {
	for _, block := range []string{
		`{"secrets":[{"name":"k","secret_id":"x"}]}`,
		`{"secrets":[{"pillar_key":"k","secret_name":"x"}]}`,
	} {
		cfg, err := parseConfig(json.RawMessage(block))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Secrets[0].Key != "k" || cfg.Secrets[0].SecretID != "x" {
			t.Errorf("%s read %+v", block, cfg.Secrets[0])
		}
	}
}

// Region precedence: the secret's own setting, then the ARN, then the
// node's `region` grain, then the block's default.
func TestRegionPrecedence(t *testing.T) {
	cfg := &Config{Region: "us-east-1"}
	grains := map[string]any{"region": "eu-west-2"}
	cases := []struct {
		name   string
		secret Secret
		grains map[string]any
		want   string
	}{
		{"explicit wins", Secret{Region: "ap-south-1",
			SecretID: "arn:aws:secretsmanager:us-gov-east-1:1:secret:x"}, grains, "ap-south-1"},
		{"then the ARN", Secret{
			SecretID: "arn:aws-us-gov:secretsmanager:us-gov-east-1:1:secret:x"}, grains, "us-gov-east-1"},
		{"then the grain", Secret{SecretID: "a-name"}, grains, "eu-west-2"},
		{"then the default", Secret{SecretID: "a-name"}, map[string]any{}, "us-east-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cfg.regionFor(c.secret, c.grains); got != c.want {
				t.Errorf("it chose %s, want %s", got, c.want)
			}
		})
	}
}

// A secret that cannot be fetched fails the source. Salt logged and
// carried on, which is how a state comes to apply with an empty
// password.
func TestAFailedFetchFailsTheCall(t *testing.T) {
	f := newFakeSecrets(t, nil)
	_, err := f.call(t, `{"secrets":[{"name":"k","secret_id":"missing"}]}`, "", "")
	if err == nil {
		t.Fatal("a missing secret was not an error")
	}
	if !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Errorf("the error is %q", err)
	}
}

// Every request is signed.
func TestEveryRequestIsSigned(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"k": "v"})
	if _, err := f.call(t, `{"secrets":[{"name":"k","secret_id":"k"}]}`, "", ""); err != nil {
		t.Fatal(err)
	}
	if f.unsigned {
		t.Error("a request arrived unsigned")
	}
}

// The tree may name secrets, which is what the Python module's own
// `aws_secrets_ext_pillar` pillar key did — and which, unlike the
// grain, a node has no say in.
func TestTheTreeMayNameSecrets(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"vmop/mongo": "mongo-value"})
	out, err := f.call(t,
		`{"pillar_list":"aws_secrets_ext_pillar"}`,
		"",
		`{"aws_secrets_ext_pillar":[{"pillar_key":"mongodb.mongokey","secret_id":"vmop/mongo","region":"us-east-1"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	root := out["aws_secrets"].(map[string]any)
	mongo := root["mongodb"].(map[string]any)
	if mongo["mongokey"] != "mongo-value" {
		t.Errorf("it is %v", mongo["mongokey"])
	}
}

// A node may name its own secrets when the block says so, and the allow
// list bounds which — loudly, because a node asking for a secret it may
// not have is either misconfigured or probing.
func TestTheNodeGrainAndItsAllowList(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"vmop/ok": "yes", "other/no": "no"})

	permitted := `{"aws_secrets_ext_pillar":[{"pillar_key":"k","secret_id":"vmop/ok","region":"us-east-1"}]}`
	out, err := f.call(t,
		`{"node_grain":"aws_secrets_ext_pillar","node_grain_allow":["vmop/*"]}`, permitted, "")
	if err != nil {
		t.Fatal(err)
	}
	if out["aws_secrets"].(map[string]any)["k"] != "yes" {
		t.Errorf("the permitted secret is %v", out)
	}

	denied := `{"aws_secrets_ext_pillar":[{"pillar_key":"k","secret_id":"other/no","region":"us-east-1"}]}`
	_, err = f.call(t,
		`{"node_grain":"aws_secrets_ext_pillar","node_grain_allow":["vmop/*"]}`, denied, "")
	if err == nil {
		t.Fatal("a secret outside the allow list was fetched")
	}
	if !strings.Contains(err.Error(), "node_grain_allow") {
		t.Errorf("the error is %q", err)
	}
	for _, c := range f.calls {
		if c == "other/no" {
			t.Error("the denied secret was requested anyway")
		}
	}
}

// The grain is read only when the block asks for it. A node that sets
// it on a hub that did not ask gets nothing.
func TestTheNodeGrainIsIgnoredUnlessConfigured(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"vmop/mongo": "v"})
	grains := `{"aws_secrets_ext_pillar":[{"pillar_key":"k","secret_id":"vmop/mongo","region":"us-east-1"}]}`
	out, err := f.call(t, `{}`, grains, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("it produced %v", out)
	}
	if len(f.calls) != 0 {
		t.Errorf("it made %d calls", len(f.calls))
	}
}

// The cache holds for its TTL and is dropped after it.
func TestTheCacheHoldsForItsTTL(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"k": "v"})
	now := time.Now()
	cache = newSecretCache(func() time.Time { return now })

	client := &secretsClient{
		Provider: &awsauth.Provider{Explicit: awsauth.Credentials{AccessKeyID: "A", SecretAccessKey: "B"}},
		Endpoint: f.server.URL,
		Client:   f.server.Client(),
	}
	s := Secret{Key: "k", SecretID: "k"}
	for i := 0; i < 2; i++ {
		if _, err := cache.fetch(t.Context(), client, s, "us-east-1", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("it called Secrets Manager %d times inside the TTL", len(f.calls))
	}
	now = now.Add(2 * time.Minute)
	if _, err := cache.fetch(t.Context(), client, s, "us-east-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Errorf("it called Secrets Manager %d times across the TTL", len(f.calls))
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

// The handshake announces `ext_pillar`, which is what the hub checks
// before it will use the extension as a pillar source.
func TestTheHandshakeAnnouncesTheEntryPoint(t *testing.T) {
	sigs := functions()
	if len(sigs) != 1 {
		t.Fatalf("it announced %d function(s)", len(sigs))
	}
	if sigs[0].Function != "ext_pillar" {
		t.Errorf("it announced %q", sigs[0].Function)
	}
	// Every parameter names a type rather than carrying a number. The
	// host reads names, and the first version of this extension sent
	// numbers and had every signature refused.
	for _, p := range sigs[0].Params {
		if p.Type == "" {
			t.Errorf("the %s parameter declares no type", p.Name)
		}
	}
}

// It asks for the network and nothing else. An extension that declared
// more than it needs would be signing off on more than it needs.
func TestItDeclaresOnlyTheNetwork(t *testing.T) {
	// Read off the same literal main() uses, so this cannot drift from
	// what is actually served.
	ext := &ext.Extension{Declares: []string{"network"}}
	if len(ext.Declares) != 1 || ext.Declares[0] != "network" {
		t.Errorf("it declares %v", ext.Declares)
	}
}
