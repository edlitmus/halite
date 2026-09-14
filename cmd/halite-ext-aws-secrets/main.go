// Command halite-ext-aws-secrets is an external pillar source for AWS
// Secrets Manager, and the worked example of the extension model.
//
// # What this is
//
// SPEC 24 replaces Salt's dynamic module loading. In Salt, a site puts
// `_pillar/aws_secrets_manager.py` in the file server, runs
// `saltutil.sync_all`, and the master imports it in-process as root // lexicon:allow
// with no signature requirement. That is the supply chain hole this
// project exists to close, and closing it is not much use unless the
// replacement is usable for the same job.
//
// So this is that job, done the new way. It is a separate executable
// speaking the JSON-over-stdio protocol of SPEC 24.2. It is packaged as
// a signed bundle, delivered under `_ext/` in the tree, verified on
// every load against a key the hub trusts, pinned by version and Merkle
// root, and run out of process in the sandbox of SPEC 24.3 with only
// the network declared. The hub invokes it while compiling pillar, the
// way Salt called `ext_pillar()`.
//
// Read it as a template. An extension of any kind is this shape: a
// `ext.Extension` with a name, a version, a kind, the signatures of
// what it provides, what it needs declared, and a handler. Everything
// below the handler is this extension's own business.
//
// # Behaviour
//
// Bug-for-bug where an existing tree depends on it. Secrets land under
// `aws_secrets`, a JSON secret is parsed into a mapping, a dotted name
// nests, and a value is cached for five minutes by default -- so
// `pillar.get('aws_secrets:database:creds:password')` resolves exactly
// as it did under Salt.
//
// Not bug-for-bug where the bug was the point: Salt's module logged a
// failed fetch and returned the secrets it did get, so a state applied
// with an empty password and nothing said so. This returns an error,
// and the hub fails the compilation.
//
// # Configuration
//
// Everything comes from the `ext_pillar` block, which the hub hands
// over untouched. The hub holds no `aws_secrets_*` settings of its own:
// a host that kept an extension's schema would be a second place for it
// to drift.
//
//	ext_pillar:
//	  - aws_secrets_manager:
//	      region: us-east-1
//	      secrets:
//	        - name: database.creds
//	          secret_id: arn:aws:secretsmanager:us-east-1:1:secret:prod/db-AbCdEf
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/edlitmus/halite/ext"
)

// Version is what the handshake and `sys.list_extensions` report. It is
// also what a pin names, so it changes when behaviour does.
const Version = "1.0.0"

func main() {
	// The resource limits of SPEC 24.3, applied by the extension to
	// itself. `setrlimit` bounds the calling process, so a host cannot
	// set a child's without setting its own: it names them in the
	// environment and a cooperating extension applies them here.
	//
	// This was missing, which meant the one extension this project
	// ships ran unbounded while `sys.list_extensions` reported cpu,
	// open-file and process limits as being in force. The two test
	// extensions called it and the generated skeleton emits it; the
	// shipped one did not, and nothing checked.
	ext.Confine()

	// The network is denied unless the manifest declared it, and the
	// denial is a declaration honoured rather than a boundary enforced
	// -- see `Sandbox.Describe`. This source cannot do its job without
	// it, so it says so now rather than failing later as a connection
	// that timed out against a link-local address.
	if ext.NetworkDenied() {
		fmt.Fprintln(os.Stderr, "aws_secrets_manager: the host did not grant the network, "+
			"and a secret cannot be fetched without it. The bundle's manifest has to declare "+
			"`network`; see docs/extensions.md.")
		os.Exit(1)
	}

	e := &ext.Extension{
		Name:    "aws_secrets_manager",
		Version: Version,
		Kind:    "pillar",
		// The network, and nothing else. Not root: reading a secret
		// needs no privilege, and the sandbox drops to the
		// unprivileged account the host names. An extension that
		// declared more than it needs would be signing off on more
		// than it needs.
		Declares:  []string{"network"},
		Functions: functions(),
		Handler:   handle,
	}
	if err := e.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "aws_secrets_manager:", err)
		os.Exit(1)
	}
}

