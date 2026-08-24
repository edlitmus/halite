package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/value"
)

// runAccount is `halite-api account <hash|list>`.
func runAccount(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("account needs a subcommand; there are hash and list")
	}
	switch args.Positional[0] {
	case "hash":
		return accountHash(args)
	case "list":
		return accountList(args)
	}
	cli.Fatalf("account has no subcommand %q; there are hash and list", args.Positional[0])
	return 2
}

// accountHash produces the verifier to paste into the account file.
//
// It prints the hash and never the password, and it reads the password
// from standard input rather than from an argument: an argument reaches
// the process table and the shell history.
func accountHash(args *cli.Args) int {
	iterations := account.DefaultIterations
	if v := args.Flag("iterations", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			cli.Fatalf("--iterations %q is not a number", v)
		}
		iterations = n
	}

	password, err := readPassword()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if password == "" {
		cli.Fatalf("an empty password is not one")
	}
	hash, err := account.Hash(password, iterations)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	fmt.Println(hash)
	return 0
}

// readPassword takes the password from standard input, without echoing
// it when standard input is a terminal.
//
// Through `stty` rather than a terminal library, because SPEC 4.2
// admits no dependency for this and the platform already has the tool.
// A terminal where `stty` is not available still works; it echoes, and
// says so, rather than silently typing a password onto the screen as
// though it were hidden.
func readPassword() (string, error) {
	restore := func() {}
	if isTerminal() {
		if err := stty("-echo"); err == nil {
			restore = func() { _ = stty("echo") }
		} else {
			fmt.Fprintln(os.Stderr,
				"warning: this terminal will show the password as it is typed")
		}
		fmt.Fprint(os.Stderr, "password: ")
	}
	defer func() {
		restore()
		if isTerminal() {
			fmt.Fprintln(os.Stderr)
		}
	}()

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// isTerminal reports whether standard input is one, without asking a
// library: a character device that is not a pipe is what a terminal
// looks like from here.
func isTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func stty(mode string) error {
	cmd := exec.Command("stty", mode)
	cmd.Stdin = os.Stdin
	cmd.Stderr = nil
	return cmd.Run()
}

// accountList shows who is in the account file, without the verifiers.
func accountList(args *cli.Args) int {
	s := setup(args)
	f := loadAccounts(s, args)
	out := value.NewMap(len(f.Accounts))
	for _, name := range f.Names() {
		a, _ := f.Lookup(name)
		entry := value.NewMap(4)
		entry.Set("principal", a.Principal())
		if len(a.Roles) > 0 {
			roles := make([]any, len(a.Roles))
			for i, r := range a.Roles {
				roles[i] = r
			}
			entry.Set("roles", roles)
		}
		entry.Set("second_factor", a.NeedsSecondFactor())
		if a.Disabled {
			entry.Set("disabled", true)
		}
		out.Set(name, entry)
	}
	writeOut(args, out)
	return 0
}
