package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/compat"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/pillar"
)

// Salt's own defaults. `halite parse` exists to read a tree that was
// written for Salt, so it looks where Salt looks unless told otherwise.
const (
	saltStateRoot  = "/srv/salt"
	saltPillarRoot = "/srv/pillar"
)

// cmdParse reports whether an existing state and pillar tree can be used by
// halite as written, and what has to change where it cannot.
func cmdParse(args []string) {
	fs := flag.NewFlagSet("parse", flag.ExitOnError)
	rootFlag := fs.String("root", "", "state tree root to check (default "+saltStateRoot+")")
	pillarRootFlag := fs.String("pillar-root", "", "pillar tree root to check (default: a 'pillar' directory beside the state root, i.e. "+saltPillarRoot+")")
	asJSON := fs.Bool("json", false, "output the report as JSON")
	errorsOnly := fs.Bool("errors", false, "report only findings that halite will not run as written")
	positional := parseFlags(fs, args)
	if len(positional) > 1 {
		fatal("usage: halite parse [-root DIR] [-pillar-root DIR] [PATH]")
	}

	stateRoot, pillarRoot, pillarOptional := parseRoots(positional, *rootFlag, *pillarRootFlag)

	g := grains.Collect()
	// Rendering needs whatever pillar halite itself can read: a tree that
	// already works must come back clean, and one that does not is reported
	// on its own terms below.
	p, _ := (&pillar.Loader{Root: pillarRoot, Grains: g}).Load()
	scanner := &compat.Scanner{Grains: g, Pillar: p}

	report := compat.Report{Grains: g}
	if st, err := os.Stat(stateRoot); err == nil && !st.IsDir() {
		// A single file is checked against the tree it names, so its
		// includes still resolve; without one, against its own directory.
		root := *rootFlag
		if root == "" {
			root = filepath.Dir(stateRoot)
		}
		report.Trees = append(report.Trees, compat.TreeReport{
			Root:  root,
			Kind:  compat.KindState.String(),
			Files: []compat.FileReport{scanner.ScanFile(stateRoot, root, compat.KindState)},
		})
	} else {
		stateTree, err := scanner.ScanTree(stateRoot, compat.KindState)
		if err != nil {
			fatal("state root %s: %v\n(pass -root DIR, or an sls file, to check something else)", stateRoot, err)
		}
		report.Trees = append(report.Trees, stateTree)
	}

	if pillarRoot != "" {
		pillarTree, err := scanner.ScanTree(pillarRoot, compat.KindPillar)
		switch {
		case err != nil && pillarOptional:
			fmt.Fprintf(os.Stderr, "pillar root %s not read: %v\n", pillarRoot, err)
		case err != nil:
			fatal("pillar root: %v", err)
		default:
			report.Trees = append(report.Trees, pillarTree)
		}
	}

	if *asJSON {
		printParseJSON(report)
	} else {
		printParseReport(report, *errorsOnly)
	}
	if report.Totals().Errors > 0 {
		os.Exit(1)
	}
}

// parseRoots resolves what to scan: a positional path wins, then the flags,
// then Salt's defaults. A default pillar root that is not there is skipped
// rather than reported as a failure.
func parseRoots(positional []string, rootFlag, pillarFlag string) (stateRoot, pillarRoot string, pillarOptional bool) {
	stateRoot, pillarRoot = rootFlag, pillarFlag
	if len(positional) == 1 {
		stateRoot = positional[0]
	} else if stateRoot == "" {
		stateRoot = saltStateRoot
	}
	if pillarRoot == "" {
		if len(positional) == 1 {
			return stateRoot, "", true // an explicit path scans only that path
		}
		// The same convention `halite apply` uses: a pillar tree sits beside
		// the state tree, which for Salt's own layout is /srv/pillar.
		return stateRoot, filepath.Join(filepath.Dir(stateRoot), "pillar"), true
	}
	return stateRoot, pillarRoot, false
}

func printParseJSON(report compat.Report) {
	out := map[string]any{
		"trees":   report.Trees,
		"modules": report.ModuleUsage(),
		"summary": report.Totals(),
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func printParseReport(report compat.Report, errorsOnly bool) {
	fmt.Printf("halite %s — compatibility report\n", version)
	fmt.Printf("host: id=%v os=%v os_family=%v\n\n", report.Grains["id"], report.Grains["os"], report.Grains["os_family"])

	for _, tree := range report.Trees {
		fmt.Printf("%s tree: %s (%s)\n", tree.Kind, tree.Root, plural(len(tree.Files), "sls file"))
		for _, f := range tree.Findings {
			if errorsOnly && f.Severity != compat.SevError {
				continue
			}
			fmt.Printf("\n  %s\n", f.File)
			printFindings("  ", []compat.Finding{f})
		}
		for _, file := range tree.Files {
			printFileReport(file, errorsOnly)
		}
		fmt.Println()
	}

	printModuleUsage(report)
	printSummary(report)
}

func printFileReport(file compat.FileReport, errorsOnly bool) {
	shown := file.Findings
	if errorsOnly {
		shown = onlyErrors(shown)
	}
	if len(shown) == 0 {
		return
	}
	suffix := ""
	if file.Approximate {
		suffix = "  (read with its templates stripped: the findings below are approximate)"
	}
	fmt.Printf("\n  %s%s\n", file.Path, suffix)
	printFindings("  ", shown)
}

func printFindings(indent string, findings []compat.Finding) {
	for _, f := range findings {
		where := ""
		if f.Line > 0 {
			where = fmt.Sprintf("line %d", f.Line)
		}
		fmt.Printf("%s  %-7s %-8s %s (%s)\n", indent, strings.ToUpper(string(f.Severity)), where, f.Message, f.Code)
		if f.Hint != "" {
			fmt.Printf("%s          %s→ %s\n", indent, strings.Repeat(" ", 8), f.Hint)
		}
	}
}

func printModuleUsage(report compat.Report) {
	uses := report.ModuleUsage()
	if len(uses) == 0 {
		return
	}
	fmt.Println("Module usage")
	fmt.Println("------------")
	for _, u := range uses {
		status := "ok"
		switch {
		case u.External:
			status = "ok (external module)"
		case !u.Supported:
			status = "UNSUPPORTED"
		}
		fmt.Printf("  %-24s %4d  %s\n", u.Name, u.Count, status)
	}
	fmt.Println()
}

func printSummary(report compat.Report) {
	t := report.Totals()
	fmt.Println("Summary")
	fmt.Println("------------")
	fmt.Printf("Files checked: %d (%d with findings, %d only partly readable)\n", t.Files, t.WithFinding, t.Approximate)
	fmt.Printf("Errors:        %d (halite will not run these as written)\n", t.Errors)
	fmt.Printf("Warnings:      %d (halite loads them and ignores something)\n", t.Warnings)
	fmt.Printf("Notes:         %d\n", t.Notes)
	fmt.Println("------------")
	if t.Errors == 0 && t.Warnings == 0 {
		fmt.Println("This tree runs under halite as written.")
		return
	}
	fmt.Println("See docs/migration.md for the translation each finding needs.")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func onlyErrors(findings []compat.Finding) []compat.Finding {
	var out []compat.Finding
	for _, f := range findings {
		if f.Severity == compat.SevError {
			out = append(out, f)
		}
	}
	return out
}
