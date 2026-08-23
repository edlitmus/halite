package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// operatorClient builds the connection `run` and `jobs` use.
//
// The operator authenticates with a certificate, not with the fact of
// being logged in on the hub: `halite-hub run` reaching a hub over the
// network and one reaching it over loopback are the same request, and
// treating them differently is how an unauthenticated local path
// appears.
func operatorClient(args *cli.Args) *transport.Client {
	cfg, err := config.Load(config.Hub, config.LoadOptions{
		Path:         args.Flag("config", ""),
		Root:         args.Flag("root", config.DefaultRoot),
		AllowMissing: true,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	files := pki.Files{Dir: args.Flag("pki-dir", cfg.String("pki_dir", config.DefaultPKIDir))}

	name := args.Flag("as", "")
	certPath := args.Flag("cert", "")
	keyPath := args.Flag("key", "")
	if certPath == "" || keyPath == "" {
		if name == "" {
			name = defaultOperator(files)
		}
		if name == "" {
			cli.Fatalf("no operator certificate was found in %s; `halite-hub keys operator create <name>` makes one", files.Dir)
		}
		certPath = files.Path("operator-" + name + ".crt")
		keyPath = files.Path("operator-" + name + ".key")
	}

	pair, err := loadPair(certPath, keyPath)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		cli.Fatalf("%v", err)
	}

	address := args.Flag("hub", cfg.String("hub", ""))
	if address == "" {
		// The operator command line most often runs on the hub itself.
		address = "localhost"
	}
	url := address
	if !strings.Contains(address, "://") {
		if !strings.Contains(address, ":") {
			listen := cfg.String("listen", fmt.Sprintf(":%d", transport.DefaultPort))
			url = fmt.Sprintf("https://%s%s", address, portOf(listen))
		} else {
			url = "https://" + address
		}
	}
	return &transport.Client{
		HubURL:     url,
		CA:         ca,
		Cert:       &pair,
		ServerName: args.Flag("server-name", ""),
		Timeout:    30 * time.Second,
	}
}

// defaultOperator finds the one operator certificate in the key
// directory, and refuses to guess between several.
func defaultOperator(files pki.Files) string {
	entries, err := os.ReadDir(files.Dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "operator-") && strings.HasSuffix(name, ".crt") {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(name, "operator-"), ".crt"))
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	if len(names) > 1 {
		sort.Strings(names)
		cli.Fatalf("there are %d operator certificates in %s (%s); say which with --as",
			len(names), files.Dir, strings.Join(names, ", "))
	}
	return ""
}

func portOf(listen string) string {
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		return listen[i:]
	}
	return fmt.Sprintf(":%d", transport.DefaultPort)
}

// runRun is `halite-hub run '<target>' <function> [args...]`, the old
// `salt` command.
func runRun(args *cli.Args) int {
	if len(args.Positional) < 2 {
		cli.Fatalf("run needs a target and a function: halite-hub run '*' test.ping")
	}
	target := args.Positional[0]
	fun := args.Positional[1]
	rest := args.Positional[2:]

	kind := ""
	for _, flag := range []string{"L", "E", "G", "P", "I", "J", "S", "N", "C",
		"list", "pcre", "grain", "grain_pcre", "pillar", "pillar_pcre", "ipcidr", "nodegroup", "compound"} {
		if args.Bool(flag, false) {
			kind = flag
			break
		}
	}

	kwargs := map[string]any{}
	for _, e := range args.Kwargs.Entries() {
		if k, ok := e.Key.(string); ok {
			kwargs[k] = e.Val
		}
	}

	ttl := 0
	if v := args.Flag("ttl", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			cli.Fatalf("--ttl %q: %v", v, err)
		}
		ttl = int(d.Seconds())
	}

	client := operatorClient(args)
	ctx := context.Background()
	res, err := client.Submit(ctx, transport.SubmitRequest{
		Target:     target,
		TargetKind: kind,
		Fun:        fun,
		Arg:        rest,
		Kwarg:      kwargs,
		Env:        args.Flag("env", ""),
		Test:       args.Bool("test", false),
		Offline:    args.Flag("offline", ""),
		TTLSeconds: ttl,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}

	if len(res.Nodes) == 0 {
		fmt.Fprintf(os.Stderr, "no node matched %q\n", target)
		return 1
	}
	for _, id := range res.Absent {
		fmt.Fprintf(os.Stderr, "%s: not connected\n", id)
	}

	if args.Bool("async", false) {
		fmt.Println(res.JID)
		return 0
	}

	timeout, err := time.ParseDuration(args.Flag("timeout", "5m"))
	if err != nil {
		cli.Fatalf("--timeout %q: %v", args.Flag("timeout", ""), err)
	}
	return gather(client, res, timeout, args)
}

