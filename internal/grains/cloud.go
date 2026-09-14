package grains

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
	"github.com/edlitmus/halite/internal/value"
)

// The cloud grains of SPEC section 14.1, opt-in with `cloud_grains`.
//
// Two things are collected, and they serve different readers.
//
// The first is the whole `latest/meta-data` and `latest/dynamic` trees,
// nested, under the `meta-data` and `dynamic` grains. That is what Salt's
// `_grains/metadata.py` produced, and an existing tree reads paths out of
// it — `grains.get('meta-data:local-ipv4')`, `meta-data:services:partition`
// — so a migration that dropped it would break states that have nothing
// to do with this change.
//
// The second is the curated set SPEC 14.1 names: `cloud`, `instance_id`,
// `region`, and their neighbours, derived from the same walk rather than
// fetched again. A new tree should read those, because they are the same
// names on every provider.
//
// IMDSv2 throughout, and the credential paths are never collected. See
// excludedPaths.

// cloudWalkDepth bounds how deep the walk descends.
//
// The real tree is about six levels. The bound is not there for the real
// tree: it is there because a walk that trusts a listing to be acyclic
// and finite is a walk that a wrong answer can run forever.
const cloudWalkDepth = 12

// cloudMaxRequests bounds one collection.
//
// A full EC2 tree is roughly 120 requests. Twice that leaves room for
// instance shapes with many interfaces and many tags, and still stops a
// service answering nonsense from being followed indefinitely.
const cloudMaxRequests = 400

// excludedPaths are never read, however the walk arrives at them.
//
// `iam/security-credentials/<role>` is the instance's live access key,
// secret key and session token. Salt's metadata grain walks into it and
// publishes all three as grains, which then travel off the machine,
// land in the grain cache, and appear in `grains.items` output. That is a
// credential leak with a Salt bug number and no good reason to
// reproduce. `identity-credentials` is the same thing under another
// name.
var excludedPaths = []string{
	"latest/meta-data/iam/security-credentials",
	"latest/meta-data/identity-credentials",
}

// CloudOptions configure the metadata walk.
type CloudOptions struct {
	// IMDS reads the metadata service. Nil takes the real one.
	IMDS *awsauth.IMDS
	// Timeout bounds the whole collection, not one request.
	Timeout time.Duration
	// Exclude names extra path prefixes never to read, added to
	// excludedPaths.
	Exclude []string
}

// collectCloud fills in the cloud grains, and reports what it could not
// read.
//
// A metadata service that does not answer is one warning and no grains,
// not a failed collection: `cloud_grains: true` on a machine that turns
// out not to be in a cloud must not stop the node from having grains at
// all.
func collectCloud(g *value.Map, opts CloudOptions) []Warning {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	md := opts.IMDS
	if md == nil {
		md = &awsauth.IMDS{}
	}
	w := &cloudWalk{
		ctx:     ctx,
		imds:    md,
		exclude: append(append([]string{}, excludedPaths...), opts.Exclude...),
	}

	// The token exchange is the probe. A machine that is not on EC2 does
	// not answer it, and there is then nothing to walk.
	if _, err := md.Token(ctx); err != nil {
		return []Warning{{Source: "cloud_grains", Msg: err.Error()}}
	}

	meta := w.search("latest/meta-data")
	dynamic := w.search("latest/dynamic")
	if meta != nil {
		g.Set("meta-data", meta)
	}
	if dynamic != nil {
		g.Set("dynamic", dynamic)
	}
	curatedCloudGrains(g, meta, dynamic)
	return w.warnings
}

// cloudWalk carries the state one collection needs.
type cloudWalk struct {
	ctx      context.Context
	imds     *awsauth.IMDS
	exclude  []string
	requests int
	warnings []Warning
}

