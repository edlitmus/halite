package extpillar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// perKeyAWS answers GetSecretValue according to which access key signed
// the request, as an IAM policy would, and counts requests per key.
type perKeyAWS struct {
	*httptest.Server
	mu    sync.Mutex
	calls map[string]int
}

func newPerKeyAWS(t *testing.T, readable map[string]map[string]string) *perKeyAWS {
	t.Helper()
	a := &perKeyAWS{calls: map[string]int{}}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, rest, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		akid, _, _ := strings.Cut(rest, "/")
		var req struct {
			SecretId string `json:"SecretId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		a.mu.Lock()
		a.calls[akid]++
		a.mu.Unlock()
		if val, ok := readable[akid][req.SecretId]; ok {
			fmt.Fprintf(w, `{"Name":%q,"SecretString":%q}`, req.SecretId, val)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"not authorized"}`))
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *perKeyAWS) count(akid string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[akid]
}

// compileWith compiles a one-node pillar with the hub's `ext_pillar`
// list as written, against the runtime's one extension process.
func compileWith(t *testing.T, block string, sources func(specs []Spec) []pillar.ExtSource) *pillar.Compiled {
	t.Helper()
	doc, _, err := yaml.Parse([]byte(block), yaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := doc.(*value.Map).Get("ext_pillar")
	specs, err := ParseList(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	c := &pillar.Compiler{
		Loader: treeLoader{"base|top": "base: {}\n"},
		Config: pillar.Config{
			NodeID: "web1.prod",
			Grains: value.MapOf("id", "web1.prod"),
			Ext:    sources(specs),
		},
	}
	return c.Compile()
}

// Two `aws_secrets_manager` blocks on one hub share one extension
// process, and so one cache. A block must not be handed what the other
// block's credentials or endpoint fetched (issue 622): the cache was
// keyed by region, secret and stage alone, so the second block below
// was served the first block's value and Secrets Manager never saw the
// second block's key at all.
//
// This is the operator's path: the real binary, signed and verified,
// over the real bridge, compiled by the real pillar compiler, with the
// two blocks written in one `ext_pillar` list.
func TestTwoBlocksDoNotShareWhatTheyFetched(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	rt := startExtension(t)
	sources := func(specs []Spec) []pillar.ExtSource {
		out, err := Sources(specs, rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	keyFile := func(body string) string {
		p := filepath.Join(t.TempDir(), "secret-key")
		if err := os.WriteFile(p, []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("credentials", func(t *testing.T) {
		aws := newPerKeyAWS(t, map[string]map[string]string{
			"AKIDHIGH": {"prod/db": "hunter22"},
		})
		block := func(root, akid string) string {
			return fmt.Sprintf(`
  - aws_secrets_manager:
      pillar_key: %s
      endpoint: %s
      region: us-east-1
      access_key_id: %s
      secret_access_key_file: %s
      secrets:
        - name: db
          secret_id: prod/db
`, root, aws.URL, akid, keyFile("secret-"+akid))
		}
		out := compileWith(t, "ext_pillar:"+block("privileged", "AKIDHIGH")+block("restricted", "AKIDLOW"), sources)
		err := out.Err()
		if err == nil {
			got, _ := value.Traverse(out.Pillar, "restricted:db", ":")
			t.Fatalf("the restricted block compiled, with db = %v", got)
		}
		if !strings.Contains(err.Error(), "AccessDenied") {
			t.Errorf("the failure is not the service's refusal: %v", err)
		}
		if n := aws.count("AKIDLOW"); n != 1 {
			t.Errorf("Secrets Manager saw the restricted key %d time(s), want 1", n)
		}
	})

	t.Run("endpoint", func(t *testing.T) {
		first := newPerKeyAWS(t, map[string]map[string]string{"AKID": {"shared/name": "from-first"}})
		second := newPerKeyAWS(t, map[string]map[string]string{"AKID": {"shared/name": "from-second"}})
		key := keyFile("secret")
		block := func(root, url string) string {
			return fmt.Sprintf(`
  - aws_secrets_manager:
      pillar_key: %s
      endpoint: %s
      region: us-east-1
      access_key_id: AKID
      secret_access_key_file: %s
      secrets:
        - name: k
          secret_id: shared/name
`, root, url, key)
		}
		out := compileWith(t, "ext_pillar:"+block("one", first.URL)+block("two", second.URL), sources)
		if err := out.Err(); err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{"one:k": "from-first", "two:k": "from-second"} {
			if got, _ := value.Traverse(out.Pillar, path, ":"); got != want {
				t.Errorf("%s is %v, want %s", path, got, want)
			}
		}
		if first.count("AKID") != 1 || second.count("AKID") != 1 {
			t.Errorf("the endpoints were called %d and %d time(s), want 1 each",
				first.count("AKID"), second.count("AKID"))
		}
	})
}
