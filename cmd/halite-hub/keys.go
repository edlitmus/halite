package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
)

var keysUsage = `halite-hub keys — enrollment and the key lifecycle (SPEC section 7.4)

  keys list [--state <state>]      every node the hub has heard from
  keys show <node>                 one node's record in full
  keys fingerprint <node>          the digest to compare out of band
  keys accept <node>... | --all    issue to a pending request
  keys reject <node>... --reason   refuse a request, and keep it
  keys revoke <node>... --reason   withdraw an issued certificate
  keys delete <node>...            forget a node entirely
  keys export-crl [--out <path>]   sign a revocation list
  keys token create --ttl <dur>    mint a bootstrap token
  keys token list                  every token, and what it admitted
  keys token revoke <id>           withdraw a token
  keys operator create <name>      issue a certificate for an operator

operator create flags:
  --out <path>         where to write the certificate and key, default
                       <pki_dir>/operator-<name>.crt and .key
  --lifetime <dur>     default 720h
  --admin              also write a bootstrap policy granting this
                       operator everything, if there is no policy yet

keys flags:
  --state <state>      list only pending, accepted, rejected, revoked, or expired
  --all                accept every pending request
  --reason <text>      why a request was rejected or a certificate revoked
  --out <format>       summary (default) or json
  --help               describe these commands without running one

token create flags:
  --ttl <duration>     mandatory, at most 24h
  --nodes <glob>       node IDs the token may admit, default any
  --cidr <prefix>      source addresses the token may be presented from
  --uses <n>           default 1, which is single-use
  --comment <text>     what it is for, for the audit
`

func runKeys(args *cli.Args) int {
	if len(args.Positional) == 0 {
		fmt.Fprint(os.Stderr, keysUsage)
		return 2
	}
	switch args.Positional[0] {
	case "help", "--help", "-h":
		fmt.Print(keysUsage)
		return 0
	case "token":
		return runKeysToken(args)
	case "operator":
		return runKeysOperator(args)
	}

	h := openHub(args, false)
	rest := args.Positional[1:]

	switch args.Positional[0] {
	case "list":
		return keysList(h, args)
	case "show":
		return keysShow(h, args, rest)
	case "fingerprint":
		return keysFingerprint(h, rest)
	case "accept":
		return keysAccept(h, args, rest)
	case "reject":
		return keysDecide(h, rest, args.Flag("reason", ""), h.auth.Reject, "rejected")
	case "revoke":
		return keysDecide(h, rest, args.Flag("reason", ""), h.auth.Revoke, "revoked")
	case "delete":
		return keysDelete(h, rest)
	case "export-crl":
		return keysExportCRL(h, args)
	default:
		fmt.Fprintf(os.Stderr, "halite-hub keys: unknown subcommand %q\n\n%s", args.Positional[0], keysUsage)
		return 2
	}
}

