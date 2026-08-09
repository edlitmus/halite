package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/ca"
)

const keyUsage = `usage:
  halite key init [-pki DIR] [-cn NAME] [-days N]
      create the CA (once per fleet)
  halite key server <hostname> [-san host,ip,...] [-days N]
      issue the control plane's server certificate
  halite key gen <id> [-out DIR]
      agent side: generate a private key and a signing request
  halite key submit <id> <csr-file>
      file a signing request with the CA (the agent does this over the wire)
  halite key list
      show every known identity, its state, and its key fingerprint
  halite key accept <id> ... | -all
  halite key reject <id> ...
  halite key remove <id> ...
  halite key show <id>
      print the issued certificate, or the pending request

All subcommands take -pki DIR (default: $HALITE_PKI or the platform path).`

// defaultPKI is the PKI directory when -pki and $HALITE_PKI are unset. It
// sits beside the state tree.
func defaultPKI() string {
	return filepath.Join(filepath.Dir(defaultRoot()), "pki")
}

func resolvePKI(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("HALITE_PKI"); env != "" {
		return env
	}
	return defaultPKI()
}

func cmdKey(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, keyUsage)
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "init":
		keyInit(rest)
	case "server":
		keyServer(rest)
	case "gen":
		keyGen(rest)
	case "submit":
		keySubmit(rest)
	case "list":
		keyList(rest)
	case "accept", "reject", "remove":
		keyDecide(sub, rest)
	case "show":
		keyShow(rest)
	case "-h", "--help", "help":
		fmt.Println(keyUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown key subcommand %q\n%s\n", sub, keyUsage)
		os.Exit(2)
	}
}

// pkiFlags adds the flags every key subcommand shares and returns an
// accessor for the resolved PKI directory.
func pkiFlags(fs *flag.FlagSet) func() string {
	pki := fs.String("pki", "", "PKI directory (default: $HALITE_PKI or the platform path)")
	return func() string { return resolvePKI(*pki) }
}

func keyInit(args []string) {
	fs := flag.NewFlagSet("key init", flag.ExitOnError)
	pki := pkiFlags(fs)
	cn := fs.String("cn", "halite ca", "common name for the CA certificate")
	days := fs.Int("days", 3650, "validity in days")
	_ = parseFlags(fs, args)

	store := &ca.Store{Dir: pki()}
	if err := store.Init(*cn, time.Duration(*days)*24*time.Hour); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("CA created in %s\n", store.Dir)
	fmt.Printf("distribute %s to agents as their trust root\n", filepath.Join(store.Dir, "ca.crt"))
}

func keyServer(args []string) {
	fs := flag.NewFlagSet("key server", flag.ExitOnError)
	pki := pkiFlags(fs)
	sans := fs.String("san", "", "extra subject alternative names (comma-separated hosts or IPs)")
	days := fs.Int("days", 825, "validity in days")
	rest := parseFlags(fs, args)

	if len(rest) != 1 {
		fatal("usage: halite key server <hostname> [-san host,ip,...]")
	}
	hostname := rest[0]
	hosts := []string{hostname}
	for _, s := range strings.Split(*sans, ",") {
		if s = strings.TrimSpace(s); s != "" && s != hostname {
			hosts = append(hosts, s)
		}
	}

	store := &ca.Store{Dir: pki()}
	if err := store.IssueServer(hostname, hosts, time.Duration(*days)*24*time.Hour); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("server certificate for %s written to %s\n", hostname, filepath.Join(store.Dir, "master.crt"))
	fmt.Printf("valid for: %s\n", strings.Join(hosts, ", "))
}

