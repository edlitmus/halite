package config

import "sort"

// Key describes one configuration setting: which roles accept it, what it
// means, and what it defaults to.
//
// The table is the single source of truth for three things that otherwise
// drift apart: the loader's "is this key recognised" check, the generated
// documentation, and the migration report's key-by-key translation.
type Key struct {
	Name    string
	Roles   []Role
	Default string
	Doc     string
	// Section names the part of SPEC that defines the setting.
	Section string
}

func (k Key) appliesTo(r Role) bool {
	for _, role := range k.Roles {
		if role == r {
			return true
		}
	}
	return false
}

var (
	all      = []Role{Node, Hub, API}
	nodeOnly = []Role{Node}
	hubOnly  = []Role{Hub}
	nodeHub  = []Role{Node, Hub}
	hubAPI   = []Role{Hub, API}
	apiOnly  = []Role{API}
	nodeAPI  = []Role{Node, API}
)

// Keys is the recognised configuration surface. It grows with each
// delivery phase; a key that is not here is reported as unrecognised
// rather than silently ignored.
var Keys = []Key{
	// Identity and transport.
	{"node_id", nodeOnly, "", "This node's identity. Resolution order is in SPEC section 7.2.", "7.2"},
	{"node_id_source", nodeOnly, "auto", "Where the node ID comes from: auto, config, env, file, cloud, fqdn, hostname.", "7.2"},
	{"node_id_caching", nodeOnly, "true", "Pin the resolved node ID at first enrollment.", "7.2"},
	{"node_id_lowercase", nodeOnly, "false", "Lowercase the resolved node ID.", "7.2"},
	{"node_id_remove_domain", nodeOnly, "false", "Strip the domain from a resolved FQDN.", "7.2"},
	{"hub", nodeAPI, "", "The hub to dial. A node and the API dial the hub; the hub never dials either.", "5.1"},
	{"hub_port", nodeOnly, "4510", "The hub's TCP port.", "6.1"},
	{"hub_fingerprint", nodeOnly, "", "The hub CA's fingerprint. Required to enrol; the node fetches the CA and checks it against this.", "7.3"},
	{"hub_ca_file", nodeOnly, "", "A hub CA you deliver yourself, instead of the one the node would fetch. --ca-file overrides it.", "7.3"},
	{"hub_alive_interval", nodeOnly, "30s", "Ping interval on the subscribe stream.", "6.2"},
	{"hub_tries", nodeOnly, "0", "Reconnect attempts before giving up; 0 means retry forever.", "6.2"},
	{"hub_type", nodeOnly, "static", "static or failover, selecting how a list of hubs is used.", "6.2"},
	{"listen", hubAPI, ":4510", "Listen address.", "6.1"},
	{"pki_dir", all, DefaultPKIDir, "Key material.", "27.3"},
	{"cache_dir", all, DefaultCacheDir, "Discardable cache.", "27.3"},
	{"state_dir", all, DefaultStateDir, "Durable state: job cache, events, evidence.", "27.3"},
	{"socket_dir", all, DefaultSocketDir, "Sockets and PID files.", "27.3"},
	{"config_file", all, "", "The primary configuration file, set by the loader.", "27.3"},

	// Enrollment.
	{"enrollment_mode", hubOnly, "manual", "manual or token. `attested` is named and refused; it is not built. There is no auto_accept.", "7.3"},
	{"certificate_lifetime", hubOnly, "2160h", "Issued certificate lifetime; renewal happens at half of it.", "7.4"},
	{"key_algorithm", hubOnly, "ecdsa-p256", "ecdsa-p256, ecdsa-p384, rsa-3072, rsa-4096, or ed25519 in non-FIPS builds.", "7.1"},

	// State and pillar.
	{"file_roots", nodeHub, "", "Environment to an ordered list of state directories.", "13.1"},
	{"pillar_roots", nodeHub, "", "Environment to an ordered list of pillar directories.", "12.2"},
	{"env", all, "base", "The default environment. saltenv is a permanent alias.", "13.1"},
	{"pillarenv", nodeHub, "", "The pillar environment, defaulting to env.", "12.2"},
	{"env_allowlist", nodeHub, "", "Environments a run may use.", "28.3"},
	{"env_denylist", nodeHub, "", "Environments a run may not use.", "28.3"},
	{"top_file_merging_strategy", nodeHub, "merge", "merge, same, or merge_all.", "11.2"},
	{"pillar_source_merging_strategy", nodeHub, "smart", "smart, recurse, aggregate, or overwrite.", "12.3"},
	{"pillar_merge_lists", nodeHub, "false", "Concatenate lists when merging pillar sources.", "12.3"},
	{"pillar_trusted_grains", nodeHub, "", "Grains a node may use to target pillar. Custom grains are excluded by default.", "12.4"},
	{"pillar_cache_disk", nodeOnly, "false", "Cache pillar on the node's disk, encrypted at rest.", "12.8"},
	{"ext_pillar", hubOnly, "", "External pillar sources.", "12.7"},
	{"ext_pillar_fail", hubOnly, "hard", "hard or ignore. A partial pillar is worse than no pillar.", "12.7"},
	{"state_allowlist", nodeHub, "", "SLS names a state run may include.", "28.3"},
	{"state_denylist", nodeHub, "", "SLS names a state run may not include.", "28.3"},
	{"startup_states", nodeOnly, "", "What to run when the node starts: highstate, sls, or top.", "20.1"},
	{"failhard", nodeHub, "false", "Abort a state run on the first failure.", "11.4"},
	{"test", nodeHub, "false", "Run every state in test mode by default.", "11.6"},

	// Renderers.
	{"renderer", nodeHub, "jinja|yaml", "The default renderer pipeline.", "10"},
	{"undefined", nodeHub, "strict", "strict or permissive name resolution in templates.", "10.2.6"},
	{"yaml_bool_11", nodeHub, "true", "Resolve yes, no, on, off, y, and n as booleans, as PyYAML does.", "10.1.3"},
	{"template_trim_blocks", nodeHub, "false", "Jinja trim_blocks.", "10.2.1"},
	{"template_lstrip_blocks", nodeHub, "false", "Jinja lstrip_blocks.", "10.2.1"},
	{"random_seed", nodeHub, "deterministic", "deterministic or nondeterministic template randomness.", "10.2.4"},
	{"regex_engine", nodeHub, "re2", "re2 only until the backtracking engine of SPEC section 10.4 ships.", "10.4"},

	// File server.
	{"fileserver_backend", hubOnly, "roots", "Ordered list of file server backends.", "13.2"},
	{"fileserver_follow_symlinks", hubOnly, "false", "Follow symlinks inside a served root. Never outside it.", "13.5"},
	{"file_ignore_regex", hubOnly, "", "Paths the file server hides.", "13.5"},
	{"file_ignore_glob", hubOnly, "", "Paths the file server hides.", "13.5"},
	{"relay", hubOnly, "false", "Run as a relay: serve nodes and proxy them to an upstream hub.", "5.3"},
	{"relay_spool_dir", hubOnly, "", "Where returns wait during an upstream outage; empty is <state_dir>/relay-spool.", "5.3"},
	{"relay_spool_max_size", hubOnly, "536870912", "Bytes of undelivered returns to hold before refusing.", "5.3"},
	{"relay_event_tags", hubOnly, "", "Tag globs whose events are forwarded upstream; empty forwards none.", "5.3"},
	{"relay_max_depth", hubOnly, "2", "How many relays a connection may be behind.", "5.3"},
	{"relay_pki_dir", hubOnly, "", "Key material this relay enrolled with its upstream.", "5.3"},
	{"relay_server_name", hubOnly, "", "Name to verify in the upstream's certificate.", "5.3"},
	{"relay_timeout", hubOnly, "60s", "How long one upstream request may take.", "5.3"},
	{"roster", hubOnly, "flat", "Agentless roster backend: flat, sshconfig, cache, or ansible.", "21.2"},
	{"roster_file", hubOnly, "", "The roster; empty is <root>/roster.", "21.2"},
	{"ssh_binary", hubOnly, "", "The halite-node binary agentless mode pushes.", "21.1"},
	{"ssh_command", hubOnly, "ssh", "The ssh binary to connect with.", "21.1"},
	{"scp_command", hubOnly, "scp", "The copier used to push the binary.", "21.1"},
	{"ssh_options", hubOnly, "", "Extra -o settings passed to ssh and scp.", "21.1"},
	{"ssh_timeout", hubOnly, "5m", "How long one agentless target may take.", "21.1"},
	{"s3_buckets", hubOnly, "", "S3 buckets the file server serves.", "13.4"},
	{"s3_region", hubOnly, "us-east-1", "Default region for buckets that name none.", "13.4"},
	{"s3_partition", hubOnly, "aws", "aws, aws-us-gov, or aws-cn. Endpoints are built from it.", "13.4"},
	{"s3_endpoint", hubOnly, "", "Custom endpoint, for an S3-compatible service.", "13.4"},
	{"s3_path_style", hubOnly, "false", "Address the bucket in the path rather than the host.", "13.4"},
	{"s3_dualstack", hubOnly, "false", "Use the IPv6-capable endpoints.", "13.4"},
	{"s3_access_key_id", hubOnly, "", "Access key; prefer a role or the environment.", "13.4"},
	{"s3_secret_access_key", hubOnly, "", "Secret key; prefer the file form.", "13.4"},
	{"s3_secret_access_key_file", hubOnly, "", "File holding the secret key, mode 600.", "13.4"},
	{"s3_role_arn", hubOnly, "", "Role to assume after the base credentials resolve.", "13.4"},
	{"s3_role_session", hubOnly, "halite", "Session name for the assumed role.", "13.4"},
	{"s3_web_identity_token_file", hubOnly, "", "IRSA token file; with s3_role_arn it needs no other credential.", "13.4"},
	{"s3_update_interval", hubOnly, "5m", "How often the hub lists; 0 lists only on demand.", "13.4"},
	{"s3_cache_dir", hubOnly, "", "Where fetched objects live; empty is <cache_dir>/s3fs.", "13.4"},
	{"s3_env_allowlist", hubOnly, "", "Environments the file server exposes.", "13.4"},
	{"s3_env_denylist", hubOnly, "", "Environments the file server refuses to expose.", "13.4"},
	{"gitfs_remotes", hubOnly, "", "Git repositories the file server serves.", "13.3"},
	{"gitfs_root", hubOnly, "", "Subdirectory inside each repository to serve.", "13.3"},
	{"gitfs_ref_types", hubOnly, "branches", "What becomes an environment: branches, tags, or both.", "13.3"},
	{"gitfs_keyring", hubOnly, "", "GnuPG home holding the keys a signed ref must be signed by.", "13.3"},
	{"gitfs_update_interval", hubOnly, "5m", "How often the hub fetches; 0 fetches only on demand.", "13.3"},
	{"gitfs_cache_dir", hubOnly, "", "Where mirrors live; empty is <cache_dir>/gitfs.", "13.3"},
	{"gitfs_env_allowlist", hubOnly, "", "Git refs the file server exposes as environments.", "13.3"},
	{"gitfs_env_denylist", hubOnly, "", "Git refs the file server refuses to expose.", "13.3"},
	{"gitfs_base", hubOnly, "main", "The branch that becomes the base environment.", "13.3"},
	{"gitfs_verify_signatures", hubOnly, "false", "Serve a ref only if its tip carries a trusted signature.", "13.3"},
	{"hash_type", all, "sha256", "sha256, sha384, sha512, or sha3-256.", "13.5"},

	// Grains, mine, scheduler, beacons, reactor.
	{"grains", nodeOnly, "", "Static grains merged last, so they can override.", "14.2"},
	{"grains_refresh_interval", nodeOnly, "30m", "How often grains are re-collected.", "8.3"},
	{"grain_stale_after", hubOnly, "1h", "When cached grains are annotated as stale during targeting.", "8.3"},
	{"cloud_grains", nodeOnly, "false", "Collect cloud metadata grains. Opt-in, because it costs a round trip.", "14.1"},
	{"mine_functions", nodeOnly, "", "What this node publishes to the mine.", "19.5"},
	{"mine_interval", nodeOnly, "60", "Mine publication interval in minutes.", "19.5"},
	{"schedule", nodeOnly, "", "Scheduled jobs.", "20.1"},
	{"timezone", nodeOnly, "<the node's local zone>", "The time zone schedules evaluate in, as an IANA name. Each job may override it.", "20.1"},
	{"beacons", nodeOnly, "", "Beacon configuration.", "16.1"},
	{"reactor", hubOnly, "", "Event tag globs to reaction SLS.", "18.1"},
	{"reactor_workers", hubOnly, "2 x NumCPU", "Reactor worker pool size.", "18.2"},
	{"reactor_queue_depth", hubOnly, "10000", "Reactor queue depth. On overflow the oldest are dropped and the count is reported.", "18.2"},
	{"reactor_timeout", hubOnly, "60s", "How long one reaction may take to render and dispatch.", "18.2"},
	{"max_causality_depth", hubOnly, "5", "How long a reactor causality chain may grow before it is broken.", "16.3"},
	{"extension_dir", nodeHub, "", "Extension cache; empty is <state_dir>/ext.", "24.4"},
	{"extension_pins", nodeHub, "", "Fixes each extension by version and Merkle root.", "24.4"},
	{"extension_user", nodeHub, "", "Account an extension drops to unless it declares root.", "24.3"},
	{"extension_group", nodeHub, "", "Its group.", "24.3"},
	{"extension_timeout", nodeHub, "60s", "How long one extension call may take.", "24.2"},
	{"extension_pool_size", nodeHub, "4", "Processes one extension may have.", "24.2"},
	{"returner", nodeOnly, "local", "Default returner: local, local_cache, file, syslog, webhook, or smtp.", "20.3"},
	{"returner_timeout", nodeHub, "30s", "How long one delivery may take.", "20.3"},
	{"event_return", hubOnly, "", "Ship the whole event stream to this returner.", "20.3"},
	{"event_return_tags", hubOnly, "", "Tag globs to ship, comma-separated; empty ships everything.", "20.3"},
	{"event_return_batch", hubOnly, "200", "Events read from the bus per shipment.", "20.3"},
	{"event_return_from", hubOnly, "latest", "Where to start on a first run: latest, earliest, or an offset.", "20.3"},
	{"returner_file", nodeHub, "", "Path for the file returner; empty is <state_dir>/returns.ndjson.", "20.3"},
	{"returner_file_max_size", nodeHub, "0", "Rotate the file returner past this many bytes; 0 never rotates.", "20.3"},
	{"returner_file_keep", nodeHub, "5", "How many rotated copies to keep.", "20.3"},
	{"returner_syslog_address", nodeHub, "", "host:port for syslog; empty uses the local socket.", "20.3"},
	{"returner_syslog_network", nodeHub, "tcp", "tcp or udp, for a syslog address.", "20.3"},
	{"returner_syslog_tag", nodeHub, "halite", "The RFC 5424 app-name.", "20.3"},
	{"returner_syslog_facility", nodeHub, "daemon", "The syslog facility.", "20.3"},
	{"returner_syslog_tls", nodeHub, "false", "Wrap the syslog connection in TLS.", "20.3"},
	{"returner_syslog_ca_file", nodeHub, "", "CA to verify the syslog receiver against.", "20.3"},
	{"returner_webhook_url", nodeHub, "", "https:// endpoint for the webhook returner.", "20.3"},
	{"returner_webhook_ca_file", nodeHub, "", "CA to verify the webhook receiver against.", "20.3"},
	{"returner_webhook_secret", nodeHub, "", "HMAC-SHA-256 signing secret; prefer the file form.", "20.3"},
	{"returner_webhook_secret_file", nodeHub, "", "File holding the signing secret, mode 600.", "20.3"},
	{"returner_webhook_attempts", nodeHub, "5", "Delivery attempts before a return is spooled.", "20.3"},
	{"returner_spool_max_size", nodeHub, "268435456", "Bytes of undelivered returns to hold before refusing.", "20.3"},
	{"returner_smtp_address", nodeHub, "", "host:port of the mail server.", "20.3"},
	{"returner_smtp_from", nodeHub, "", "Envelope sender.", "20.3"},
	{"returner_smtp_to", nodeHub, "", "Recipients, comma-separated.", "20.3"},
	{"returner_smtp_subject", nodeHub, "", "Fixed subject; empty describes the return.", "20.3"},
	{"returner_smtp_username", nodeHub, "", "SMTP account; refused without tls.", "20.3"},
	{"returner_smtp_password", nodeHub, "", "SMTP password; refused without tls.", "20.3"},
	{"returner_smtp_tls", nodeHub, "true", "Require STARTTLS.", "20.3"},
	{"nodegroups", hubOnly, "", "Named compound target expressions.", "8.1"},

	// Jobs and policy.
	{"job_cache", hubOnly, "local", "Job cache backend.", "9.4"},
	{"job_cache_retention", hubOnly, "720h", "Job cache retention by age.", "9.4"},
	{"job_cache_max_size", hubOnly, "10GiB", "Job cache retention by total size.", "9.4"},
	{"policy", hubAPI, DefaultPolicy, "The RBAC policy file. Deny by default.", "23.5"},

	// The HTTP API.
	{"accounts", apiOnly, "<config root>/accounts.yaml", "The local account file for break-glass and automation identities.", "23.2"},
	{"token_lifetime", apiOnly, "12h", "How long a token issued at login is good for.", "23.6"},
	{"token_idle", apiOnly, "4h", "How long a token may go unused before it stops.", "23.6"},
	{"token_retention", apiOnly, "720h", "How long an expired token's record is kept for the audit.", "23.6"},
	{"max_body", apiOnly, "64MiB", "The largest request body this service will read.", "22.3"},
	{"tls_cert", apiOnly, "", "The certificate this service presents to its own clients.", "22.3"},
	{"tls_key", apiOnly, "", "Its key.", "22.3"},
	{"api_operator", apiOnly, "api", "Which operator certificate this service presents to the hub.", "22"},
	{"hooks", apiOnly, "", "Webhook ingress paths. Every one declares an authentication method; there is no unauthenticated hook.", "22.2"},
	{"legacy_acl", hubOnly, "", "Salt ACL keys the shim preserved for review rather than translating.", "28.3"},
	{"quiesce", nodeOnly, "false", "Refuse jobs other than the allowlist. Salt calls this blackout.", "2.1"},
	{"quiesce_allowlist", nodeOnly, "", "Functions still permitted while quiesced.", "2.1"},

	// Relay.
	{"relay_upstream", hubOnly, "", "The hub this relay reports to.", "5.3"},
	{"relay_upstream_port", hubOnly, "4510", "The upstream hub's port.", "5.3"},
	{"accept_relays", hubOnly, "false", "Accept connections from relays as well as nodes.", "5.3"},

	// Observability.
	{"log_level", all, "info", "error, warn, info, debug, or trace.", "26.1"},
	{"log_level_file", all, "", "Level for the file sink, defaulting to log_level.", "26.1"},
	{"log_file", all, "", "Log file; empty logs to stderr or the journal.", "26.1"},
	{"log_format", all, "json", "json or console.", "26.1"},
	{"tracing", all, "off", "off or otlp.", "26.3"},

	// Node execution.
	{"node_data_cache", hubOnly, "true", "Keep per-node grains, pillar, and mine on the hub.", "28.3"},
	{"parallel_jobs", nodeOnly, "false", "Allow jobs to run alongside one another by default.", "9.6"},
	{"job_queue_depth", nodeOnly, "100", "How many jobs may wait before the node refuses more.", "9.6"},
	{"require_job_signature", nodeOnly, "false", "Refuse a job without a valid detached operator signature.", "25.6"},
	{"job_signer_keys", nodeOnly, "", "Public keys whose detached job signatures this node accepts.", "25.6"},
	{"extension_trust_keys", nodeHub, "", "Keys whose signed extension bundles this node accepts, as `<name> <base64>`.", "24.4"},
	{"extension_require_signature", nodeHub, "true", "Refuse an unsigned extension. False is for development and warns on every load.", "24.4"},
	{"exec_path", nodeHub, "", "PATH for this program and every process it spawns. Empty inherits the environment's.", "25.4"},
	{"cmd_default_shell", nodeOnly, "false", "Run cmd.run through a shell by default, as Salt does.", "15.2"},
	{"legacy_arg_parse", nodeHub, "false", "Restore Salt's YAML coercion of command line arguments.", "9.2"},
	{"event_tag_compat", hubOnly, "false", "Additionally emit every event under its salt/ equivalent.", "17.1"},
	{"metrics", all, "true", "Record Prometheus metrics. The hub and the API expose them at /v1/metrics; a node writes them where metrics_textfile says.", "26.2"},
	{"metrics_textfile", nodeOnly, "", "Where a node writes its exposition, for node_exporter's textfile collector. Empty means a node records nothing.", "26.2"},
	{"metrics_interval", nodeOnly, "60s", "How often a node rewrites metrics_textfile.", "26.2"},
	{"ldap_address", apiOnly, "", "Directory host:port; empty disables LDAP.", "23.3"},
	{"ldap_tls", apiOnly, "ldaps", "ldaps or starttls. There is no plaintext mode.", "23.3"},
	{"ldap_ca_file", apiOnly, "", "CA to verify the directory against.", "23.3"},
	{"ldap_server_name", apiOnly, "", "Name to verify in the directory's certificate.", "23.3"},
	{"ldap_bind_dn", apiOnly, "", "Service account this client searches with.", "23.3"},
	{"ldap_bind_password", apiOnly, "", "Its password; prefer the file form.", "23.3"},
	{"ldap_bind_password_file", apiOnly, "", "File holding the bind password, mode 600.", "23.3"},
	{"ldap_user_base_dn", apiOnly, "", "Where operators are looked for.", "23.3"},
	{"ldap_user_filter", apiOnly, "(uid=%s)", "How they are looked for; %s is the escaped username.", "23.3"},
	{"ldap_member_of_attribute", apiOnly, "memberOf", "Attribute on a user entry listing their groups.", "23.3"},
	{"ldap_group_base_dn", apiOnly, "", "Where groups are searched for.", "23.3"},
	{"ldap_group_filter", apiOnly, "", "Group search; %s is the escaped user DN.", "23.3"},
	{"ldap_group_attribute", apiOnly, "cn", "Attribute on a group entry holding its name.", "23.3"},
	{"ldap_nested_depth", apiOnly, "0", "How far to follow a group's own memberships.", "23.3"},
	{"ldap_principal_attribute", apiOnly, "", "Attribute naming the operator; empty uses the username.", "23.3"},
	{"ldap_role_map", apiOnly, "", "Maps a directory group to roles in the policy.", "23.3"},
	{"ldap_timeout", apiOnly, "10s", "How long one directory operation may take.", "23.3"},
	{"oidc_issuer", apiOnly, "", "OpenID Connect issuer URL; empty disables OIDC.", "23.4"},
	{"oidc_client_id", apiOnly, "", "This service's client id at the provider.", "23.4"},
	{"oidc_client_secret", apiOnly, "", "Client secret; prefer the file form.", "23.4"},
	{"oidc_client_secret_file", apiOnly, "", "File holding the client secret, mode 600.", "23.4"},
	{"oidc_audience", apiOnly, "", "Audience the tokens must carry; empty takes the client id.", "23.4"},
	{"oidc_redirect_url", apiOnly, "", "Where the provider sends an operator back to.", "23.4"},
	{"oidc_scopes", apiOnly, "", "Extra scopes to request, comma-separated; openid is always sent.", "23.4"},
	{"oidc_groups_claim", apiOnly, "groups", "Colon-delimited path to the claim holding an operator's groups.", "23.4"},
	{"oidc_principal_claim", apiOnly, "sub", "Which claim names the operator.", "23.4"},
	{"oidc_role_map", apiOnly, "", "Maps a provider group to roles in the policy.", "23.4"},
	{"oidc_ca_file", apiOnly, "", "CA to verify the identity provider against.", "23.4"},
	{"oidc_skew", apiOnly, "60s", "Clock difference tolerated on a token's exp and nbf.", "23.4"},
	{"event_retention", hubOnly, "720h", "How long the event bus keeps a record.", "17.2"},
	{"event_max_size", hubOnly, "4294967296", "Ceiling on the whole event bus, whichever binds first.", "17.2"},

	// GPG pillar compatibility.
	{"gpg_binary", nodeHub, "gpg", "The gpg binary the gpg renderer drives.", "12.6"},
	{"gpg_home", nodeHub, "", "GNUPGHOME for the gpg renderer. Empty uses the environment's.", "12.6"},
	{"gpg_timeout", nodeHub, "30s", "How long one decryption may take.", "12.6"},
}

var keyIndex = func() map[string][]Role {
	m := make(map[string][]Role, len(Keys))
	for _, k := range Keys {
		m[k.Name] = k.Roles
	}
	return m
}()

// IsKnownKey reports whether a role recognises a configuration key.
func IsKnownKey(role Role, name string) bool {
	roles, ok := keyIndex[name]
	if !ok {
		return false
	}
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// KeysFor lists the keys a role accepts, sorted, for documentation and for
// `doctor`.
func KeysFor(role Role) []Key {
	var out []Key
	for _, k := range Keys {
		if k.appliesTo(role) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