func (w *cloudWalk) excluded(path string) bool {
	for _, p := range w.exclude {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// get reads one path, counting it against the request budget.
func (w *cloudWalk) get(path string) (string, bool) {
	if w.requests >= cloudMaxRequests {
		return "", false
	}
	w.requests++
	body, err := w.imds.Get(w.ctx, path)
	if err != nil {
		// A path that is absent on this instance shape — no spot
		// section, no IPv6, no tags in metadata — is not a problem to
		// report. Anything else is.
		if !errors.Is(err, awsauth.ErrNotFound) {
			w.warnings = append(w.warnings, Warning{Source: "cloud_grains", Msg: path + ": " + err.Error()})
		}
		return "", false
	}
	return string(body), true
}

// search reads one node of the metadata tree and everything under it.
//
// This is `_search` from `_grains/metadata.py`, rule for rule, because
// the shape it produces is what existing states index into:
//
//   - a line ending in `/` is a directory: recurse, and drop the slash
//     from the key;
//   - a line of the form `0=name` is a listing whose entry is addressed
//     by the index and named by the value, which is how `public-keys`
//     is laid out: recurse into the index, and key the result by the
//     name;
//   - anything else is a leaf: read it as a string.
//
// The one deliberate difference is that a leaf is not parsed as JSON.
// `dynamic/instance-identity/document` stays the JSON text it is in
// Salt, so a state that already reads it keeps working. The values
// inside it reach the curated grains parsed, which is where a new tree
// should read them.
func (w *cloudWalk) search(path string) *value.Map {
	return w.searchAt(path, 0)
}

func (w *cloudWalk) searchAt(path string, depth int) *value.Map {
	if depth >= cloudWalkDepth || w.excluded(path) {
		return nil
	}
	body, ok := w.get(path)
	if !ok {
		return nil
	}
	out := value.NewMap(8)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasSuffix(line, "/"):
			name := strings.TrimSuffix(line, "/")
			if sub := w.searchAt(path+"/"+name, depth+1); sub != nil {
				out.Set(name, sub)
			}
		case strings.Contains(line, "="):
			index, name, _ := strings.Cut(line, "=")
			if sub := w.searchAt(path+"/"+index, depth+1); sub != nil {
				out.Set(name, sub)
			}
		default:
			if w.excluded(path + "/" + line) {
				continue
			}
			if leaf, ok := w.get(path + "/" + line); ok {
				out.Set(line, leaf)
			}
		}
	}
	if out.Len() == 0 {
		return nil
	}
	return out
}

// identityDocument is the part of `dynamic/instance-identity/document`
// the curated grains come from.
type identityDocument struct {
	AccountID        string `json:"accountId"`
	Region           string `json:"region"`
	AvailabilityZone string `json:"availabilityZone"`
	InstanceID       string `json:"instanceId"`
	InstanceType     string `json:"instanceType"`
	ImageID          string `json:"imageId"`
}

// curatedCloudGrains derives the flat set SPEC 14.1 names from the tree
// that has already been fetched.
//
// Derived rather than fetched again: a second round of requests for
// values that are already in memory is the cost this grain is opt-in to
// avoid.
func curatedCloudGrains(g *value.Map, meta, dynamic *value.Map) {
	if meta == nil && dynamic == nil {
		return
	}
	g.Set("cloud", "ec2")

	var doc identityDocument
	if dynamic != nil {
		if raw, ok := value.Traverse(dynamic, "instance-identity:document", ":"); ok {
			if text, ok := raw.(string); ok {
				_ = json.Unmarshal([]byte(text), &doc)
			}
		}
	}

	setIfFound(g, "instance_id", meta, "instance-id", doc.InstanceID)
	setIfFound(g, "instance_type", meta, "instance-type", doc.InstanceType)
	setIfFound(g, "image_id", meta, "ami-id", doc.ImageID)
	setIfFound(g, "availability_zone", meta, "placement:availability-zone", doc.AvailabilityZone)
	// `placement/region` is newer than the identity document and absent
	// on older instance shapes, so the document is the fallback rather
	// than the source.
	setIfFound(g, "region", meta, "placement:region", doc.Region)
	if doc.AccountID != "" {
		g.Set("account_id", doc.AccountID)
	}

	// The VPC and subnet live under the primary interface's MAC, which
	// is named by `mac` rather than being the only one: an instance with
	// several interfaces has several, and the primary is the one the
	// instance itself reports.
	if meta != nil {
		// Not value.Traverse: a MAC address is full of colons, and a
		// colon-delimited path through it selects the wrong thing.
		if mac, ok := stringAt(meta, "mac"); ok {
			iface := []string{"network", "interfaces", "macs", mac}
			if s, ok := stringAtPath(meta, append(iface, "vpc-id")...); ok {
				g.Set("vpc_id", s)
			}
			if s, ok := stringAtPath(meta, append(iface, "subnet-id")...); ok {
				g.Set("subnet_id", s)
			}
		}
		if tags, ok := value.Traverse(meta, "tags:instance", ":"); ok {
			if m, ok := tags.(*value.Map); ok && m.Len() > 0 {
				g.Set("tags", m)
			}
		}
	}
}

// setIfFound sets a grain from a path in the tree, or from the identity
// document when the path is absent, and sets nothing when neither has it.
func setIfFound(g *value.Map, name string, tree *value.Map, path, fallback string) {
	if tree != nil {
		if s, ok := stringAt(tree, path); ok {
			g.Set(name, s)
			return
		}
	}
	if fallback != "" {
		g.Set(name, fallback)
	}
}

// stringAtPath walks named keys one at a time, so a key that contains
// the traversal delimiter is still addressable.
func stringAtPath(m *value.Map, parts ...string) (string, bool) {
	var cur any = m
	for _, part := range parts {
		node, ok := cur.(*value.Map)
		if !ok {
			return "", false
		}
		if cur, ok = node.Get(part); !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func stringAt(m *value.Map, path string) (string, bool) {
	raw, ok := value.Traverse(m, path, ":")
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}
