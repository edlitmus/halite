package config

// KeyDoc is the expanded documentation for one setting: which topic it
// belongs to, and what an operator needs to know beyond the one-line
// meaning in the key table.
//
// Kept beside the table rather than in it. The table is a dense list
// that is easy to scan for a name; this is prose, and mixing the two
// made the table unreadable and the prose cramped. An audit requires
// every key to appear here, so a setting cannot be added without being
// explained.
type KeyDoc struct {
	// Group is the topic heading the reference files it under.
	Group string
	// Detail says what the one-liner cannot: when to change it, what it
	// interacts with, and what happens if it is wrong. Empty only where
	// the one-liner is genuinely the whole story.
	Detail string
}

// Group is a topic in the configuration reference, in the order an
// operator meets it rather than alphabetically.
type Group struct {
	Name  string
	Intro string
}

// Groups orders the reference. A key naming a group not listed here is
// an error, so the two cannot drift.
var Groups = []Group{
	{"Identity and connection", `Who a program is and what it talks to. A node dials the hub
and the hub never dials a node, so these are the settings that decide
whether anything talks at all.`},

	{"Filesystem layout", `Where each program keeps its files. The defaults follow the
platform, so most estates set none of these; a packager or an operator
splitting state across filesystems sets all of them.`},

	{"Enrollment and certificates", `How a node gets the certificate it authenticates with, and
how long it lasts. There is no auto-accept in any of them.`},

	{"The tree: states and pillar", `Where the state tree and the pillar tree live, which
environment a run uses, and how sources merge. On a hub these serve the
fleet; on a node they are what a masterless run reads.`},

	{"State runs", `What a state run is allowed to touch and how it behaves when
something fails.`},

	{"Rendering and templates", `The renderer pipeline and the template engine's dialect.
These are the settings that decide whether a tree written for Salt
renders the same way here.`},

	{"The file server", `What the hub serves under salt:// and what it refuses to
serve.`},

	{"The git file server", `Serving a state tree straight out of a git repository, where
a branch is an environment.`},

	{"The S3 file server", `Serving a state tree out of S3 or an S3-compatible service,
with the request signing done in-house.`},

	{"Relays", `A hub that serves its own segment and reports to an upstream
hub as a single client.`},

	{"Agentless mode", `Running against a machine with no agent, over the system
ssh.`},

	{"Grains and the mine", `Facts a node reports about itself, and what it publishes for
other nodes to read.`},

	{"Scheduling and beacons", `Work a node starts on its own: the scheduler that replaces
cron, and the beacons that turn a local condition into an event.`},

	{"Reactors", `Turning an event into a job, on the hub.`},

	{"Extensions", `Functions written in another language, delivered as signed
bundles and run out of process.`},

	{"Returners", `Where a job's answer goes besides the job cache.`},

	{"Targeting and the job cache", `Naming sets of nodes, and how long the record of a job is
kept.`},

	{"Authorization", `Who may ask for what. Deny by default, in one file.`},

	{"The API service", `Settings only halite-api reads: what it listens on, how long
a token lives, and what it accepts from outside.`},

	{"LDAP and Active Directory", `Authenticating operators against a directory.`},

	{"OpenID Connect", `Authenticating operators against an identity provider.`},

	{"The event bus", `How much of the estate's history the hub keeps.`},

	{"Node execution controls", `What a node will and will not run, and how much of it at
once.`},

	{"Logging and diagnostics", `What each program says about itself.`},
}

