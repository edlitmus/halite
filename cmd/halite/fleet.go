package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/agent"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/master"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/returner"
	"github.com/edlitmus/halite/internal/transport"
)

// defaultCache is where an agent keeps the state tree it fetched.
func defaultCache() string {
	switch runtime.GOOS {
	case "windows":
		return `C:\ProgramData\halite\cache`
	default:
		return "/var/cache/halite"
	}
}

// signalContext cancels on SIGINT or SIGTERM so both daemons shut down
// cleanly under an init system.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func cmdMaster(args []string) {
	fs := flag.NewFlagSet("master", flag.ExitOnError)
	addr := fs.String("addr", fmt.Sprintf(":%d", transport.DefaultPort), "listen address")
	pkiFlag := fs.String("pki", "", "PKI directory (default: $HALITE_PKI or the platform path)")
	rootFlag := fs.String("root", "", "state tree root served to agents")
	pillarRootFlag := fs.String("pillar-root", "", "pillar tree root")
	autoAccept := fs.Bool("auto-accept", false, "sign enrollment requests without an operator decision (labs only)")
	pollTimeout := fs.Duration("poll-timeout", 30*time.Second, "how long an agent's job poll is held open")
	var returnerSpecs stringList
	fs.Var(&returnerSpecs, "returner", "durable result sink, kind:target (repeatable): file:PATH or webhook:URL")
	_ = parseFlags(fs, args)

	var returners []returner.Returner
	for _, spec := range returnerSpecs {
		r, err := returner.Parse(spec)
		if err != nil {
			fatal("%v", err)
		}
		returners = append(returners, r)
	}

	root := resolveRoot(*rootFlag)
	cfg := master.Config{
		Addr:        *addr,
		PKIDir:      resolvePKI(*pkiFlag),
		StatesRoot:  root,
		PillarRoot:  resolvePillarRoot(*pillarRootFlag, root),
		AutoAccept:  *autoAccept,
		PollTimeout: *pollTimeout,
		Returners:   returners,
	}
	if _, err := os.Stat(cfg.StatesRoot); err != nil {
		fatal("state tree %s is not readable: %v", cfg.StatesRoot, err)
	}

	ctx, stop := signalContext()
	defer stop()
	logger := log.New(os.Stderr, "halite-master ", log.LstdFlags)
	if err := master.New(cfg, logger).Run(ctx); err != nil {
		fatal("%v", err)
	}
}

func cmdAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	masterAddr := fs.String("master", os.Getenv("HALITE_MASTER"), "control plane host[:port] (or $HALITE_MASTER)")
	id := fs.String("id", "", "agent identity (default: the host grain)")
	pkiFlag := fs.String("pki", "", "PKI directory holding ca.crt and this agent's key")
	cacheFlag := fs.String("cache", "", "directory for the fetched state tree")
	retry := fs.Duration("retry", 10*time.Second, "delay between reconnection and enrollment attempts")
	_ = parseFlags(fs, args)

	cache := *cacheFlag
	if cache == "" {
		cache = defaultCache()
	}
	cfg := agent.Config{
		ID:            *id,
		Master:        *masterAddr,
		PKIDir:        resolvePKI(*pkiFlag),
		CacheDir:      cache,
		Version:       version,
		RetryInterval: *retry,
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o755); err != nil {
		fatal("cache directory: %v", err)
	}

	logger := log.New(os.Stderr, "halite-agent ", log.LstdFlags)
	a, err := agent.New(cfg, grains.Collect(), logger)
	if err != nil {
		fatal("%v", err)
	}
	ctx, stop := signalContext()
	defer stop()
	if err := a.Run(ctx); err != nil {
		fatal("%v", err)
	}
}

// operatorClient builds a control plane client from the operator's
// certificate.
func operatorClient(masterAddr, pkiDir string) *transport.Client {
	if masterAddr == "" {
		fatal("no control plane address (-master or $HALITE_MASTER)")
	}
	tlsCfg, err := transport.ClientTLS(
		filepath.Join(pkiDir, "admin.crt"),
		filepath.Join(pkiDir, "admin.key"),
		filepath.Join(pkiDir, "ca.crt"))
	if err != nil {
		fatal("%v (run 'halite key admin <name>' on the CA host)", err)
	}
	return transport.NewJSONClient(masterAddr, tlsCfg, 60*time.Second)
}