func keyGen(args []string) {
	fs := flag.NewFlagSet("key gen", flag.ExitOnError)
	pki := pkiFlags(fs)
	out := fs.String("out", "", "directory for agent.key and agent.csr (default: the PKI directory)")
	rest := parseFlags(fs, args)

	if len(rest) != 1 {
		fatal("usage: halite key gen <id>")
	}
	id := rest[0]
	if err := ca.ValidateID(id); err != nil {
		fatal("%v", err)
	}
	dir := *out
	if dir == "" {
		dir = pki()
	}

	key, keyPEM, err := ca.GenerateKey()
	if err != nil {
		fatal("%v", err)
	}
	csrPEM, err := ca.NewCSR(key, id, nil)
	if err != nil {
		fatal("%v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal("%v", err)
	}
	keyPath := filepath.Join(dir, "agent.key")
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		fatal("write %s: %v", keyPath, err)
	}
	csrPath := filepath.Join(dir, "agent.csr")
	if err := os.WriteFile(csrPath, csrPEM, 0o644); err != nil {
		fatal("write %s: %v", csrPath, err)
	}
	fp, err := ca.CSRFingerprint(csrPEM)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("id:          %s\n", id)
	fmt.Printf("key:         %s\n", keyPath)
	fmt.Printf("request:     %s\n", csrPath)
	fmt.Printf("fingerprint: %s\n", fp)
}

func keySubmit(args []string) {
	fs := flag.NewFlagSet("key submit", flag.ExitOnError)
	pki := pkiFlags(fs)
	rest := parseFlags(fs, args)

	if len(rest) != 2 {
		fatal("usage: halite key submit <id> <csr-file>")
	}
	id, csrPath := rest[0], rest[1]
	csrPEM, err := os.ReadFile(csrPath)
	if err != nil {
		fatal("read %s: %v", csrPath, err)
	}
	state, err := (&ca.Store{Dir: pki()}).Submit(id, csrPEM)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("%s: %s\n", id, state)
}

func keyList(args []string) {
	fs := flag.NewFlagSet("key list", flag.ExitOnError)
	pki := pkiFlags(fs)
	_ = parseFlags(fs, args)

	entries, err := (&ca.Store{Dir: pki()}).List()
	if err != nil {
		fatal("%v", err)
	}
	if len(entries) == 0 {
		fmt.Println("no keys")
		return
	}
	for _, e := range entries {
		fmt.Printf("%-10s %-20s %s\n", e.State, e.ID, e.Fingerprint)
	}
}

// keyDecide runs accept, reject, or remove over one or more identities.
// Failures are reported per identity and the exit status reflects them, so
// a bulk accept does not stop halfway.
func keyDecide(action string, args []string) {
	fs := flag.NewFlagSet("key "+action, flag.ExitOnError)
	pki := pkiFlags(fs)
	all := fs.Bool("all", false, "apply to every pending identity")
	ids := parseFlags(fs, args)

	store := &ca.Store{Dir: pki()}
	if *all {
		entries, err := store.List()
		if err != nil {
			fatal("%v", err)
		}
		for _, e := range entries {
			if e.State == ca.StatePending {
				ids = append(ids, e.ID)
			}
		}
	}
	if len(ids) == 0 {
		fatal("usage: halite key %s <id> ... | -all", action)
	}

	failed := 0
	for _, id := range ids {
		var err error
		switch action {
		case "accept":
			_, err = store.Accept(id)
		case "reject":
			err = store.Reject(id)
		case "remove":
			err = store.Remove(id)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
			failed++
			continue
		}
		fmt.Printf("%s: %sed\n", id, strings.TrimSuffix(action, "e"))
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func keyShow(args []string) {
	fs := flag.NewFlagSet("key show", flag.ExitOnError)
	pki := pkiFlags(fs)
	rest := parseFlags(fs, args)

	if len(rest) != 1 {
		fatal("usage: halite key show <id>")
	}
	id := rest[0]
	store := &ca.Store{Dir: pki()}
	if certPEM, err := store.IssuedCert(id); err == nil {
		fmt.Print(string(certPEM))
		return
	}
	entries, err := store.List()
	if err != nil {
		fatal("%v", err)
	}
	for _, e := range entries {
		if e.ID == id {
			fmt.Printf("%s: %s (fingerprint %s)\n", e.ID, e.State, e.Fingerprint)
			return
		}
	}
	fatal("unknown id %q", id)
}