// keyRow is one line of `keys list`, and the JSON shape of it. The
// certificate itself is not here: `show` prints that.
type keyRow struct {
	NodeID      string `json:"node_id"`
	State       string `json:"state"`
	Source      string `json:"source,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Serial      string `json:"serial,omitempty"`
	NotAfter    string `json:"not_after,omitempty"`
	Reason      string `json:"reason,omitempty"`
	FirstSeen   string `json:"first_seen"`
}

func rowOf(rec *keystore.Record, now time.Time) keyRow {
	row := keyRow{
		NodeID:      rec.NodeID,
		State:       string(rec.Status(now)),
		Source:      string(rec.Source),
		Fingerprint: rec.Fingerprint,
		Serial:      rec.Serial,
		Reason:      rec.Reason,
		FirstSeen:   rec.FirstSeen.UTC().Format(time.RFC3339),
	}
	if !rec.NotAfter.IsZero() {
		row.NotAfter = rec.NotAfter.UTC().Format(time.RFC3339)
	}
	return row
}

func keysList(h *hubContext, args *cli.Args) int {
	records, err := h.store.List()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	now := time.Now()
	want := args.Flag("state", "")

	var rows []keyRow
	for _, rec := range records {
		row := rowOf(rec, now)
		if want != "" && row.State != want {
			continue
		}
		rows = append(rows, row)
	}

	if args.Flag("out", "summary") != "summary" {
		return writeOut(args, rows)
	}
	if len(rows) == 0 {
		fmt.Println("no nodes have asked to enrol")
		return 0
	}
	// Grouped by state, because the question is always "what is
	// waiting for me".
	byState := map[string][]keyRow{}
	for _, row := range rows {
		byState[row.State] = append(byState[row.State], row)
	}
	for _, state := range keystore.States {
		group := byState[string(state)]
		if len(group) == 0 {
			continue
		}
		fmt.Printf("%s (%d)\n", state, len(group))
		sort.Slice(group, func(i, j int) bool { return group[i].NodeID < group[j].NodeID })
		for _, row := range group {
			fmt.Printf("  %-40s %s\n", row.NodeID, row.Fingerprint)
			if row.Reason != "" {
				fmt.Printf("  %-40s   %s\n", "", row.Reason)
			}
		}
	}
	return 0
}

func keysShow(h *hubContext, args *cli.Args, names []string) int {
	if len(names) == 0 {
		cli.Fatalf("show needs a node")
	}
	rec, err := h.store.Get(names[0])
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if args.Flag("out", "summary") != "summary" {
		return writeOut(args, rowOf(rec, time.Now()))
	}
	fmt.Printf("node        %s\n", rec.NodeID)
	fmt.Printf("state       %s\n", rec.Status(time.Now()))
	fmt.Printf("source      %s\n", rec.Source)
	fmt.Printf("fingerprint %s\n", rec.Fingerprint)
	if rec.TokenID != "" {
		fmt.Printf("token       %s\n", rec.TokenID)
	}
	if rec.Serial != "" {
		fmt.Printf("serial      %s\n", rec.Serial)
		fmt.Printf("expires     %s\n", rec.NotAfter.UTC().Format(time.RFC3339))
	}
	if rec.Reason != "" {
		fmt.Printf("reason      %s\n", rec.Reason)
	}
	fmt.Printf("first seen  %s\n", rec.FirstSeen.UTC().Format(time.RFC3339))
	if rec.Cert != "" {
		fmt.Printf("\n%s", rec.Cert)
	}
	return 0
}

// keysFingerprint prints one digest and nothing else, so that it can be
// compared by a script or pasted into a ticket.
func keysFingerprint(h *hubContext, names []string) int {
	if len(names) == 0 {
		// With no node, the CA's own fingerprint: this is what a node
		// pins with hub_fingerprint.
		fingerprint, err := pki.FingerprintCert(h.auth.CA.Cert)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Println(fingerprint)
		return 0
	}
	rec, err := h.store.Get(names[0])
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Println(rec.Fingerprint)
	return 0
}

func keysAccept(h *hubContext, args *cli.Args, names []string) int {
	if args.Bool("all", false) {
		records, err := h.store.List()
		if err != nil {
			cli.Fatalf("%v", err)
		}
		now := time.Now()
		for _, rec := range records {
			if rec.Status(now) == keystore.Pending {
				names = append(names, rec.NodeID)
			}
		}
		if len(names) == 0 {
			fmt.Println("nothing is pending")
			return 0
		}
	}
	if len(names) == 0 {
		cli.Fatalf("accept needs a node, or --all")
	}
	failed := 0
	for _, name := range names {
		rec, err := h.auth.Accept(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			failed++
			continue
		}
		fmt.Printf("accepted %s (%s), expires %s\n",
			rec.NodeID, rec.Fingerprint, rec.NotAfter.UTC().Format(time.RFC3339))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func keysDecide(h *hubContext, names []string, reason string, decide func(string, string) (*keystore.Record, error), verb string) int {
	if len(names) == 0 {
		cli.Fatalf("%s needs a node", verb)
	}
	failed := 0
	for _, name := range names {
		if _, err := decide(name, reason); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			failed++
			continue
		}
		fmt.Printf("%s %s\n", verb, name)
	}
	if verb == "revoked" && failed < len(names) {
		// The denylist that matters lives in the serving process, and
		// this is not it. Saying so is better than an operator
		// believing the connection is already gone.
		fmt.Printf("a running hub applies this within a few seconds; the record on disk is the decision\n")
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func keysDelete(h *hubContext, names []string) int {
	if len(names) == 0 {
		cli.Fatalf("delete needs a node")
	}
	failed := 0
	for _, name := range names {
		// A delete of an accepted node revokes first, or the
		// certificate stays valid with nothing recording that it
		// exists -- which is the one way to lose a credential
		// completely.
		if rec, err := h.store.Get(name); err == nil && rec.State == keystore.Accepted {
			if _, err := h.auth.Revoke(name, "deleted"); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
				failed++
				continue
			}
			fmt.Printf("revoked %s before deleting it\n", name)
		}
		if err := h.store.Delete(name); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			failed++
			continue
		}
		fmt.Printf("deleted %s\n", name)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func keysExportCRL(h *hubContext, args *cli.Args) int {
	records, err := h.store.List()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	var revoked []pki.RevokedCert
	for _, rec := range records {
		if rec.State == keystore.Revoked && rec.Serial != "" {
			revoked = append(revoked, pki.RevokedCert{Serial: rec.Serial, At: rec.Updated})
		}
	}
	// The number is the count of revocations, which rises whenever the
	// list changes and never falls: a consumer can tell an older list
	// from a newer one.
	crl, err := h.auth.CA.CRL(revoked, int64(len(revoked)+1), 7*24*time.Hour)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	out := args.Flag("out", "")
	if out == "" || out == "summary" || out == "-" {
		os.Stdout.Write(crl)
		return 0
	}
	if err := os.WriteFile(out, crl, 0o644); err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Printf("wrote %d revocation(s) to %s\n", len(revoked), out)
	return 0
}

func runKeysToken(args *cli.Args) int {
	if len(args.Positional) < 2 {
		fmt.Fprint(os.Stderr, keysUsage)
		return 2
	}
	h := openHub(args, false)
	rest := args.Positional[2:]

	switch args.Positional[1] {
	case "create":
		ttl := args.Flag("ttl", "")
		if ttl == "" {
			cli.Fatalf("a bootstrap token needs --ttl; the maximum is %s (SPEC 7.3)", keystore.MaxTokenTTL)
		}
		d, err := time.ParseDuration(ttl)
		if err != nil {
			cli.Fatalf("--ttl %q: %v", ttl, err)
		}
		uses := 1
		fmt.Sscanf(args.Flag("uses", "1"), "%d", &uses)
		tok, secret, err := h.store.MintToken(keystore.TokenOptions{
			TTL:      d,
			NodeGlob: args.Flag("nodes", ""),
			CIDR:     args.Flag("cidr", ""),
			MaxUses:  uses,
			Comment:  args.Flag("comment", ""),
		}, time.Now())
		if err != nil {
			cli.Fatalf("%v", err)
		}
		// Printed once. The hub keeps a digest, so there is no second
		// chance and saying so is part of the output.
		fmt.Printf("token   %s\n", secret)
		fmt.Printf("id      %s\n", tok.ID)
		fmt.Printf("expires %s\n", tok.Expires.UTC().Format(time.RFC3339))
		fmt.Printf("uses    %d\n", tok.MaxUses)
		if tok.NodeGlob != "" {
			fmt.Printf("nodes   %s\n", tok.NodeGlob)
		}
		if tok.CIDR != "" {
			fmt.Printf("from    %s\n", tok.CIDR)
		}
		fmt.Fprintln(os.Stderr, "\nThe hub stores only a digest: this is the only time the token is shown.")
		return 0

	case "list":
		tokens, err := h.store.ListTokens()
		if err != nil {
			cli.Fatalf("%v", err)
		}
		if len(tokens) == 0 {
			fmt.Println("no bootstrap tokens")
			return 0
		}
		now := time.Now()
		for _, tok := range tokens {
			status := fmt.Sprintf("live, %d of %d uses", tok.Uses, tok.MaxUses)
			if why := tok.Why(now); why != "" {
				status = why
			}
			fmt.Printf("%s  %-24s expires %s\n",
				tok.ID, status, tok.Expires.UTC().Format(time.RFC3339))
			if tok.NodeGlob != "" || tok.CIDR != "" {
				fmt.Printf("            scope %s %s\n", orAny(tok.NodeGlob), orAny(tok.CIDR))
			}
			if len(tok.SpentBy) > 0 {
				fmt.Printf("            admitted %s\n", strings.Join(tok.SpentBy, ", "))
			}
			if tok.Comment != "" {
				fmt.Printf("            %s\n", tok.Comment)
			}
		}
		return 0

	case "revoke":
		if len(rest) == 0 {
			cli.Fatalf("token revoke needs a token id, which `keys token list` prints")
		}
		for _, id := range rest {
			if err := h.store.RevokeToken(id); err != nil {
				cli.Fatalf("%v", err)
			}
			fmt.Printf("revoked token %s\n", id)
		}
		return 0

	default:
		fmt.Fprintf(os.Stderr, "halite-hub keys token: unknown subcommand %q\n\n%s", args.Positional[1], keysUsage)
		return 2
	}
}

// runKeysOperator issues the certificate that `halite-hub run` presents.
//
// An operator is a peer like any other: SPEC 23.5 binds a role to
// `cert:CN=<name>`, so the certificate is the credential and there is
// no password, no shared secret, and no "trusted because it is running
// on the hub".
func runKeysOperator(args *cli.Args) int {
	if len(args.Positional) < 2 || args.Positional[1] != "create" {
		fmt.Fprint(os.Stderr, keysUsage)
		return 2
	}
	if len(args.Positional) < 3 {
		cli.Fatalf("operator create needs a name; it becomes the RBAC principal cert:CN=<name>")
	}
	name := args.Positional[2]
	h := openHub(args, false)

	lifetime, err := time.ParseDuration(args.Flag("lifetime", "720h"))
	if err != nil {
		cli.Fatalf("--lifetime %q: %v", args.Flag("lifetime", ""), err)
	}
	alg, err := pki.ParseKeyAlgorithm(h.cfg.String("key_algorithm", string(pki.ECDSAP256)))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	key, err := pki.GenerateKey(alg)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	der, err := h.auth.CA.IssueOperator(key, name, lifetime)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	base := args.Flag("out", "")
	if base == "" {
		base = h.files.Path("operator-" + name)
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	// The key first and 0600, because a certificate written beside a
	// key that failed to write is a credential nobody can use and
	// everybody can see.
	if err := os.WriteFile(base+".key", keyPEM, 0o600); err != nil {
		cli.Fatalf("%v", err)
	}
	if err := os.WriteFile(base+".crt", pki.EncodeCert(der), 0o644); err != nil {
		cli.Fatalf("%v", err)
	}
	cert, err := pki.DecodeCert(pki.EncodeCert(der))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if args.Bool("admin", false) {
		path, err := bootstrapAdmin(h.cfg, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		} else {
			fmt.Printf("policy    %s (administrator)\n", path)
		}
	}
	fmt.Printf("operator  %s\n", name)
	fmt.Printf("principal %s\n", pki.Principal(name))
	fmt.Printf("cert      %s\n", base+".crt")
	fmt.Printf("key       %s\n", base+".key")
	fmt.Printf("expires   %s\n", cert.NotAfter.UTC().Format(time.RFC3339))
	return 0
}

func orAny(s string) string {
	if s == "" {
		return "any"
	}
	return s
}

func writeOut(args *cli.Args, v any) int {
	format, err := cli.ParseFormat(args.Flag("out", "json"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	indent := 0
	fmt.Sscanf(args.Flag("indent", "2"), "%d", &indent)
	if err := cli.Write(os.Stdout, v, format, indent); err != nil {
		cli.Fatalf("%v", err)
	}
	return 0
}
