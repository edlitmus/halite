package grains

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/awsauth"
	"github.com/edlitmus/halite/internal/value"
)

// fakeIMDS serves a metadata tree from a path-to-body map, the way the
// real service does: a directory answers with one entry per line.
type fakeIMDS struct {
	tree   map[string]string
	server *httptest.Server
	// noToken makes the service refuse IMDSv2, which is what a machine
	// that is not on EC2 amounts to.
	noToken bool
	gets    []string
}

func newFakeIMDS(t *testing.T, tree map[string]string) *fakeIMDS {
	t.Helper()
	f := &fakeIMDS{tree: tree}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/latest/api/token") {
			if f.noToken {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte("a-token"))
			return
		}
		if r.Header.Get("X-aws-ec2-metadata-token") != "a-token" {
			// IMDSv1 would be a plain GET. Answering it would let a
			// fallback pass the tests.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		f.gets = append(f.gets, path)
		body, ok := f.tree[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIMDS) imds() *awsauth.IMDS {
	return &awsauth.IMDS{Address: f.server.URL, Client: f.server.Client()}
}

func (f *fakeIMDS) collect(t *testing.T, exclude ...string) (*value.Map, []Warning) {
	t.Helper()
	g := value.NewMap(8)
	w := collectCloud(g, CloudOptions{IMDS: f.imds(), Exclude: exclude})
	return g, w
}

// A directory listing is descended into and the trailing slash is not
// part of the key, which is what `grains.get('meta-data:placement:region')`
// depends on.
func TestCloudGrainsNestTheMetadataTree(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":                             "instance-id\nlocal-ipv4\nplacement/\nservices/",
		"latest/meta-data/instance-id":                 "i-0123456789abcdef0",
		"latest/meta-data/local-ipv4":                  "10.20.0.10",
		"latest/meta-data/placement":                   "availability-zone\nregion",
		"latest/meta-data/placement/availability-zone": "us-east-1a",
		"latest/meta-data/placement/region":            "us-east-1",
		"latest/meta-data/services":                    "partition\ndomain",
		"latest/meta-data/services/partition":          "aws",
		"latest/meta-data/services/domain":             "amazonaws.com",
	})
	g, warnings := f.collect(t)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}

	for path, want := range map[string]string{
		"meta-data:instance-id":                 "i-0123456789abcdef0",
		"meta-data:local-ipv4":                  "10.20.0.10",
		"meta-data:placement:availability-zone": "us-east-1a",
		"meta-data:services:partition":          "aws",
	} {
		got, ok := value.Traverse(g, path, ":")
		if !ok {
			t.Errorf("%s is absent", path)
			continue
		}
		if got != want {
			t.Errorf("%s is %v, want %s", path, got, want)
		}
	}
}

// `0=name` is how public-keys is listed: the entry is addressed by the
// index and keyed by the name.
func TestCloudGrainsKeyAnIndexedListingByName(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":                           "public-keys/",
		"latest/meta-data/public-keys":               "0=deploy-key",
		"latest/meta-data/public-keys/0":             "openssh-key",
		"latest/meta-data/public-keys/0/openssh-key": "ssh-ed25519 AAAA deploy",
	})
	g, _ := f.collect(t)
	got, ok := value.Traverse(g, "meta-data:public-keys:deploy-key:openssh-key", ":")
	if !ok {
		t.Fatalf("the key is absent; tree is %v", sortedKeys(g))
	}
	if got != "ssh-ed25519 AAAA deploy" {
		t.Errorf("it read %v", got)
	}
}