func cmdAgents(args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	masterAddr := fs.String("master", os.Getenv("HALITE_MASTER"), "control plane host[:port]")
	pkiFlag := fs.String("pki", "", "PKI directory holding the operator certificate")
	asJSON := fs.Bool("json", false, "output as JSON")
	_ = parseFlags(fs, args)

	client := operatorClient(*masterAddr, resolvePKI(*pkiFlag))
	var agents []transport.Agent
	if err := client.Get(context.Background(), transport.PathAgents, &agents); err != nil {
		fatal("%v", err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(agents, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(agents) == 0 {
		fmt.Println("no agents have connected")
		return
	}
	for _, a := range agents {
		status := "offline"
		if a.Online {
			status = "online"
		}
		osName, _ := a.Grains["os"].(string)
		fmt.Printf("%-20s %-8s %-12s last seen %s\n",
			a.ID, status, osName, a.LastSeen.Local().Format(time.RFC3339))
	}
}

const runUsage = `usage: halite run <target> <kind> [args] [flags]

targets:
  '*'                      every online agent
  'os_family:FreeBSD'      a grain, with a glob on its value
  'web*'                   a glob on the agent's id

kinds:
  state.highstate          converge against the control plane's top.sls
  state.apply <sls> ...    apply named sls files
  call <module.fn> [k=v]   run one state function
  grains                   report the agent's facts
  pillar                   report the agent's resolved pillar

flags:
  -test        dry run
  -wait DUR    how long to wait for results (default 2m)
  -json        machine-readable output
  -master HOST control plane address (or $HALITE_MASTER)`

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	masterAddr := fs.String("master", os.Getenv("HALITE_MASTER"), "control plane host[:port]")
	pkiFlag := fs.String("pki", "", "PKI directory holding the operator certificate")
	test := fs.Bool("test", false, "dry run: report changes without applying")
	asJSON := fs.Bool("json", false, "output results as JSON")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for results")
	rest := parseFlags(fs, args)

	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, runUsage)
		os.Exit(2)
	}
	req, err := buildDispatch(rest, *test)
	if err != nil {
		fatal("%v\n\n%s", err, runUsage)
	}

	client := operatorClient(*masterAddr, resolvePKI(*pkiFlag))
	ctx, stop := signalContext()
	defer stop()

	var dispatched transport.DispatchResponse
	if err := client.Post(ctx, transport.PathDispatch, req, &dispatched); err != nil {
		fatal("%v", err)
	}
	if len(dispatched.Agents) == 0 {
		fatal("job %s matched no online agents", dispatched.JobID)
	}
	if !*asJSON {
		fmt.Printf("job %s dispatched to %d agent(s): %s\n\n",
			dispatched.JobID, len(dispatched.Agents), strings.Join(dispatched.Agents, ", "))
	}

	info := collectResults(ctx, client, dispatched, *wait)
	if *asJSON {
		b, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(b))
	} else {
		printJobInfo(info)
	}
	for _, res := range info.Results {
		if !res.Ok {
			os.Exit(1)
		}
	}
	if len(info.Results) < len(info.Expecting) {
		os.Exit(1)
	}
}

// buildDispatch turns the command line into a dispatch request.
func buildDispatch(args []string, test bool) (transport.DispatchRequest, error) {
	req := transport.DispatchRequest{Target: args[0], Kind: args[1], Test: test}
	rest := args[2:]

	switch req.Kind {
	case transport.KindHighstate, transport.KindGrains, transport.KindPillar:
		if len(rest) > 0 {
			return req, fmt.Errorf("%s takes no arguments", req.Kind)
		}
	case transport.KindApply:
		if len(rest) == 0 {
			return req, fmt.Errorf("state.apply needs at least one sls name")
		}
		req.SLS = rest
	case transport.KindCall:
		if len(rest) == 0 {
			return req, fmt.Errorf("call needs a module.function")
		}
		req.Fn = rest[0]
		req.Args = map[string]string{}
		for _, kv := range rest[1:] {
			key, value, found := strings.Cut(kv, "=")
			if !found || key == "" {
				return req, fmt.Errorf("argument %q is not key=value", kv)
			}
			req.Args[key] = value
		}
	default:
		return req, fmt.Errorf("unknown kind %q", req.Kind)
	}
	return req, nil
}