// gather polls the hub until every expected node has answered or the
// window closes.
//
// Polling rather than a streamed response: the job and its returns are
// hub-side records, so a caller that is disconnected, killed, or run
// again later sees exactly the same thing. SPEC 9.3 makes the same
// argument for batching.
func gather(client *transport.Client, sub *transport.SubmitResponse, timeout time.Duration, args *cli.Args) int {
	deadline := time.Now().Add(timeout)
	// Only the nodes the job was delivered to. Waiting the full gather
	// window for a node the hub has already reported as not connected
	// means `run '*' test.ping` blocks for five minutes because one
	// machine is off, which is exactly the case an operator runs it in.
	expected := len(sub.Nodes) - len(sub.Absent)
	var status *transport.JobStatus

	for {
		var err error
		status, err = client.JobStatus(context.Background(), sub.JID)
		if err != nil {
			cli.Fatalf("%v", err)
		}
		if len(status.Returns) >= expected || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	returns := make([]*job.Return, 0, len(status.Returns))
	for _, raw := range status.Returns {
		var r job.Return
		if err := json.Unmarshal(raw, &r); err != nil {
			cli.Fatalf("the hub's answer is not readable: %v", err)
		}
		returns = append(returns, &r)
	}

	format := args.Flag("out", "nested")
	if format != "nested" && format != "summary" {
		out := value.NewMap(len(returns))
		for _, r := range returns {
			out.Set(r.NodeID, decodeReturn(r))
		}
		if err := cli.Write(os.Stdout, out, cli.Format(format), 2); err != nil {
			cli.Fatalf("%v", err)
		}
	} else {
		for _, r := range returns {
			fmt.Printf("%s:\n", r.NodeID)
			printReturn(r)
		}
	}

	for _, id := range status.Missing {
		fmt.Fprintf(os.Stderr, "%s: no answer\n", id)
	}

	failed := 0
	for _, r := range returns {
		if !r.Success {
			failed++
		}
	}
	switch {
	case failed > 0:
		return 1
	case len(status.Missing) > 0:
		// Distinct from a failure: nothing said no, and something did
		// not say anything, and an operator needs to tell those apart.
		return 3
	}
	return 0
}

func printReturn(r *job.Return) {
	decoded := decodeReturn(r)
	// A state run comes back in the return schema of SPEC 9.4, and it
	// is rendered by the same code that renders a local run: an
	// operator reading `halite-hub run '*' state.apply` and one reading
	// `halite-node state apply` should be reading the same thing.
	if m, ok := decoded.(*value.Map); ok && r.Out == "highstate" {
		fmt.Print(indentBlock(runner.NestedFromReturns(m, nil), "    "))
		return
	}
	if err := cli.Write(os.Stdout, decoded, cli.Nested, 4); err != nil {
		cli.Fatalf("%v", err)
	}
}

// indentBlock puts a node's output under its name.
func indentBlock(text, indent string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// decodeReturn reads a node's payload back into the ordered model, so
// that a highstate prints in the order the states ran rather than in
// whatever order a map iterated.
func decodeReturn(r *job.Return) any {
	if len(r.Return) == 0 {
		return nil
	}
	decoded, err := value.DecodeJSON(r.Return)
	if err != nil {
		return string(r.Return)
	}
	return decoded
}

// loadPair reads an operator's certificate and key.
func loadPair(certPath, keyPath string) (tls.Certificate, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("reading %s: %w", keyPath, err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s and %s do not go together: %w", certPath, keyPath, err)
	}
	return pair, nil
}
