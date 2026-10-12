// Command handbook builds, checks and generates the halite Handbook.
//
//	handbook check            hold docs/handbook to its rules
//	handbook gen              write the generated chapters and the contents
//	handbook build -out DIR   write the HTML edition
//
// See internal/handbook for why it exists and what it refuses.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/handbook"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: handbook check|build [flags]")
		os.Exit(2)
	}
	sub, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	root := fs.String("root", ".", "the repository root")
	out := fs.String("out", "", "build: the directory to write")
	version := fs.String("version", "", "build: the version to stamp")
	commit := fs.String("commit", "", "build: the commit to stamp")
	fs.Parse(args)

	bk, err := handbook.ReadContents(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "handbook:", err)
		os.Exit(1)
	}
	switch sub {
	case "gen":
		files, err := handbook.Generate(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "handbook:", err)
			os.Exit(1)
		}
		for _, name := range sortedNames(files) {
			if err := os.WriteFile(filepath.Join(*root, name), []byte(files[name]), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "handbook:", err)
				os.Exit(1)
			}
			fmt.Println("wrote", name)
		}
		// The contents page lists chapters by their titles, so it is made
		// from the book as it now is, generated chapters included.
		bk2, err := handbook.ReadContents(*root)
		if err == nil {
			err = bk2.Load(nil, false)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "handbook:", err)
			os.Exit(1)
		}
		readme := filepath.Join("docs", "handbook", "README.md")
		if err := os.WriteFile(filepath.Join(*root, readme), []byte(handbook.Contents(bk2)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "handbook:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", readme)
	case "check":
		ps := handbook.Check(bk)
		for _, p := range ps {
			fmt.Fprintln(os.Stderr, p)
		}
		if len(ps) > 0 {
			os.Exit(1)
		}
		fmt.Printf("%d chapters ok\n", len(bk.All))
	case "build":
		if *out == "" {
			fmt.Fprintln(os.Stderr, "handbook: build needs -out")
			os.Exit(2)
		}
		known, err := handbook.KnownIdentifiers(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "handbook:", err)
			os.Exit(1)
		}
		names, err := handbook.Build(bk, handbook.SiteOptions{Out: *out, Version: *version, Commit: *commit,
			RepoURL: repoURL(*version), Known: known})
		if err != nil {
			fmt.Fprintln(os.Stderr, "handbook:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %d files to %s\n", len(names), *out)
	default:
		fmt.Fprintln(os.Stderr, "handbook: unknown command", sub)
		os.Exit(2)
	}
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// repoURL is where a link to a file outside the book points: the tag of the
// release being built, or the main branch for a build that is not one. A
// release's handbook then links to the source that release was built from.
func repoURL(version string) string {
	ref := "main"
	if strings.HasPrefix(version, "v") || (version != "" && version[0] >= '0' && version[0] <= '9' && !strings.Contains(version, "-")) {
		ref = "v" + strings.TrimPrefix(version, "v")
	}
	return "https://github.com/edlitmus/halite/blob/" + ref + "/"
}