// functions is the machine-readable signature of SPEC 15.6, which the
// host reads at handshake and `sys.list_extensions` reports.
//
// Declared as `ext.Signature` and nothing else. It used to be
// `[]json.RawMessage` marshalled by hand, which is how this extension
// came to send each parameter's type as an integer and have every
// signature refused; the typed field is the fix, and it is why a new
// extension cannot make that mistake.
func functions() []ext.Signature {
	return []ext.Signature{{
		Module:   "aws_secrets_manager",
		Function: "ext_pillar",
		Doc: "Fetch secrets from AWS Secrets Manager into pillar['aws_secrets'], " +
			"parsing a JSON secret into a mapping and nesting a dotted name.",
		Params: []ext.Param{
			{Name: "node_id", Type: ext.TypeString, Required: true,
				Doc: "The node the pillar is being compiled for."},
			{Name: "env", Type: ext.TypeString, Doc: "The pillar environment."},
			{Name: "grains", Type: ext.TypeMap,
				Doc: "The node's grains. A node controls these; see `node_grain`."},
			{Name: "pillar", Type: ext.TypeMap,
				Doc: "The pillar compiled so far, which the tree wrote."},
			{Name: "config", Type: ext.TypeMap,
				Doc: "This source's block from `ext_pillar`."},
		},
	}}
}

// request is what the host sends, mirroring what Salt handed
// `ext_pillar(minion_id, pillar, *args)`. // lexicon:allow
type request struct {
	NodeID string          `json:"node_id"`
	Env    string          `json:"env"`
	Grains json.RawMessage `json:"grains"`
	Pillar json.RawMessage `json:"pillar"`
	Config json.RawMessage `json:"config"`
}

// handle runs one pillar compilation's worth of work.
func handle(call ext.Call) (any, error) {
	if call.Function != "ext_pillar" {
		return nil, fmt.Errorf("this extension provides ext_pillar, not %q", call.Function)
	}
	var req request
	if len(call.Kwargs) > 0 {
		if err := json.Unmarshal(call.Kwargs, &req); err != nil {
			return nil, fmt.Errorf("the request is not readable: %w", err)
		}
	}

	cfg, err := parseConfig(req.Config)
	if err != nil {
		return nil, err
	}

	grains := map[string]any{}
	if len(req.Grains) > 0 {
		_ = json.Unmarshal(req.Grains, &grains)
	}
	treePillar := map[string]any{}
	if len(req.Pillar) > 0 {
		_ = json.Unmarshal(req.Pillar, &treePillar)
	}

	secrets, err := cfg.secretsFor(grains, treePillar)
	if err != nil {
		return nil, err
	}
	if len(secrets) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout())
	defer cancel()

	client := &secretsClient{
		Provider:  cfg.provider(),
		Partition: cfg.Partition,
		Endpoint:  cfg.Endpoint,
		Timeout:   cfg.timeout(),
	}

	flat := map[string]any{}
	var order []string
	for _, s := range secrets {
		region := cfg.regionFor(s, grains)
		if region == "" {
			return nil, fmt.Errorf("the secret %q names neither a region nor an ARN carrying one, "+
				"and neither `region` in this block nor this node's `region` grain is set", s.Key)
		}
		if _, dup := flat[s.Key]; dup {
			return nil, fmt.Errorf("two secrets both claim the pillar key %q", s.Key)
		}
		val, err := cache.fetch(ctx, client, s, region, cfg.cacheTTL())
		if err != nil {
			return nil, fmt.Errorf("fetching %q for the pillar key %q in %s: %w",
				s.SecretID, s.Key, region, err)
		}
		flat[s.Key] = val
		order = append(order, s.Key)
	}

	// A log frame, which the host records against this extension's
	// name. Counts and keys only: the values are the whole point of
	// not logging.
	call.Log("debug", fmt.Sprintf("fetched %d secret(s) for %s", len(order), req.NodeID))

	return map[string]any{cfg.root(): nest(flat, order)}, nil
}

// cache is process-wide, which is what makes the TTL worth having: the
// host keeps a pool of these processes alive across compilations, so a
// second node needing the same secret does not fetch it again.
var cache = newSecretCache(time.Now)
