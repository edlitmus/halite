package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/hub"
)

var evidenceUsage = `halite-hub evidence — what nodes have reported of their evidence (SPEC section 25.7)

  evidence anchors <node>      the heads a node reported, as the hub stored them

The output is the hub's file for that node, byte for byte: one JSON
object per line. Give it to ` + "`halite-node verify-evidence --anchors`" + ` on
the node, or keep it beside a copy of the node's chain.
`

// anchorDir is where the hub keeps the anchors, for serve and for this
// command alike, so the two cannot disagree about where to look.
func anchorDir(cfg *config.Config) string {
	return filepath.Join(cfg.String("state_dir", config.DefaultStateDir), "evidence-anchors")
}

// runEvidence is `halite-hub evidence`.
//
// It reads the file rather than asking the running hub, for the reason
// `jobs` does: the record is the hub's own and this runs on the hub, and
// the moment somebody wants it is as likely as not one where the hub has
// been stopped to be looked at.
func runEvidence(args *cli.Args) int {
	if len(args.Positional) == 0 {
		fmt.Fprint(os.Stderr, evidenceUsage)
		return cli.ExitUsage
	}
	switch args.Positional[0] {
	case "help":
		fmt.Print(evidenceUsage)
		return 0
	case "anchors":
		// The operand is checked before the configuration is read, and
		// nothing is created either way: this command reads, and a
		// directory it made would be a directory owned by whoever ran
		// it. See runJobs.
		if len(args.Positional) < 2 {
			cli.Usagef("evidence anchors needs a node")
		}
		if len(args.Positional) > 2 {
			cli.Usagef("evidence anchors takes one node")
		}
		return evidenceAnchors(args, args.Positional[1])
	default:
		cli.Usagef("evidence has no subcommand %q; it has anchors", args.Positional[0])
	}
	return cli.ExitUsage
}

func evidenceAnchors(args *cli.Args, nodeID string) int {
	cfg, err := config.Load(config.Hub, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         args.Flag("root", config.DefaultRoot),
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	path, err := hub.AnchorPath(anchorDir(cfg), nodeID)
	if err != nil {
		cli.Usagef("%v", err)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		cli.Fatalf("the hub has no evidence anchors for %s (looked for %s); "+
			"a node reports its head when it connects and after every job", nodeID, path)
	}
	if err != nil {
		cli.Fatalf("%v", err)
	}
	defer f.Close()
	// Copied rather than parsed and printed: the investigator is to get
	// what the hub wrote, including a line this build could not read,
	// and not this build's opinion of it.
	if _, err := io.Copy(os.Stdout, f); err != nil {
		cli.Fatalf("%v", err)
	}
	return 0
}
