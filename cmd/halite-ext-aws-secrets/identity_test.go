package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/ext"
)

// keyedSecrets is a Secrets Manager that answers by who is asking: the
// access key ID in the signature decides which secrets it will read,
// as an IAM policy would. Every request is counted per key.
type keyedSecrets struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  map[string]int
}

func newKeyedSecrets(t *testing.T, readable map[string]map[string]string) *keyedSecrets {
	t.Helper()
	k := &keyedSecrets{calls: map[string]int{}}
	k.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		_, rest, ok := strings.Cut(auth, "Credential=")
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		akid, _, _ := strings.Cut(rest, "/")
		var req struct {
			SecretId string `json:"SecretId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		k.mu.Lock()
		k.calls[akid]++
		k.mu.Unlock()

		val, ok := readable[akid][req.SecretId]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"not authorized"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"Name": req.SecretId, "SecretString": val})
	}))
	t.Cleanup(k.server.Close)
	return k
}

func (k *keyedSecrets) count(akid string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls[akid]
}

// handleBlock drives the handler with one block exactly as written,
// against whatever cache the process holds -- which is the point: the
// host keeps one process, and so one cache, for every block.
func handleBlock(t *testing.T, block string) (map[string]any, error) {
	t.Helper()
	kwargs, err := json.Marshal(request{
		NodeID: "web1.prod", Env: "base",
		Grains: json.RawMessage(`{}`), Pillar: json.RawMessage(`{}`),
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

func secretKeyFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret-key")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Two blocks in one process share the cache, and a value one block
// fetched must not be handed to the other unless the other could have
// fetched it itself (issue 622). The key was region, secret and stage,
// so a block holding low-privilege credentials was served what the
// high-privilege block had read, and Secrets Manager never saw it ask.
func TestABlockIsNotServedWhatAnotherBlocksCredentialsFetched(t *testing.T) {
	cache = newSecretCache(time.Now)
	aws := newKeyedSecrets(t, map[string]map[string]string{
		"AKIDHIGH": {"prod/db": "hunter22"},
	})
	block := func(akid string) string {
		return `{"endpoint":"` + aws.server.URL + `","region":"us-east-1",` +
			`"access_key_id":"` + akid + `","secret_access_key_file":"` +
			secretKeyFile(t, "secret-"+akid) + `",` +
			`"secrets":[{"name":"db","secret_id":"prod/db"}]}`
	}

	out, err := handleBlock(t, block("AKIDHIGH"))
	if err != nil {
		t.Fatal(err)
	}
	if out["aws_secrets"].(map[string]any)["db"] != "hunter22" {
		t.Fatalf("the privileged block read %v", out)
	}

	out, err = handleBlock(t, block("AKIDLOW"))
	if err == nil {
		t.Errorf("the low-privilege block was served the privileged block's value: %v", out)
	} else if !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("the refusal is not the service's own: %v", err)
	}
	if n := aws.count("AKIDLOW"); n != 1 {
		t.Errorf("Secrets Manager saw the low-privilege key %d time(s), want 1", n)
	}
}

// The same access key ID with a different secret key is a different
// identity as far as the service is concerned -- one of them does not
// sign -- so it is a different cache entry too.
func TestTheSecretKeyIsPartOfTheIdentity(t *testing.T) {
	a := &secretsClient{}
	b := &secretsClient{}
	cfgA := &Config{AccessKeyID: "AKID", SecretAccessKeyFile: secretKeyFile(t, "one")}
	cfgB := &Config{AccessKeyID: "AKID", SecretAccessKeyFile: secretKeyFile(t, "two")}
	a.Provider, b.Provider = cfgA.provider(), cfgB.provider()
	if a.identity() == b.identity() {
		t.Error("two secret keys under one access key ID share an identity")
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.RoleARN = "arn:aws:iam::1:role/other" },
		func(c *Config) { c.RoleSession = "other" },
		func(c *Config) { c.WebIdentityTokenFile = "/other" },
		func(c *Config) { c.AccessKeyID = "OTHER" },
	} {
		c := *cfgA
		mutate(&c)
		other := &secretsClient{Provider: c.provider()}
		if other.identity() == a.identity() {
			t.Errorf("%+v shares an identity with %+v", c, *cfgA)
		}
	}
	for _, client := range []*secretsClient{
		{Provider: cfgA.provider(), Endpoint: "https://elsewhere.example"},
		{Provider: cfgA.provider(), Partition: "aws-us-gov"},
	} {
		if client.identity() == a.identity() {
			t.Errorf("endpoint %q partition %q share an identity with the default",
				client.Endpoint, client.Partition)
		}
	}
	// And the secret key itself does not appear in it: the key is held
	// in a map for minutes, and a heap dump is not a place for it.
	if strings.Contains(a.identity(), "one") {
		t.Errorf("the identity carries the secret key: %q", a.identity())
	}
	same := &secretsClient{Provider: (&Config{AccessKeyID: "AKID",
		SecretAccessKeyFile: cfgA.SecretAccessKeyFile}).provider()}
	if same.identity() != a.identity() {
		t.Error("the same block twice has two identities, so the cache would never hit")
	}
}

// Two blocks pointing at two endpoints -- a compatible service beside
// the real one, or two accounts' VPC endpoints -- read the same secret
// name from each, not whichever was asked first.
func TestTwoEndpointsAreTwoCacheEntries(t *testing.T) {
	cache = newSecretCache(time.Now)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	first := newFakeSecrets(t, map[string]string{"shared/name": "from-first"})
	second := newFakeSecrets(t, map[string]string{"shared/name": "from-second"})

	for _, c := range []struct {
		f    *fakeSecrets
		want string
	}{{first, "from-first"}, {second, "from-second"}} {
		out, err := handleBlock(t, `{"endpoint":"`+c.f.server.URL+`","region":"us-east-1",`+
			`"secrets":[{"name":"k","secret_id":"shared/name"}]}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := out["aws_secrets"].(map[string]any)["k"]; got != c.want {
			t.Errorf("the block for %s read %v", c.want, got)
		}
		if len(c.f.calls) != 1 {
			t.Errorf("the endpoint for %s was called %d time(s)", c.want, len(c.f.calls))
		}
	}
}