// The curated grains SPEC 14.1 names come out of the same walk, and the
// identity document fills in what the tree does not carry.
func TestCuratedCloudGrainsComeFromTheWalk(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":                                                     "instance-id\ninstance-type\nami-id\nmac\nplacement/\nnetwork/\ntags/",
		"latest/meta-data/instance-id":                                         "i-0123456789abcdef0",
		"latest/meta-data/instance-type":                                       "m6i.large",
		"latest/meta-data/ami-id":                                              "ami-00112233",
		"latest/meta-data/mac":                                                 "02:aa:bb:cc:dd:ee",
		"latest/meta-data/placement":                                           "availability-zone",
		"latest/meta-data/placement/availability-zone":                         "us-gov-east-1b",
		"latest/meta-data/network":                                             "interfaces/",
		"latest/meta-data/network/interfaces":                                  "macs/",
		"latest/meta-data/network/interfaces/macs":                             "02:aa:bb:cc:dd:ee/",
		"latest/meta-data/network/interfaces/macs/02:aa:bb:cc:dd:ee":           "vpc-id\nsubnet-id",
		"latest/meta-data/network/interfaces/macs/02:aa:bb:cc:dd:ee/vpc-id":    "vpc-0abc",
		"latest/meta-data/network/interfaces/macs/02:aa:bb:cc:dd:ee/subnet-id": "subnet-0def",
		"latest/meta-data/tags":                                                "instance/",
		"latest/meta-data/tags/instance":                                       "Name\nenvironment",
		"latest/meta-data/tags/instance/Name":                                  "web-01",
		"latest/meta-data/tags/instance/environment":                           "prod",

		"latest/dynamic":                            "instance-identity/",
		"latest/dynamic/instance-identity":          "document",
		"latest/dynamic/instance-identity/document": `{"accountId":"123456789012","region":"us-gov-east-1"}`,
	})
	g, _ := f.collect(t)

	want := map[string]string{
		"cloud":             "ec2",
		"instance_id":       "i-0123456789abcdef0",
		"instance_type":     "m6i.large",
		"image_id":          "ami-00112233",
		"availability_zone": "us-gov-east-1b",
		// Absent from `placement/`, so it comes from the identity
		// document rather than being missing.
		"region":     "us-gov-east-1",
		"account_id": "123456789012",
		"vpc_id":     "vpc-0abc",
		"subnet_id":  "subnet-0def",
	}
	for name, wantVal := range want {
		got, ok := g.Get(name)
		if !ok {
			t.Errorf("the %s grain is absent", name)
			continue
		}
		if got != wantVal {
			t.Errorf("the %s grain is %v, want %s", name, got, wantVal)
		}
	}
	tags, ok := value.Traverse(g, "tags:Name", ":")
	if !ok || tags != "web-01" {
		t.Errorf("the tags grain is %v", tags)
	}
}

// The identity document keeps its JSON text, because that is what Salt's
// metadata grain produced and what an existing state reads.
func TestTheIdentityDocumentStaysItsJSONText(t *testing.T) {
	doc := `{"accountId":"123456789012","region":"eu-west-1"}`
	f := newFakeIMDS(t, map[string]string{
		"latest/dynamic":                            "instance-identity/",
		"latest/dynamic/instance-identity":          "document",
		"latest/dynamic/instance-identity/document": doc,
	})
	g, _ := f.collect(t)
	got, ok := value.Traverse(g, "dynamic:instance-identity:document", ":")
	if !ok {
		t.Fatal("the document is absent")
	}
	if got != doc {
		t.Errorf("it is %#v", got)
	}
	// And the values inside it still reach the curated grains.
	if region, _ := g.Get("region"); region != "eu-west-1" {
		t.Errorf("the region grain is %v", region)
	}
}

// Salt's metadata grain walks into `iam/security-credentials/<role>` and
// publishes the instance's live access key, secret key and session token
// as grains. That is not reproduced.
func TestTheInstanceCredentialsAreNeverCollected(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":                                   "instance-id\niam/",
		"latest/meta-data/instance-id":                       "i-0123456789abcdef0",
		"latest/meta-data/iam":                               "info\nsecurity-credentials/",
		"latest/meta-data/iam/info":                          `{"InstanceProfileArn":"arn:aws:iam::1:instance-profile/p"}`,
		"latest/meta-data/iam/security-credentials":          "the-role",
		"latest/meta-data/iam/security-credentials/the-role": `{"SecretAccessKey":"SHOULD-NOT-APPEAR"}`,
	})
	g, _ := f.collect(t)

	if _, ok := value.Traverse(g, "meta-data:iam:security-credentials", ":"); ok {
		t.Error("the credential path was collected")
	}
	// The rest of the iam section is still there: the exclusion is one
	// path, not the section.
	if _, ok := value.Traverse(g, "meta-data:iam:info", ":"); !ok {
		t.Error("iam/info was dropped along with the credentials")
	}
	for _, path := range f.gets {
		if strings.Contains(path, "security-credentials/") {
			t.Errorf("it requested %s", path)
		}
	}
}