// KeyDocs is the expanded documentation, one entry per key in Keys.
//
// Ordered by group in the reference rather than here; this is a map so
// that a key and its documentation are looked up together and neither
// can be silently dropped.
var KeyDocs = map[string]KeyDoc{
	"node_id": {
		Group:  "Identity and connection",
		Detail: "The name this node is known by everywhere: in targeting, in its certificate, in the job cache, and in every event it raises. Set it explicitly on anything whose hostname might change. Salt calls this `id`, and the same value is what `keys accept` names.",
	},
	"node_id_source": {
		Group:  "Identity and connection",
		Detail: "`auto` tries each source in the order SPEC 7.2 gives and takes the first that answers. Pin it to one — `config`, `fqdn`, `cloud` — when a machine has several plausible names and you want to know which one it will pick before it picks.",
	},
	"node_id_caching": {
		Group:  "Identity and connection",
		Detail: "On by default: the name resolved at first enrollment is written down and reused, so a DHCP lease or a cloud rename cannot silently turn one node into two. Turn it off only for an image that is meant to re-identify on every boot.",
	},
	"node_id_lowercase": {
		Group:  "Identity and connection",
		Detail: "For estates whose hostnames vary in case. Targeting is case-sensitive, so `WEB1` and `web1` are two different nodes without this.",
	},
	"node_id_remove_domain": {
		Group:  "Identity and connection",
		Detail: "Turns `web1.prod.example.com` into `web1`. Convenient and lossy: two machines in different domains can then collide, and the collision looks like one node reconnecting.",
	},
	"hub": {
		Group:  "Identity and connection",
		Detail: "Written as a host, `host:port`, or a URL. Because the connection is always outbound from the node, a node behind NAT needs no inbound rule and no port forwarded. Salt's `master` is accepted as an alias. <!-- lexicon:allow -->",
	},
	"hub_port": {
		Group:  "Identity and connection",
		Detail: "Only read when `hub` names no port. One port carries everything — jobs, returns, files, events — because SPEC 6.1 has a single mutual-TLS listener rather than Salt's separate publish and return ports.",
	},
	"hub_ca_file": {
		Group:  "Identity and connection",
		Detail: "A CA delivered by your own route rather than fetched from the hub. Optional: a node with only `hub_fingerprint` fetches the certificate and checks it against that. Supplying one here does not remove the need for the fingerprint, because a CA this node has not already pinned is one it is being asked to start trusting. Once enrollment succeeds the CA is written into this node's own `pki_dir` and this setting is no longer read. `--ca-file` overrides it.",
	},
	"hub_fingerprint": {
		Group:  "Identity and connection",
		Detail: "Required to enrol. The node fetches the hub's CA and trusts it only if it matches this, so the fingerprint is the whole of the trust decision at first contact — there is no mode that skips it. Deliver it by a route separate from the network the node enrols over; print it on the hub with `halite-hub keys fingerprint`, named with no node. A node that has already pinned a CA does not read this again, which is why `connect` and `renew` need none.",
	},
	"hub_alive_interval": {
		Group:  "Identity and connection",
		Detail: "How often the hub pings down the open stream. It is what detects a connection that has stopped carrying traffic without closing, which is the common failure behind a firewall with an idle timeout.",
	},
	"hub_tries": {
		Group:  "Identity and connection",
		Detail: "0 means retry for ever, which is what a managed node should do. A finite count is for a one-shot container that should exit rather than sit reconnecting.",
	},
	"hub_type": {
		Group:  "Identity and connection",
		Detail: "`static` uses the first hub in the list; `failover` tries each in turn. Salt's `master_type` with the same meanings.",
	},
	"listen": {
		Group:  "Identity and connection",
		Detail: "The address the hub or the API binds. Bind to a specific address rather than every interface when the machine has a management network; there is no plaintext mode to fall back to, so this is the whole of the exposure.",
	},
	"pki_dir": {
		Group:  "Filesystem layout",
		Detail: "Certificates and private keys. Mode matters: the node's key is 0600 and the directory 0700. On the hub this also holds the enrollment CA, which is the most valuable thing in the estate.",
	},
	"cache_dir": {
		Group:  "Filesystem layout",
		Detail: "Everything here can be deleted at any time and will be refetched — the cached tree, gitfs mirrors, s3 objects. It is the first thing to point at a larger filesystem.",
	},
	"state_dir": {
		Group:  "Filesystem layout",
		Detail: "Everything here is durable and losing it loses history: the job cache, the event bus, the key store, a relay's spool. Back it up; do not put it on tmpfs.",
	},
	"socket_dir": {
		Group:  "Filesystem layout",
		Detail: "Unix sockets and PID files. On a system with a tmpfs `/run` this is cleared on boot, which is correct.",
	},
	"config_file": {
		Group:  "Filesystem layout",
		Detail: "Set by the loader to whatever file it actually read, so a program can report its own configuration path. Not something to set by hand.",
	},
	"enrollment_mode": {
		Group:  "Enrollment and certificates",
		Detail: "`manual` holds every request until an operator compares the fingerprint and accepts it. `token` admits a node presenting a bootstrap token, for provisioning at scale. `attested` is named and refused; it is not built. There is deliberately no equivalent of Salt's `auto_accept`, which is how estates end up trusting whatever asked.",
	},
	"certificate_lifetime": {
		Group:  "Enrollment and certificates",
		Detail: "A node renews at half of this, so a shorter lifetime means more renewals and a smaller window for a stolen key. Renewal is automatic and needs no operator, so this can be short.",
	},
	"key_algorithm": {
		Group:  "Enrollment and certificates",
		Detail: "ECDSA P-256 is the default because one certificate profile then works everywhere, FIPS hosts included. `ed25519` is the better algorithm and is not FIPS-approved, so a FIPS build refuses it by name.",
	},
	"file_roots": {
		Group:  "The tree: states and pillar",
		Detail: "A mapping of environment to an ordered list of directories, searched in order, first match winning. On a hub this is what the fleet fetches; on a node it is what a `--local` run reads. The configuration root is probed for a `state` directory first, so a tree beside the configuration file needs no setting at all.",
	},
	"pillar_roots": {
		Group:  "The tree: states and pillar",
		Detail: "The same shape as `file_roots`, for pillar. Read by the hub when it compiles pillar for the fleet, and by a node compiling its own in masterless mode — so a masterless node sets both.",
	},
	"env": {
		Group:  "The tree: states and pillar",
		Detail: "The environment a run uses when nothing names one. `saltenv` is a permanent alias, not a deprecation. With gitfs, an environment is a branch.",
	},
	"pillarenv": {
		Group:  "The tree: states and pillar",
		Detail: "Lets pillar come from a different environment than states. Leaving it unset — pillar following `env` — is what most estates want; setting it is how a test environment reads production pillar by accident.",
	},
	"env_allowlist": {
		Group:  "The tree: states and pillar",
		Detail: "Environments a run may name. Empty permits any. Set it when environments come from git branches and anyone can push one.",
	},
	"env_denylist": {
		Group:  "The tree: states and pillar",
		Detail: "The other direction, applied after the allowlist.",
	},
	"top_file_merging_strategy": {
		Group:  "The tree: states and pillar",
		Detail: "How several environments' top files combine. `merge` takes all of them, `same` requires each environment to describe only itself, `merge_all` merges without regard to which environment declared what. A branch's own `top.sls` declares its environment; the branch name does not.",
	},
	"pillar_source_merging_strategy": {
		Group:  "The tree: states and pillar",
		Detail: "How two pillar files setting the same key are reconciled. `smart` is Salt's default behaviour, `recurse` merges mappings deeply, `aggregate` combines lists and mappings, `overwrite` takes the last. Changing this changes what every node's pillar contains, so change it in test first.",
	},
	"pillar_merge_lists": {
		Group:  "The tree: states and pillar",
		Detail: "Whether lists concatenate rather than replace when pillar sources merge. Off by default, matching Salt.",
	},
	"pillar_trusted_grains": {
		Group:  "The tree: states and pillar",
		Detail: "The allowlist of grains a pillar top file may target on. A node controls its own grains, so a node that can name an arbitrary grain in pillar targeting can ask for another node's secrets. Custom grains are excluded by default and adding one is a deliberate act.",
	},
	"pillar_cache_disk": {
		Group:  "The tree: states and pillar",
		Detail: "Keeps compiled pillar on the node so a run survives a hub outage. It is encrypted at rest because pillar is where the secrets are; it is still a copy of them on that disk.",
	},
	"ext_pillar": {
		Group:  "The tree: states and pillar",
		Detail: "Named and not built. The setting warns at startup that the sources it lists contribute nothing, rather than silently compiling pillar without them.",
	},
	"ext_pillar_fail": {
		Group:  "The tree: states and pillar",
		Detail: "`hard` fails the whole compilation when an external source fails, which is the right default: a pillar missing the half that holds the credentials is worse than no pillar, because the run proceeds with it.",
	},
	"state_allowlist": {
		Group:  "State runs",
		Detail: "SLS names a run may include, as globs. Empty permits any. This is the control that keeps an operator with `state.apply` from applying anything in the tree, and it is enforced beside the policy's own `allow_sls`.",
	},
	"state_denylist": {
		Group:  "State runs",
		Detail: "Applied after the allowlist, for carving one dangerous SLS out of a permitted set.",
	},
	"startup_states": {
		Group:  "State runs",
		Detail: "What a node runs when it starts: `highstate`, or `sls`/`top` with the names to run. Off by default. Turning it on means a reboot converges the machine, which is usually what you want and is occasionally a surprise at 3am.",
	},
	"failhard": {
		Group:  "State runs",
		Detail: "Stops the whole run at the first failure rather than continuing to the states that did not depend on it. Per-state `failhard` overrides this.",
	},
	"test": {
		Group:  "State runs",
		Detail: "Makes every run a dry run unless something explicitly asks for a real one. Useful while a tree is being written; dangerous to leave on, because a converged-looking estate is not converging.",
	},
	"renderer": {
		Group:  "Rendering and templates",
		Detail: "The pipeline each SLS goes through, as `jinja|yaml`. A per-file shebang overrides it. Add `gpg` — `jinja|yaml|gpg` — to decrypt pillar values in place.",
	},
	"undefined": {
		Group:  "Rendering and templates",
		Detail: "`strict` makes an undefined name an error; `permissive` renders it as empty, which is Jinja's default and is how a typo in a pillar key becomes a state that quietly does nothing. Prefer strict.",
	},
	"yaml_bool_11": {
		Group:  "Rendering and templates",
		Detail: "Resolves `yes`, `no`, `on`, `off`, `y`, and `n` as booleans, as PyYAML does. Off by default because YAML 1.2 does not, and a Norwegian country code of `no` should stay a string. Turn it on for a tree written against Salt that depends on the old behaviour.",
	},
	"template_trim_blocks": {
		Group:  "Rendering and templates",
		Detail: "Jinja's `trim_blocks`. Matches whatever the tree was written against; changing it changes whitespace in every rendered file.",
	},
	"template_lstrip_blocks": {
		Group:  "Rendering and templates",
		Detail: "Jinja's `lstrip_blocks`, with the same caution.",
	},
	"random_seed": {
		Group:  "Rendering and templates",
		Detail: "`deterministic` seeds template randomness per node and per template, so a highstate rendered twice is byte-identical and `--test` means something. `nondeterministic` restores Salt's behaviour, where a random value differs on every render and every run reports changes.",
	},
	"regex_engine": {
		Group:  "Rendering and templates",
		Detail: "`re2` is the only engine until the backtracking one of SPEC 10.4 ships. A pattern re2 cannot compile — a backreference, a lookaround — is refused by name rather than silently not matching.",
	},
	"gpg_binary": {
		Group:  "Rendering and templates",
		Detail: "The `gpg` executable the gpg renderer drives. Named rather than linked, so the estate gets its operating system's gpg patching.",
	},
	"gpg_home": {
		Group:  "Rendering and templates",
		Detail: "`GNUPGHOME` for decryption. Set it explicitly for a service account: relying on the environment's means the key that gets used depends on who started the process.",
	},
	"gpg_timeout": {
		Group:  "Rendering and templates",
		Detail: "Bounds one decryption. A gpg waiting on a pinentry that will never come is the failure this catches.",
	},
	"fileserver_backend": {
		Group:  "The file server",
		Detail: "An ordered list, searched in order: `roots`, `git`/`gitfs`, `s3`/`s3fs`. First match wins, so `roots` first lets a local file override what the repository serves.",
	},
	"fileserver_follow_symlinks": {
		Group:  "The file server",
		Detail: "Follows symlinks inside a served root. Never outside one — a link pointing out of the tree is refused whatever this says, because otherwise `salt://` reads the whole filesystem.",
	},
	"file_ignore_regex": {
		Group:  "The file server",
		Detail: "Paths the server hides, as regular expressions. Use it for the `.git` directory and for editor droppings, not as a security control: a path that must not be served should not be in the tree.",
	},
	"file_ignore_glob": {
		Group:  "The file server",
		Detail: "The same, as globs, which is usually the readable form.",
	},
	"gitfs_remotes": {
		Group:  "The git file server",
		Detail: "The repositories to serve. Each may be a URL or a mapping carrying its own root, base branch, and credentials. Cloned as a bare mirror and fetched on an interval.",
	},
	"gitfs_root": {
		Group:  "The git file server",
		Detail: "A subdirectory inside each repository to serve as the tree root, for a repository that holds the states under `salt/` alongside other things.",
	},
	"gitfs_ref_types": {
		Group:  "The git file server",
		Detail: "Whether branches, tags, or both become environments. Tags are immutable, which makes them the honest choice for a release; branches are what most estates use.",
	},
	"gitfs_keyring": {
		Group:  "The git file server",
		Detail: "The GnuPG home holding the keys a signed ref must be signed by. Verification with no keyring is refused rather than falling back to the hub user's own keyring, which would trust whatever that user happens to trust.",
	},
	"gitfs_update_interval": {
		Group:  "The git file server",
		Detail: "How often the hub fetches. 0 fetches only when something asks, which is right for a repository that is pushed rarely and wrong for one an operator expects to poll.",
	},
	"gitfs_cache_dir": {
		Group:  "The git file server",
		Detail: "Where the bare mirrors and the materialised trees live. Discardable; deleting it costs a refetch.",
	},
	"gitfs_env_allowlist": {
		Group:  "The git file server",
		Detail: "Which refs become environments. Set it when anyone can push a branch, or every feature branch becomes an environment the fleet can be pointed at.",
	},
	"gitfs_env_denylist": {
		Group:  "The git file server",
		Detail: "Applied after the allowlist.",
	},
	"gitfs_base": {
		Group:  "The git file server",
		Detail: "The branch that becomes the `base` environment: `main`, or whatever the repository calls its default branch.",
	},
	"gitfs_verify_signatures": {
		Group:  "The git file server",
		Detail: "A control rather than a log line: a ref whose tip is not signed by a key in the keyring is not served at all, rather than served with a warning. This is what makes the tree's provenance a property of the estate rather than of the repository host.",
	},
	"s3_buckets": {
		Group:  "The S3 file server",
		Detail: "The buckets to serve, as names or as mappings carrying a prefix, a region, and an environment. A prefix is how one bucket holds several environments.",
	},
	"s3_region": {
		Group:  "The S3 file server",
		Detail: "Used for any bucket that names none. Endpoints are derived from it and the partition rather than configured.",
	},
	"s3_partition": {
		Group:  "The S3 file server",
		Detail: "`aws`, `aws-us-gov`, or `aws-cn`. The endpoint hostname and the signing scope both come from this, so a GovCloud estate sets it and nothing else changes.",
	},
	"s3_endpoint": {
		Group:  "The S3 file server",
		Detail: "For MinIO, Ceph, or another S3-compatible service. Setting it replaces the derived endpoint entirely.",
	},
	"s3_path_style": {
		Group:  "The S3 file server",
		Detail: "Addresses the bucket in the path rather than the hostname. Most S3-compatible services need this; real S3 does not.",
	},
	"s3_dualstack": {
		Group:  "The S3 file server",
		Detail: "Uses the IPv6-capable endpoints.",
	},
	"s3_access_key_id": {
		Group:  "The S3 file server",
		Detail: "A static key. Prefer a role, an instance profile, or the environment: a key in a configuration file is a key in a backup, in a config management tree, and eventually in a git history.",
	},
	"s3_secret_access_key": {
		Group:  "The S3 file server",
		Detail: "The secret half. Prefer `s3_secret_access_key_file`, which keeps it out of the file everything else reads.",
	},
	"s3_secret_access_key_file": {
		Group:  "The S3 file server",
		Detail: "A file holding the secret, required to be mode 600. This is the form to use when a static credential is unavoidable.",
	},
	"s3_role_arn": {
		Group:  "The S3 file server",
		Detail: "A role to assume once the base credentials resolve, so the long-lived credential is only ever allowed to assume, and the credential that reads the bucket is short-lived.",
	},
	"s3_role_session": {
		Group:  "The S3 file server",
		Detail: "The session name the assumed role is recorded under, which is what appears in CloudTrail. Name it after the hub.",
	},
	"s3_web_identity_token_file": {
		Group:  "The S3 file server",
		Detail: "The projected token an EKS service account gets. With `s3_role_arn` it needs no other credential at all, which is the arrangement to prefer everywhere it is available.",
	},
	"s3_update_interval": {
		Group:  "The S3 file server",
		Detail: "How often the hub lists the buckets. 0 lists only on demand.",
	},
	"s3_cache_dir": {
		Group:  "The S3 file server",
		Detail: "Where fetched objects live. Discardable.",
	},
	"s3_env_allowlist": {
		Group:  "The S3 file server",
		Detail: "Which environments the bucket layout exposes.",
	},
	"s3_env_denylist": {
		Group:  "The S3 file server",
		Detail: "Applied after the allowlist.",
	},
	"relay": {
		Group:  "Relays",
		Detail: "Makes this hub a relay: it serves its own nodes and presents itself to an upstream hub as one connected client. Use one for a segment that cannot reach the main hub, or one whose returns must survive the link between them.",
	},
	"relay_upstream": {
		Group:  "Relays",
		Detail: "The hub this relay reports to. The relay enrols with it as an ordinary node first, and the upstream's policy must grant that node certificate the `relay.proxy` runner.",
	},
	"relay_upstream_port": {
		Group:  "Relays",
		Detail: "Only read when `relay_upstream` names no port.",
	},
	"relay_pki_dir": {
		Group:  "Relays",
		Detail: "The key material this relay enrolled with upstream, which is separate from the CA it issues its own nodes' certificates from. Two identities in two directories: a relay is a client above and an authority below.",
	},
	"relay_server_name": {
		Group:  "Relays",
		Detail: "The name to verify in the upstream's certificate, when the address dialled is not the name the certificate carries.",
	},
	"relay_spool_dir": {
		Group:  "Relays",
		Detail: "Where returns wait while the upstream is unreachable, drained oldest-first when it comes back. This is the durability that the syndic it replaces does not have: an outage delays returns rather than losing them.",
	},
	"relay_spool_max_size": {
		Group:  "Relays",
		Detail: "The ceiling on undelivered returns. Past it the relay refuses a new return rather than dropping the oldest, because the oldest is the one most likely to be the answer somebody is waiting for.",
	},
	"relay_event_tags": {
		Group:  "Relays",
		Detail: "Tag globs whose events are forwarded upstream. Empty forwards nothing, which is the safe default: forwarding everything is what floods a hub, and is why the syndic's all-or-nothing is worth replacing. `halite/job/**` is a reasonable start.",
	},
	"relay_max_depth": {
		Group:  "Relays",
		Detail: "How many relays a connection may already be behind. Capped at 2, because unbounded nesting is how a syndic estate becomes undebuggable.",
	},
	"relay_timeout": {
		Group:  "Relays",
		Detail: "Bounds one upstream request.",
	},
	"accept_relays": {
		Group:  "Relays",
		Detail: "Set on the hub relays report to, not on the relay. Off by default: a hub does not accept a relay's word about which nodes it proxies for unless it has been told to.",
	},
	"roster": {
		Group:  "Agentless mode",
		Detail: "Where the list of agentless targets comes from: `flat` reads a file, `sshconfig` reads the operator's own ssh config, `cache` uses nodes the hub already knows. `scan`, `cloud`, and `terraform` are named and refused.",
	},
	"roster_file": {
		Group:  "Agentless mode",
		Detail: "The roster for the `flat` backend.",
	},
	"ssh_binary": {
		Group:  "Agentless mode",
		Detail: "The halite-node binary pushed to the target and verified by digest before it runs. It is static and needs nothing on the far side — no Python, which is the whole difference from salt-ssh.",
	},
	"ssh_command": {
		Group:  "Agentless mode",
		Detail: "The ssh binary to connect with. The system one, so the estate's `ssh_config`, jump hosts, certificate authentication, and `known_hosts` all work without being reimplemented.",
	},
	"scp_command": {
		Group:  "Agentless mode",
		Detail: "The copier used to push the binary.",
	},
	"ssh_options": {
		Group:  "Agentless mode",
		Detail: "Extra `-o` settings passed to both ssh and scp, for anything the estate's config does not already carry.",
	},
	"ssh_timeout": {
		Group:  "Agentless mode",
		Detail: "Bounds one target, so a machine that accepts a connection and then stops answering does not hold the run open.",
	},
	"hash_type": {
		Group:  "Filesystem layout",
		Detail: "The digest used for file server manifests and change detection. `sha256` unless an estate has a reason; the weaker options are not offered.",
	},
	"grains": {
		Group:  "Grains and the mine",
		Detail: "Static grains set in the configuration file, merged last so they override collected ones. This is where an estate puts `role` and `datacentre` — facts about a machine that the machine cannot work out for itself.",
	},
	"grains_refresh_interval": {
		Group:  "Grains and the mine",
		Detail: "How often grains are re-collected and re-pushed. A machine whose facts change — a disk added, an address moved — is stale to targeting until this fires or `saltutil.refresh_grains` is run.",
	},
	"grain_stale_after": {
		Group:  "Grains and the mine",
		Detail: "Read by the hub, not the node: when cached grains are old enough to be annotated as stale during targeting, so a match against a node that has not reported in a week says so.",
	},
	"cloud_grains": {
		Group:  "Grains and the mine",
		Detail: "Collects instance metadata from the cloud provider. Opt-in because it costs a round trip to a link-local address at every collection, which on a machine that is not in a cloud is a timeout.",
	},
	"mine_functions": {
		Group:  "Grains and the mine",
		Detail: "What this node publishes for other nodes to read — addresses, versions, whatever a template on another machine needs. This is how a load balancer's tree learns its backends without an operator listing them.",
	},
	"mine_interval": {
		Group:  "Grains and the mine",
		Detail: "Publication interval, in minutes, matching Salt's units.",
	},
	"schedule": {
		Group:  "Scheduling and beacons",
		Detail: "Jobs the node starts on its own. This is what replaces the cron entry running `salt-call state.apply` on every machine: the schedule travels with the configuration, the run is recorded in the job cache like any other, and `maxrunning` stops two from overlapping.",
	},
	"timezone": {
		Group:  "Scheduling and beacons",
		Detail: "The IANA zone schedules are evaluated in. Set it explicitly on a fleet that spans zones, or `0 3 * * *` means a different moment on every machine. Each job may override it.",
	},
	"beacons": {
		Group:  "Scheduling and beacons",
		Detail: "Local conditions that become events on the hub's bus: a file changed, a service died, a disk filled. A beacon on its own does nothing; it is the reactor that turns the event into a job.",
	},
	"reactor": {
		Group:  "Reactors",
		Detail: "A mapping of event tag glob to the reaction SLS that runs when it matches. The pairing with beacons is the automation loop: something happens on a node, the hub notices, the hub acts.",
	},
	"reactor_workers": {
		Group:  "Reactors",
		Detail: "How many reactions may render and dispatch at once. Too few and a burst queues; too many and a burst becomes a thundering herd against the fleet.",
	},
	"reactor_queue_depth": {
		Group:  "Reactors",
		Detail: "How many events may wait. On overflow the oldest are dropped and the count is reported rather than the drop being silent, because a reactor that quietly stopped reacting is indistinguishable from one with nothing to do.",
	},
	"reactor_timeout": {
		Group:  "Reactors",
		Detail: "Bounds one reaction's render and dispatch.",
	},
	"max_causality_depth": {
		Group:  "Reactors",
		Detail: "How long a chain of cause and effect may grow before it is broken. This is the loop-breaker: a reaction that raises an event that matches its own trigger would otherwise run for ever.",
	},
	"extension_dir": {
		Group:  "Extensions",
		Detail: "Where verified extension bundles are cached. Nothing else should live in it; a writable directory inside it is a file the manifest does not list, and the store reports it as an unverified version.",
	},
	"extension_pins": {
		Group:  "Extensions",
		Detail: "Fixes each extension at a version and a Merkle root, so a bundle that changed underneath the estate fails to load rather than loading. This is the setting that makes an extension supply chain auditable.",
	},
	"extension_user": {
		Group:  "Extensions",
		Detail: "The account an extension drops to unless its manifest declares it needs root. Set it to something with no privileges of its own.",
	},
	"extension_group": {
		Group:  "Extensions",
		Detail: "Its group.",
	},
	"extension_timeout": {
		Group:  "Extensions",
		Detail: "Bounds one call into an extension. The process is killed with SIGTERM and then SIGKILL, so a wedged extension costs one job rather than the node.",
	},
	"extension_pool_size": {
		Group:  "Extensions",
		Detail: "How many processes one extension may have. A pool avoids paying process startup on every call; too large a pool on a small node is memory that the state run needed.",
	},
	"returner": {
		Group:  "Returners",
		Detail: "Where a node sends its answers besides the hub: `local`, `local_cache`, `file`, `syslog`, `webhook`, or `smtp`. A returner named here that this build does not have is not fatal — it fails every return with the reason instead, because a node that will not start cannot be sent the extension that would provide it.",
	},
	"returner_timeout": {
		Group:  "Returners",
		Detail: "Bounds one delivery.",
	},
	"event_return": {
		Group:  "Returners",
		Detail: "Ships the whole event stream to a returner, which SPEC 20.3 calls the recommended path to a SIEM. It resumes from a bus offset, so a receiver that was unreachable for an hour catches up rather than leaving an hour-shaped hole in the audit trail.",
	},
	"event_return_tags": {
		Group:  "Returners",
		Detail: "Tag globs to ship, comma-separated. Empty ships everything, which is usually more than a SIEM wants to be charged for.",
	},
	"event_return_batch": {
		Group:  "Returners",
		Detail: "Events read from the bus per shipment.",
	},
	"event_return_from": {
		Group:  "Returners",
		Detail: "Where to start on a first run: `latest`, `earliest`, or an explicit offset. `earliest` on a hub with months of history ships months of history.",
	},
	"returner_file": {
		Group:  "Returners",
		Detail: "The path the file returner appends to, as NDJSON.",
	},
	"returner_file_max_size": {
		Group:  "Returners",
		Detail: "Rotates past this many bytes; 0 never rotates, which fills a disk.",
	},
	"returner_file_keep": {
		Group:  "Returners",
		Detail: "How many rotated copies survive.",
	},
	"returner_syslog_address": {
		Group:  "Returners",
		Detail: "`host:port`, or empty for the local socket.",
	},
	"returner_syslog_network": {
		Group:  "Returners",
		Detail: "`tcp` or `udp`. UDP drops silently under load, which for an audit trail is the wrong trade.",
	},
	"returner_syslog_tag": {
		Group:  "Returners",
		Detail: "The RFC 5424 app-name the receiver filters on.",
	},
	"returner_syslog_facility": {
		Group:  "Returners",
		Detail: "The syslog facility.",
	},
	"returner_syslog_tls": {
		Group:  "Returners",
		Detail: "Wraps the connection in TLS. Syslog over the network without it is the estate's job history in plaintext.",
	},
	"returner_syslog_ca_file": {
		Group:  "Returners",
		Detail: "The CA to verify the receiver against. An internal CA is the common case, and this is how it is trusted without disabling verification.",
	},
	"returner_webhook_url": {
		Group:  "Returners",
		Detail: "An `https://` endpoint. There is no plaintext form.",
	},
	"returner_webhook_ca_file": {
		Group:  "Returners",
		Detail: "The CA to verify the receiver against, for an endpoint behind an estate's own CA.",
	},
	"returner_webhook_secret": {
		Group:  "Returners",
		Detail: "The HMAC-SHA-256 signing secret, so the receiver can tell a real delivery from anything else that found the URL. Prefer the file form.",
	},
	"returner_webhook_secret_file": {
		Group:  "Returners",
		Detail: "A file holding the secret, mode 600.",
	},
	"returner_webhook_attempts": {
		Group:  "Returners",
		Detail: "Delivery attempts before the return is spooled instead. The nonce is recorded after the delivery lands, not when the signature verifies, so a retry after a transient failure is not refused as a replay.",
	},
	"returner_spool_max_size": {
		Group:  "Returners",
		Detail: "The ceiling on undelivered returns held on disk.",
	},
	"returner_smtp_address": {
		Group:  "Returners",
		Detail: "`host:port` of the mail server.",
	},
	"returner_smtp_from": {
		Group:  "Returners",
		Detail: "The envelope sender.",
	},
	"returner_smtp_to": {
		Group:  "Returners",
		Detail: "Recipients, comma-separated.",
	},
	"returner_smtp_subject": {
		Group:  "Returners",
		Detail: "A fixed subject, or empty to describe the return.",
	},
	"returner_smtp_username": {
		Group:  "Returners",
		Detail: "Refused without `returner_smtp_tls`, because a password on a plaintext SMTP connection is a password on the wire.",
	},
	"returner_smtp_password": {
		Group:  "Returners",
		Detail: "The same, and the same refusal.",
	},
	"returner_smtp_tls": {
		Group:  "Returners",
		Detail: "Requires STARTTLS.",
	},
	"nodegroups": {
		Group:  "Targeting and the job cache",
		Detail: "Named compound expressions, so `halite-hub run webservers state.apply` means whatever the estate decided it means. A name is easier to review in a policy than the expression it stands for.",
	},
	"job_cache": {
		Group:  "Targeting and the job cache",
		Detail: "The backend holding job records and returns.",
	},
	"job_cache_retention": {
		Group:  "Targeting and the job cache",
		Detail: "How long a job's record is kept. This is the estate's audit trail of what ran and what answered, so keep it longer than the time it takes to notice a problem.",
	},
	"job_cache_max_size": {
		Group:  "Targeting and the job cache",
		Detail: "Retention by total size, whichever binds first.",
	},
	"node_data_cache": {
		Group:  "Targeting and the job cache",
		Detail: "Keeps each node's grains, pillar, and mine on the hub. Targeting on grains needs it: without it the hub has nothing to match against but the node's name.",
	},
	"event_retention": {
		Group:  "The event bus",
		Detail: "How long the bus keeps a record. Reactors read from it, `event listen` reads from it, and an event returner resumes from an offset in it, so this is also how long a receiver may be down without losing anything.",
	},
	"event_max_size": {
		Group:  "The event bus",
		Detail: "A ceiling on the whole bus, whichever binds first.",
	},
	"event_tag_compat": {
		Group:  "The event bus",
		Detail: "Additionally emits every event under its `salt/` equivalent, so a reactor or a consumer written against Salt's tags keeps matching during a migration. Doubles the volume; turn it off once nothing needs it.",
	},
	"policy": {
		Group:  "Authorization",
		Detail: "The RBAC file, deny by default. One file with one grammar, replacing Salt's `publisher_acl`, `external_auth`, `peer`, `peer_run`, and `client_acl`. Read by both the hub and the API, and `halite-hub policy test` evaluates a request against it without running anything.",
	},
	"accounts": {
		Group:  "Authorization",
		Detail: "Local accounts for break-glass and automation identities, not the primary operator path. An absent file is an empty set rather than an error, so an estate on OIDC alone needs none.",
	},
	"legacy_acl": {
		Group:  "Authorization",
		Detail: "Salt ACL keys the migration shim preserved verbatim for review rather than translating, because translating them silently would produce an authorization file nobody had read. Convert them into `policy` and delete this.",
	},
	"token_lifetime": {
		Group:  "The API service",
		Detail: "How long a token issued at login is good for. The roles are frozen into it at issue, so a role granted later does not widen a token already in someone's hands — which also means a role taken away is a reason to revoke rather than something that takes effect on its own.",
	},
	"token_idle": {
		Group:  "The API service",
		Detail: "How long a token may go unused before it stops, independently of its lifetime.",
	},
	"token_retention": {
		Group:  "The API service",
		Detail: "How long an expired token's record is kept for the audit. Pruned on an interval rather than at every read.",
	},
	"max_body": {
		Group:  "The API service",
		Detail: "The largest request body the service will read, so a client cannot make it run out of memory.",
	},
	"tls_cert": {
		Group:  "The API service",
		Detail: "The certificate the API presents to its own clients — browsers and scripts — which is a different certificate from the operator one it presents to the hub.",
	},
	"tls_key": {
		Group:  "The API service",
		Detail: "Its key.",
	},
	"api_operator": {
		Group:  "The API service",
		Detail: "Which operator certificate the service presents to the hub. The API is a client of the hub, deliberately, so compromising it yields one certificate bounded by one policy rather than the control plane. Grant it less than the sum of its operators and it gets exactly that.",
	},
	"hooks": {
		Group:  "The API service",
		Detail: "Webhook ingress paths. Every one declares an authentication method; there is no unauthenticated hook, and a hook configuration that will not parse stops the service rather than serving some of them.",
	},
	"metrics": {
		Group:  "The API service",
		Detail: "Records and exposes Prometheus metrics at `/v1/metrics`. On by default, because a backpressure design is only auditable if the counters were there before anyone needed them.",
	},
	"ldap_address": {
		Group:  "LDAP and Active Directory",
		Detail: "`host:port` of the directory. Empty disables LDAP entirely; a login naming it is then refused by name rather than falling through to local accounts.",
	},
	"ldap_tls": {
		Group:  "LDAP and Active Directory",
		Detail: "`ldaps` or `starttls`. There is no plaintext mode: a bind is a password on the wire.",
	},
	"ldap_ca_file": {
		Group:  "LDAP and Active Directory",
		Detail: "The CA to verify the directory against. An internal CA is the common case for a directory, and this is how it is trusted without disabling verification.",
	},
	"ldap_server_name": {
		Group:  "LDAP and Active Directory",
		Detail: "The name to verify in the directory's certificate when it differs from the address dialled.",
	},
	"ldap_bind_dn": {
		Group:  "LDAP and Active Directory",
		Detail: "The service account this client searches with. It needs to read users and groups and nothing else.",
	},
	"ldap_bind_password": {
		Group:  "LDAP and Active Directory",
		Detail: "Its password. Prefer the file form.",
	},
	"ldap_bind_password_file": {
		Group:  "LDAP and Active Directory",
		Detail: "A file holding the bind password, mode 600.",
	},
	"ldap_user_base_dn": {
		Group:  "LDAP and Active Directory",
		Detail: "The subtree operators are looked for in.",
	},
	"ldap_user_filter": {
		Group:  "LDAP and Active Directory",
		Detail: "How they are looked for; `%s` is the username, escaped by this client rather than interpolated raw, so a username containing filter syntax cannot rewrite the query.",
	},
	"ldap_member_of_attribute": {
		Group:  "LDAP and Active Directory",
		Detail: "The attribute on a user entry listing their groups — `memberOf` in Active Directory. Faster than searching groups, when the directory maintains it.",
	},
	"ldap_group_base_dn": {
		Group:  "LDAP and Active Directory",
		Detail: "Where groups are searched for, when the directory has no `memberOf`.",
	},
	"ldap_group_filter": {
		Group:  "LDAP and Active Directory",
		Detail: "The group search; `%s` is the escaped user DN.",
	},
	"ldap_group_attribute": {
		Group:  "LDAP and Active Directory",
		Detail: "The attribute on a group entry holding the name that `ldap_role_map` matches.",
	},
	"ldap_nested_depth": {
		Group:  "LDAP and Active Directory",
		Detail: "How far to follow a group's own memberships. Nested groups are how an estate's real structure is usually expressed, and unbounded following is how one lookup becomes hundreds.",
	},
	"ldap_principal_attribute": {
		Group:  "LDAP and Active Directory",
		Detail: "The attribute naming the operator in the audit trail. Empty uses the username they typed.",
	},
	"ldap_role_map": {
		Group:  "LDAP and Active Directory",
		Detail: "Maps a directory group to role names in the policy. The roles have to exist there or they grant nothing; group membership never appears in the policy as a principal.",
	},
	"ldap_timeout": {
		Group:  "LDAP and Active Directory",
		Detail: "Bounds one directory operation, so a directory that stops answering fails a login rather than hanging it.",
	},
	"oidc_issuer": {
		Group:  "OpenID Connect",
		Detail: "The provider's issuer URL. Empty disables OIDC, and a login naming it is refused by name.",
	},
	"oidc_client_id": {
		Group:  "OpenID Connect",
		Detail: "This service's client id at the provider.",
	},
	"oidc_client_secret": {
		Group:  "OpenID Connect",
		Detail: "The client secret. Prefer the file form.",
	},
	"oidc_client_secret_file": {
		Group:  "OpenID Connect",
		Detail: "A file holding the client secret, mode 600.",
	},
	"oidc_audience": {
		Group:  "OpenID Connect",
		Detail: "The audience tokens must carry. Empty takes the client id, which is what most providers issue.",
	},
	"oidc_redirect_url": {
		Group:  "OpenID Connect",
		Detail: "Where the provider sends an operator back to, and it has to match what is registered at the provider exactly.",
	},
	"oidc_scopes": {
		Group:  "OpenID Connect",
		Detail: "Extra scopes, comma-separated. `openid` is always sent; a groups claim usually needs one more.",
	},
	"oidc_groups_claim": {
		Group:  "OpenID Connect",
		Detail: "A colon-delimited path to the claim holding an operator's groups, because providers nest it differently.",
	},
	"oidc_principal_claim": {
		Group:  "OpenID Connect",
		Detail: "Which claim names the operator in the audit trail. Prefer an immutable one over an email address, which people change.",
	},
	"oidc_role_map": {
		Group:  "OpenID Connect",
		Detail: "Maps a provider group to role names in the policy, the same way the LDAP one does.",
	},
	"oidc_ca_file": {
		Group:  "OpenID Connect",
		Detail: "The CA to verify the provider against, for a provider inside the estate.",
	},
	"oidc_skew": {
		Group:  "OpenID Connect",
		Detail: "How much clock difference is tolerated on a token's `exp` and `nbf`. Small, and not a substitute for NTP.",
	},
	"quiesce": {
		Group:  "Node execution controls",
		Detail: "Refuses every job but the allowlist, so a machine can be taken out of automation without being taken off the network. Salt calls this blackout.",
	},
	"quiesce_allowlist": {
		Group:  "Node execution controls",
		Detail: "What is still permitted while quiesced. Keep `test.ping` and the grains readers in it, or a quiesced node looks like a dead one.",
	},
	"parallel_jobs": {
		Group:  "Node execution controls",
		Detail: "Lets jobs run alongside one another by default. Off by default, because two state runs converging the same machine at once is how a machine ends up in neither state.",
	},
	"job_queue_depth": {
		Group:  "Node execution controls",
		Detail: "How many jobs may wait before the node refuses more. Refusing is the honest answer: the alternative is a queue that grows until the node dies with a backlog nobody can see.",
	},
	"require_job_signature": {
		Group:  "Node execution controls",
		Detail: "Refuses a job without a valid detached operator signature. Not built; the setting is named and refused rather than accepted and ignored.",
	},
	"job_signer_keys": {
		Group:  "Node execution controls",
		Detail: "The public keys whose detached job signatures this node would accept.",
	},
	"extension_trust_keys": {
		Group:  "Extensions",
		Detail: "The keys whose signed bundles this node accepts, as `<name> <base64>`. An extension is code, so this is the same decision as trusting a package repository.",
	},
	"extension_require_signature": {
		Group:  "Extensions",
		Detail: "On by default. False permits an unsigned bundle for development and warns on every load, so a development setting cannot quietly become the production one.",
	},
	"exec_path": {
		Group:  "Node execution controls",
		Detail: "SPEC 25.4 asks that a spawned process get an explicit PATH, and without this it gets whatever started the program — rc.d, systemd, and an operator's shell each hand over a different one, so a state that works when you run it by hand fails under the service. Set to the whole search path, colon-separated; it replaces rather than extends. It applies to the program itself as well as to what it spawns, so `cmd.run`, the package providers, and the hub's git, gpg, and ssh all resolve binaries the same way. Empty inherits the environment's, falling back to a built-in list when there is none.",
	},
	"cmd_default_shell": {
		Group:  "Node execution controls",
		Detail: "Runs `cmd.run` through a shell by default, as Salt does. Off here, because an argument vector cannot be reinterpreted by anything; turn it on for a tree that depends on shell syntax it never quoted.",
	},
	"legacy_arg_parse": {
		Group:  "Node execution controls",
		Detail: "Restores Salt's YAML coercion of command line arguments, where `1.10` becomes a number and a version string is corrupted. On for a tree that relies on the coercion; off is correct.",
	},
	"log_level": {
		Group:  "Logging and diagnostics",
		Detail: "`error`, `warn`, `info`, `debug`, or `trace`. `info` records what happened; `debug` records why.",
	},
	"log_level_file": {
		Group:  "Logging and diagnostics",
		Detail: "A separate level for the file sink, so an estate can keep `info` on the console and `debug` on disk.",
	},
	"log_file": {
		Group:  "Logging and diagnostics",
		Detail: "Empty logs to stderr, which under a service manager means the journal. Set it when there is no journal to log to.",
	},
	"log_format": {
		Group:  "Logging and diagnostics",
		Detail: "`json` for anything that ships logs, `console` for a person reading them.",
	},
	"tracing": {
		Group:  "Logging and diagnostics",
		Detail: "`off` or `otlp`. Named and refused; distributed tracing is SPEC 26.3 and is not built.",
	},
}
