package extpillar

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// treeLoader is a pillar tree held in memory, keyed `env|sls`.
type treeLoader map[string]string

func (m treeLoader) Source(env, sls string) ([]byte, string, error) {
	src, ok := m[env+"|"+sls]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", state.ErrNotFound, sls)
	}
	return []byte(src), sls + ".sls", nil
}

func (m treeLoader) Envs() []string { return []string{"base"} }

func (m treeLoader) Templates(env string) template.Loader { return treeTemplates{m, env} }

type treeTemplates struct {
	m   treeLoader
	env string
}

func (t treeTemplates) Load(name string) (string, string, error) {
	if src, ok := t.m[t.env+"|"+name]; ok {
		return src, name, nil
	}
	return "", "", template.ErrNotFound
}

// The whole path, end to end: a pillar tree, the compiler, this source,
// and a real Secrets Manager exchange over a signed request — because
// the seams between them are where a unit test of each piece proves
// nothing.
func TestTheSourceReachesPillarThroughTheCompiler(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{
		"vmop/dev/salt-api": `{"salt_api_pass":"s3cr3t"}`,
		"arn:aws-us-gov:secretsmanager:us-gov-east-1:1:secret:vmop/mongo-AbCdEf": "mongo-key-material",
	})
	src := f.source(t, AWSSecretsOptions{
		Secrets: []Secret{
			{Key: "salt_api_secret_id", SecretID: "vmop/dev/salt-api", Region: "us-east-1"},
		},
		NodeGrain:      "aws_secrets_ext_pillar",
		NodeGrainAllow: []string{"arn:aws-us-gov:secretsmanager:*:*:secret:vmop/*"},
	})

	tree := treeLoader{
		"base|top": "base:\n  '*':\n    - common\n",
		// The tree reads a secret that the external source has not
		// supplied yet, so this also pins the ordering: the source runs
		// after the tree and its value is what survives.
		"base|common": "java_home: /usr/lib/jdk17\naws_secrets:\n  salt_api_secret_id:\n    salt_api_pass: placeholder\n",
	}

	grains := value.MapOf(
		"id", "web1.prod",
		"os", "Ubuntu",
		"region", "us-gov-east-1",
		"aws_secrets_ext_pillar", []any{
			value.MapOf("pillar_key", "mongodb.mongokey",
				"secret_id", "arn:aws-us-gov:secretsmanager:us-gov-east-1:1:secret:vmop/mongo-AbCdEf"),
		},
	)

	var redacted []string
	src.opts.OnSecret = func(v string) { redacted = append(redacted, v) }

	c := &pillar.Compiler{
		Loader: tree,
		Config: pillar.Config{
			NodeID: "web1.prod",
			Grains: grains,
			Ext:    []pillar.ExtSource{src},
		},
	}
	out := c.Compile()
	if err := out.Err(); err != nil {
		t.Fatalf("compilation failed: %v", err)
	}

	// The tree's own value survives where the source does not touch it.
	if got, _ := out.Pillar.Get("java_home"); got != "/usr/lib/jdk17" {
		t.Errorf("java_home is %v", got)
	}
	// The source's value replaces the tree's placeholder. This is the
	// exact read `shared/salt/api.sls` makes.
	got, ok := value.Traverse(out.Pillar, "aws_secrets:salt_api_secret_id:salt_api_pass", ":")
	if !ok {
		t.Fatalf("the API password is absent; pillar is %v", out.Pillar.StringKeys())
	}
	if got != "s3cr3t" {
		t.Errorf("the API password is %v", got)
	}
	// And the secret this node named for itself arrived under its own
	// dotted key.
	if got, _ := value.Traverse(out.Pillar, "aws_secrets:mongodb:mongokey", ":"); got != "mongo-key-material" {
		t.Errorf("the node's own secret is %v", got)
	}

	if len(out.Ext) != 1 || out.Ext[0] != AWSSecretsName {
		t.Errorf("the contributing sources are %v", out.Ext)
	}
	for _, want := range []string{"s3cr3t", "mongo-key-material"} {
		if !contains(redacted, want) {
			t.Errorf("%q never reached the redactor", want)
		}
	}
}

// A secret the hub cannot read fails this node's compilation rather than
// handing it a pillar with the placeholder still in it.
func TestAnUnreadableSecretFailsTheCompilation(t *testing.T) {
	f := newFakeSecrets(t, nil)
	src := f.source(t, AWSSecretsOptions{Secrets: []Secret{
		{Key: "salt_api_secret_id", SecretID: "vmop/dev/salt-api", Region: "us-east-1"},
	}})
	tree := treeLoader{
		"base|top":    "base:\n  '*':\n    - common\n",
		"base|common": "aws_secrets:\n  salt_api_secret_id:\n    salt_api_pass: placeholder\n",
	}
	c := &pillar.Compiler{
		Loader: tree,
		Config: pillar.Config{NodeID: "web1.prod", Grains: value.NewMap(0), Ext: []pillar.ExtSource{src}},
	}
	out := c.Compile()
	err := out.Err()
	if err == nil {
		t.Fatal("the compilation succeeded with the secret missing")
	}
	if !strings.Contains(err.Error(), "vmop/dev/salt-api") {
		t.Errorf("the error does not name the secret: %q", err)
	}
	// The placeholder is still in out.Pillar, which is why this has to
	// be an error: a caller that ignored it would apply with it.
	if got, _ := value.Traverse(out.Pillar, "aws_secrets:salt_api_secret_id:salt_api_pass", ":"); got != "placeholder" {
		t.Errorf("the placeholder is %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