// collectResults polls the control plane until every agent has answered or
// the deadline passes. Agents that never answer are reported as missing
// rather than silently dropped.
func collectResults(
	ctx context.Context,
	client *transport.Client,
	dispatched transport.DispatchResponse,
	wait time.Duration,
) transport.JobInfo {
	deadline := time.Now().Add(wait)
	info := transport.JobInfo{Expecting: dispatched.Agents}
	for {
		var current transport.JobInfo
		if err := client.Get(ctx, transport.PathJobInfo+dispatched.JobID, &current); err != nil {
			fatal("%v", err)
		}
		info = current
		if len(info.Results) >= len(info.Expecting) || time.Now().After(deadline) || ctx.Err() != nil {
			return info
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return info
		}
	}
}

// stateResult adapts a wire outcome to the local Result type so fleet runs
// print exactly like a local `halite apply`.
func stateResult(st transport.StateOutcome) modules.Result {
	return modules.Result{
		Ok:      st.Ok,
		Changed: st.Changed,
		Comment: st.Comment,
		Changes: st.Changes,
	}
}

func printJobInfo(info transport.JobInfo) {
	answered := map[string]bool{}
	for _, res := range info.Results {
		answered[res.AgentID] = true
		fmt.Printf("%s:\n", res.AgentID)
		if res.Error != "" {
			fmt.Printf("    error: %s\n", res.Error)
		}
		for _, st := range res.States {
			printResult(st.ID, st.Function, stateResult(st))
		}
		if len(res.Data) > 0 {
			printTree(res.Data, "    ")
		}
		if len(res.States) > 0 {
			fmt.Printf("  Summary: succeeded=%d (changed=%d) failed=%d in %s\n",
				res.Succeeded, res.Changed, res.Failed, res.Duration.Round(time.Millisecond))
		}
		fmt.Println()
	}

	var missing []string
	for _, id := range info.Expecting {
		if !answered[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		fmt.Printf("no result from: %s\n", strings.Join(missing, ", "))
	}
}

// moduleResult builds a Result from decoded wire fields, for printing
// remote output in the local format.
func moduleResult(ok, changed bool, comment string, changes map[string]string) modules.Result {
	return modules.Result{Ok: ok, Changed: changed, Comment: comment, Changes: changes}
}

const eventsUsage = `usage: halite events [-tag PATTERN] [-history N] [flags]

Tails the control plane's event bus. Tags are slash-delimited; '*' matches
within one segment and '**' matches any number:

  halite events
  halite events -tag 'halite/job/**'
  halite events -tag 'halite/agent/*/hello'
  halite events -tag 'halite/beacon/**' -history 50 -json

Runs until interrupted.`

func cmdEvents(args []string) {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	masterAddr := fs.String("master", os.Getenv("HALITE_MASTER"), "control plane host[:port]")
	pkiFlag := fs.String("pki", "", "PKI directory holding the operator certificate")
	tag := fs.String("tag", "**", "tag pattern to subscribe to")
	history := fs.Int("history", -1, "replay this many past events first (0 for everything kept)")
	asJSON := fs.Bool("json", false, "print raw event JSON, one per line")
	if rest := parseFlags(fs, args); len(rest) > 0 {
		fmt.Fprintln(os.Stderr, eventsUsage)
		os.Exit(2)
	}

	path := transport.PathEvents + "?tag=" + url.QueryEscape(*tag)
	if *history >= 0 {
		path += "&history=" + strconv.Itoa(*history)
	}

	// An event tail is open-ended, so it gets its own client rather than the
	// request-shaped one with a timeout.
	client := operatorClient(*masterAddr, resolvePKI(*pkiFlag))
	client.HTTP.Timeout = 0

	ctx, stop := signalContext()
	defer stop()

	body, err := client.Stream(ctx, path)
	if err != nil {
		fatal("%v", err)
	}
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), transport.MaxBodyBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue // keepalive
		}
		if *asJSON {
			fmt.Println(line)
			continue
		}
		var ev transport.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			fmt.Println(line)
			continue
		}
		printEvent(ev)
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		fatal("event stream: %v", err)
	}
}

func printEvent(ev transport.Event) {
	fmt.Printf("%s  %-44s %s\n", ev.Time.Local().Format("15:04:05"), ev.Tag, ev.Source)
	if len(ev.Data) > 0 {
		printTree(ev.Data, "    ")
	}
}