// An extra exclusion from the configuration is honoured.
func TestCloudGrainsHonourAnExtraExclusion(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":               "instance-id\nuser-data-ish",
		"latest/meta-data/instance-id":   "i-0123456789abcdef0",
		"latest/meta-data/user-data-ish": "secret",
	})
	g, _ := f.collect(t, "latest/meta-data/user-data-ish")
	if _, ok := value.Traverse(g, "meta-data:user-data-ish", ":"); ok {
		t.Error("the excluded path was collected")
	}
	if _, ok := value.Traverse(g, "meta-data:instance-id", ":"); !ok {
		t.Error("the exclusion took the rest of the tree with it")
	}
}

// A machine that is not in a cloud gets one warning and no grains,
// rather than a collection that fails and leaves the node with none.
func TestCloudGrainsOnAMachineThatIsNotInACloud(t *testing.T) {
	f := newFakeIMDS(t, nil)
	f.noToken = true
	g, warnings := f.collect(t)
	if g.Len() != 0 {
		t.Errorf("it set %v", sortedKeys(g))
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings: %v", warnings)
	}
	if !strings.Contains(warnings[0].Msg, "IMDSv1 is not used") {
		t.Errorf("the warning is %q", warnings[0].Msg)
	}
}

// A path that is absent on this instance shape is an absent grain, not a
// warning: no spot section and no tags in metadata are both normal.
func TestAnAbsentSectionIsNotAWarning(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":             "instance-id\nspot/",
		"latest/meta-data/instance-id": "i-0123456789abcdef0",
	})
	_, warnings := f.collect(t)
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
}

// The walk is bounded. A service that answers a listing with itself
// would otherwise be followed until something ran out.
func TestTheWalkIsBounded(t *testing.T) {
	tree := map[string]string{"latest/meta-data": "loop/"}
	path := "latest/meta-data/loop"
	for i := 0; i < 40; i++ {
		tree[path] = "loop/"
		path += "/loop"
	}
	f := newFakeIMDS(t, tree)
	f.collect(t)
	if len(f.gets) > cloudMaxRequests {
		t.Errorf("it made %d requests", len(f.gets))
	}
	if len(f.gets) > cloudWalkDepth+2 {
		t.Errorf("it descended %d levels", len(f.gets))
	}
}

// Collect runs the walk only when it is asked to. The round trip is the
// reason the grain is opt-in.
func TestCloudGrainsAreNotCollectedUnlessEnabled(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":             "instance-id",
		"latest/meta-data/instance-id": "i-0123456789abcdef0",
	})
	opts := Options{NodeID: "n1", CloudOptions: CloudOptions{IMDS: f.imds()}}
	g, _ := Collect(opts)
	if g.Has("meta-data") {
		t.Error("the metadata tree was collected with cloud_grains off")
	}
	if len(f.gets) != 0 {
		t.Errorf("it made %d requests", len(f.gets))
	}

	opts.Cloud = true
	g, _ = Collect(opts)
	if !g.Has("meta-data") {
		t.Error("the metadata tree was not collected with cloud_grains on")
	}
}

// A static grains file is merged after the cloud grains, so an operator
// can still override one.
func TestAStaticFileOverridesACloudGrain(t *testing.T) {
	f := newFakeIMDS(t, map[string]string{
		"latest/meta-data":                  "placement/",
		"latest/meta-data/placement":        "region",
		"latest/meta-data/placement/region": "us-east-1",
	})
	dir := t.TempDir()
	static := dir + "/grains"
	if err := os.WriteFile(static, []byte("region: us-west-2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, _ := Collect(Options{
		NodeID:       "n1",
		Cloud:        true,
		StaticFile:   static,
		CloudOptions: CloudOptions{IMDS: f.imds()},
	})
	if region, _ := g.Get("region"); region != "us-west-2" {
		t.Errorf("the region grain is %v", region)
	}
}

// sortedKeys reports a map's keys in a stable order, so a failure names
// what was actually collected.
func sortedKeys(m *value.Map) []string {
	keys := m.StringKeys()
	sort.Strings(keys)
	return keys
}
