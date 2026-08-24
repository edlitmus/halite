package main

import (
	"fmt"
	"os"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/value"
	"path/filepath"
)

// runToken is `halite-api token <list|show|revoke|prune>`: what an
// operator uses to see who holds authority and to take it away.
func runToken(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("token needs a subcommand; there are list, show, revoke, and prune")
	}
	s := setup(args)
	tokens, err := apitoken.Open(
		filepath.Join(s.cfg.String("state_dir", config.DefaultStateDir), "tokens"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	rest := args.Positional[1:]

	switch args.Positional[0] {
	case "list":
		return tokenList(args, tokens)
	case "show":
		if len(rest) == 0 {
			cli.Fatalf("token show needs a token identifier")
		}
		return tokenShow(args, tokens, rest[0])
	case "revoke":
		return tokenRevoke(args, tokens, rest)
	case "prune":
		keep := s.cfg.Duration("token_retention", 30*24*time.Hour)
		n, err := tokens.Prune(keep)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Printf("pruned %d expired token(s) older than %s\n", n, keep)
		return 0
	}
	cli.Fatalf("token has no subcommand %q; there are list, show, revoke, and prune",
		args.Positional[0])
	return 2
}

func tokenList(args *cli.Args, tokens *apitoken.Store) int {
	all, err := tokens.List()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	limit := 20
	fmt.Sscanf(args.Flag("limit", "20"), "%d", &limit)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	format := args.Flag("out", "nested")
	if format != "nested" {
		out := value.NewMap(len(all))
		for _, t := range all {
			out.Set(t.ID, tokenMap(t, time.Now()))
		}
		writeOut(args, out)
		return 0
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no tokens have been issued")
		return 0
	}
	now := time.Now()
	for _, t := range all {
		state := "live"
		if why := t.Why(now); why != "" {
			state = why
		}
		fmt.Printf("%-32s  %-24s  %-10s  %s\n",
			t.ID, t.Principal, state, t.Expires.UTC().Format(time.RFC3339))
	}
	return 0
}

func tokenShow(args *cli.Args, tokens *apitoken.Store, id string) int {
	t, err := tokens.Get(id)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	writeOut(args, tokenMap(t, time.Now()))
	return 0
}

// tokenRevoke withdraws one token, or every token a principal holds.
func tokenRevoke(args *cli.Args, tokens *apitoken.Store, rest []string) int {
	if principal := args.Flag("principal", ""); principal != "" {
		n, err := tokens.RevokePrincipal(principal)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Printf("revoked %d token(s) held by %s\n", n, principal)
		return 0
	}
	if len(rest) == 0 {
		cli.Fatalf("token revoke needs an identifier, or --principal <name>")
	}
	for _, id := range rest {
		t, err := tokens.Revoke(id)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		fmt.Printf("revoked %s, held by %s\n", t.ID, t.Principal)
	}
	return 0
}

// tokenMap renders a token. The digest is not in it: it is not a
// credential, but it is the one field that would let a stolen listing
// be checked against a guessed secret.
func tokenMap(t *apitoken.Token, now time.Time) *value.Map {
	out := value.NewMap(9)
	out.Set("id", t.ID)
	out.Set("principal", t.Principal)
	if len(t.Roles) > 0 {
		roles := make([]any, len(t.Roles))
		for i, r := range t.Roles {
			roles[i] = r
		}
		out.Set("roles", roles)
	}
	out.Set("issued", t.Issued.UTC().Format(time.RFC3339))
	out.Set("expires", t.Expires.UTC().Format(time.RFC3339))
	if t.IdleFor > 0 {
		out.Set("idle_for", t.IdleFor.String())
		out.Set("idle_expires", t.LastUsed.Add(t.IdleFor).UTC().Format(time.RFC3339))
	}
	if t.SourceCIDR != "" {
		out.Set("source_cidr", t.SourceCIDR)
	}
	if why := t.Why(now); why != "" {
		out.Set("usable", false)
		out.Set("why", why)
	} else {
		out.Set("usable", true)
	}
	return out
}

func writeOut(args *cli.Args, v any) {
	format, err := cli.ParseFormat(args.Flag("out", "nested"))
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if err := cli.Write(os.Stdout, v, format, 2); err != nil {
		cli.Fatalf("%v", err)
	}
}
