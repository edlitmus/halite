// Command simfleet is the simulated node harness SPEC 30 names as the
// method for most of its targets: thousands of nodes connected to one
// real hub, over the real transport, answering real jobs, so that the
// rows a benchmark cannot measure -- nodes per hub, hub memory, a
// `test.ping` to 10,000 nodes, a `state.apply` dispatch to 10,000 -- are
// measured rather than asserted.
//
// What is real and what is simulated:
//
//   - The hub is the real `halite-hub serve`, with its real key store,
//     enrollment CA, policy, dispatch, fleet and job cache.
//   - Each simulated node holds a real identity: a P-256 key and a
//     certificate the hub's own CA issued through keystore.Authority's
//     Enroll and Accept -- what `halite-node enroll` and `keys accept`
//     do, in process, because 20,000 interactive enrolments measure an
//     operator rather than a hub, and enrolment rate is not a SPEC 30 row.
//   - Each holds its own mutual-TLS stream to the hub, through
//     transport.Client, as a node does.
//   - What is simulated is the work: a node answers `test.ping` with
//     `true` and any other function with an empty, successful result, at
//     once. The rows measured here are the hub's, not a node's.
//
// A real node opens a new client, and so a new TLS connection, for every
// return it files (`postReturns` in halite-node builds one per return).
// The simulated nodes do the same by default, so the hub sees the load a
// fleet of real nodes would give it; -reuse-client files returns on one
// connection per node instead, to measure what that costs.
//
//	simfleet setup -root DIR -n 20000
//	halite-hub serve --root DIR --config DIR/hub.yaml --listen 127.0.0.1:4510 --names localhost,127.0.0.1
//	simfleet run -root DIR -hub 127.0.0.1:4510 -n 20000 -hub-pid <pid> -ping -apply
//
// A number from one machine running both the hub and the fleet is
// indicative, not SPEC 30's verdict: the table names "one 16-core hub",
// and a fleet on the same machine takes CPU from it. -hub and -root make
// the fleet runnable from another machine than the hub, given a copy of
// DIR.
package main

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

const operatorName = "simfleet"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "setup":
		err = setupCmd(os.Args[2:])
	case "run":
		err = runCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "simfleet:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, "usage:\n"+
		"  simfleet setup -root DIR -n N [-prefix sim]     a throwaway hub configuration and N node identities\n"+
		"  simfleet run   -root DIR -hub ADDR -n N [flags] connect N simulated nodes and measure\n")
	os.Exit(2)
}

func nodeID(prefix string, i int) string { return fmt.Sprintf("%s-%05d.simfleet", prefix, i) }

// ---- setup ----

func setupCmd(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	root := fs.String("root", "", "the directory to set the throwaway hub up in")
	n := fs.Int("n", 1000, "how many node identities to issue")
	prefix := fs.String("prefix", "sim", "the node id prefix")
	workers := fs.Int("workers", 8, "identities issued in parallel")
	_ = fs.Parse(args)
	if *root == "" {
		return errors.New("-root is required")
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	for _, d := range []string{"pki", "state", "cache", "fleet"} {
		if err := os.MkdirAll(filepath.Join(abs, d), 0o700); err != nil {
			return err
		}
	}
	hubYAML := fmt.Sprintf("state_dir: %s\npki_dir: %s\ncache_dir: %s\nlog_format: json\nlog_level: warn\n",
		filepath.Join(abs, "state"), filepath.Join(abs, "pki"), filepath.Join(abs, "cache"))
	if err := os.WriteFile(filepath.Join(abs, "hub.yaml"), []byte(hubYAML), 0o644); err != nil {
		return err
	}
	policy := "roles:\n  harness:\n    - target: '*'\n      functions: ['*']\n      runners: ['*']\n" +
		"bindings:\n  - principal: 'cert:CN=" + operatorName + "'\n    roles: ['harness']\n"
	if err := os.WriteFile(filepath.Join(abs, "policy.yaml"), []byte(policy), 0o644); err != nil {
		return err
	}

	files := pki.Files{Dir: filepath.Join(abs, "pki")}
	ca, err := files.LoadCA(time.Now)
	if err != nil {
		if ca, err = files.CreateCA(pki.ECDSAP256, "simfleet enrollment CA", 10*365*24*time.Hour); err != nil {
			return err
		}
	}
	store, err := keystore.Open(filepath.Join(abs, "state", "keys"))
	if err != nil {
		return err
	}
	auth := &keystore.Authority{Store: store, CA: ca, Mode: keystore.ModeManual, Lifetime: keystore.DefaultLifetime}

	// The harness's own operator certificate, which the policy above
	// binds: it submits the jobs the measurements time.
	opKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		return err
	}
	opDER, err := ca.IssueOperator(opKey, operatorName, 30*24*time.Hour)
	if err != nil {
		return err
	}
	if err := writePair(filepath.Join(abs, "operator"), pki.EncodeCert(opDER), opKey); err != nil {
		return err
	}

	started := time.Now()
	var next, done atomic.Int64
	errs := make(chan error, *workers)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1))
				if i > *n {
					return
				}
				// An identity already on disk is kept: issuing a second key
				// for the same id is refused by the hub (a rebuilt node must
				// be deleted first), so growing a fleet from 200 to 2000
				// would otherwise fail on its first node.
				if _, err := os.Stat(filepath.Join(abs, "fleet", nodeID(*prefix, i)+".crt")); err == nil {
					done.Add(1)
					continue
				}
				if err := issueNode(auth, filepath.Join(abs, "fleet"), nodeID(*prefix, i)); err != nil {
					errs <- err
					return
				}
				if d := done.Add(1); d%1000 == 0 {
					fmt.Printf("issued %d identities\n", d)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}
	fmt.Printf("%s: hub.yaml, policy.yaml, operator certificate and %d node identities in %s\n",
		abs, *n, time.Since(started).Round(time.Millisecond))
	return nil
}

// issueNode does what `halite-node enroll` and `keys accept` do: a key, a
// request the hub records as pending, an accept that issues, and the
// certificate collected by enrolling again.
func issueNode(auth *keystore.Authority, dir, id string) error {
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		return err
	}
	csrDER, err := pki.NewNodeCSR(key, id)
	if err != nil {
		return err
	}
	req := keystore.Request{CSR: pki.EncodeCSR(csrDER)}
	if _, err := auth.Enroll(req); err != nil && !errors.Is(err, keystore.ErrPending) {
		return fmt.Errorf("%s: enroll: %w", id, err)
	}
	if _, err := auth.Accept(id); err != nil {
		return fmt.Errorf("%s: accept: %w", id, err)
	}
	res, err := auth.Enroll(req)
	if err != nil {
		return fmt.Errorf("%s: collecting the certificate: %w", id, err)
	}
	return writePair(filepath.Join(dir, id), res.Cert, key)
}

func writePair(base string, certPEM []byte, key crypto.Signer) error {
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(base+".key", keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(base+".crt", certPEM, 0o644)
}

// ---- run ----

// sim is one simulated node.
type sim struct {
	id       string
	pair     tls.Certificate
	received atomic.Int64 // unix nanos the current measured job arrived
	returned atomic.Int64 // unix nanos its return was accepted by the hub
}

type fleet struct {
	hubURL, serverName string
	ca                 *x509.Certificate
	reuse              bool
	sims               []*sim
	connected          atomic.Int64
	returnErrs         atomic.Int64
	measuring          atomic.Value // the jid being timed, a string
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	root := fs.String("root", "", "the directory `simfleet setup` wrote")
	hub := fs.String("hub", "127.0.0.1:4510", "the hub's address")
	serverName := fs.String("server-name", "localhost", "the name to verify in the hub's certificate")
	n := fs.Int("n", 1000, "how many simulated nodes to connect")
	prefix := fs.String("prefix", "sim", "the node id prefix setup used")
	ramp := fs.Int("ramp", 1000, "nodes connected per second")
	hubPID := fs.Int("hub-pid", 0, "the hub's process id, to report its resident memory")
	ping := fs.Bool("ping", false, "time a test.ping to every simulated node")
	apply := fs.Bool("apply", false, "time a state.apply dispatch to every simulated node")
	reuse := fs.Bool("reuse-client", false, "file returns on one connection per node, rather than a new one per return as a real node does")
	hold := fs.Duration("hold", 0, "stay connected this long after measuring")
	_ = fs.Parse(args)
	if *root == "" {
		return errors.New("-root is required")
	}

	files := pki.Files{Dir: filepath.Join(*root, "pki")}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		return err
	}
	f := &fleet{hubURL: "https://" + *hub, serverName: *serverName, ca: ca, reuse: *reuse}
	f.measuring.Store("")
	for i := 1; i <= *n; i++ {
		id := nodeID(*prefix, i)
		base := filepath.Join(*root, "fleet", id)
		pair, err := tls.LoadX509KeyPair(base+".crt", base+".key")
		if err != nil {
			return fmt.Errorf("%s: %w; run simfleet setup with -n of at least %d", id, err, *n)
		}
		f.sims = append(f.sims, &sim{id: id, pair: pair})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report("hub memory before connecting", rss(*hubPID))

	started := time.Now()
	interval := time.Second / time.Duration(max(*ramp, 1))
	for _, s := range f.sims {
		go f.connect(ctx, s)
		time.Sleep(interval)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for f.connected.Load() < int64(*n) && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	fmt.Printf("connected: %d of %d in %s\n", f.connected.Load(), *n, time.Since(started).Round(time.Millisecond))
	time.Sleep(2 * time.Second)
	report(fmt.Sprintf("hub memory with %d nodes connected", f.connected.Load()), rss(*hubPID))

	op, err := f.operator(*root)
	if err != nil {
		return err
	}
	if *ping {
		if err := f.measure(ctx, op, "test.ping", *prefix, 10*time.Second, "returns"); err != nil {
			return err
		}
		report("hub memory after test.ping", rss(*hubPID))
	}
	if *apply {
		if err := f.measure(ctx, op, "state.apply", *prefix, 30*time.Second, "dispatch"); err != nil {
			return err
		}
		report("hub memory after state.apply", rss(*hubPID))
	}
	if *hold > 0 {
		time.Sleep(*hold)
	}
	return nil
}

func (f *fleet) client(s *sim) *transport.Client {
	return &transport.Client{HubURL: f.hubURL, CA: f.ca, Cert: &s.pair, ServerName: f.serverName,
		Timeout: 60 * time.Second}
}

// connect holds one node's stream open, reconnecting as a node does.
func (f *fleet) connect(ctx context.Context, s *sim) {
	stream := f.client(s)
	returns := stream
	grains, _ := json.Marshal(map[string]any{"id": s.id, "os": "Simulated", "kernel": "simfleet"})
	for ctx.Err() == nil {
		opened := false
		_ = stream.SubscribeOpened(ctx, transport.SubscribeRequest{NodeID: s.id, Grains: grains, Version: "simfleet"},
			func() { opened = true; f.connected.Add(1) },
			func(msg transport.Message) error {
				if msg.T == transport.MsgJob {
					if !f.reuse {
						returns = f.client(s)
					}
					go f.answer(ctx, returns, s, msg)
				}
				return nil
			})
		if opened {
			f.connected.Add(-1)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// answer files the return a node would, with nothing behind it.
func (f *fleet) answer(ctx context.Context, c *transport.Client, s *sim, msg transport.Message) {
	// Only one measured job is in flight at a time, so any job that
	// arrives while one is being measured is it: a node can answer before
	// Submit has told the harness the jid.
	timed := f.measuring.Load().(string) != ""
	if timed {
		s.received.Store(time.Now().UnixNano())
	}
	body := json.RawMessage(`{}`)
	if msg.Fun == "test.ping" {
		body = json.RawMessage(`true`)
	}
	ret := &job.Return{JID: job.ID(msg.JID), NodeID: s.id, Fun: msg.Fun, Success: true,
		Return: body, StartTime: time.Now().UTC().Format(time.RFC3339Nano), Schema: job.ReturnSchema,
		NodeVersion: "simfleet"}
	if err := c.Return(ctx, ret); err != nil {
		f.returnErrs.Add(1)
		return
	}
	if timed {
		s.returned.Store(time.Now().UnixNano())
	}
}

func (f *fleet) operator(root string) (*transport.Client, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(root, "operator.crt"), filepath.Join(root, "operator.key"))
	if err != nil {
		return nil, err
	}
	return &transport.Client{HubURL: f.hubURL, CA: f.ca, Cert: &pair, ServerName: f.serverName,
		Timeout: 120 * time.Second}, nil
}

// measure submits fun to every simulated node and reports, against the
// SPEC 30 target, either how soon the returns arrived (`returns`, for
// test.ping: "95% of returns within 10 s") or how soon every node had the
// job (`dispatch`, for state.apply: "full dispatch within 30 s").
func (f *fleet) measure(ctx context.Context, op *transport.Client, fun, prefix string, target time.Duration, what string) error {
	for _, s := range f.sims {
		s.received.Store(0)
		s.returned.Store(0)
	}
	errsBefore := f.returnErrs.Load()
	// The jid is not known until Submit answers, and a fast node can
	// answer before it does; so every job is timed while measuring, and
	// the answer is attributed by jid afterwards.
	f.measuring.Store("*")
	t0 := time.Now()
	resp, err := op.Submit(ctx, transport.SubmitRequest{Target: prefix + "-*", Fun: fun})
	if err != nil {
		return fmt.Errorf("submitting %s: %w", fun, err)
	}
	submitted := time.Since(t0)
	f.measuring.Store(resp.JID)
	defer f.measuring.Store("")
	fmt.Printf("\n%s: jid %s, %d nodes matched, %d absent, the submit took %s\n",
		fun, resp.JID, len(resp.Nodes), len(resp.Absent), submitted.Round(time.Millisecond))

	wait := time.Now().Add(target*3 + 30*time.Second)
	for time.Now().Before(wait) {
		if count(f.sims, func(s *sim) int64 { return s.returned.Load() }) == len(f.sims) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	recv := latencies(f.sims, t0, func(s *sim) int64 { return s.received.Load() })
	rets := latencies(f.sims, t0, func(s *sim) int64 { return s.returned.Load() })
	fmt.Printf("  received by %d of %d: p50 %s, p95 %s, last %s\n", len(recv), len(f.sims),
		pct(recv, 50), pct(recv, 95), pct(recv, 100))
	fmt.Printf("  returned by %d of %d: p50 %s, p95 %s, last %s; %d return error(s)\n", len(rets), len(f.sims),
		pct(rets, 50), pct(rets, 95), pct(rets, 100), f.returnErrs.Load()-errsBefore)
	switch what {
	case "returns":
		verdict(fmt.Sprintf("95%% of returns within %s", target),
			len(rets) == len(f.sims) && pct(rets, 95) <= target, pct(rets, 95), len(rets), len(f.sims))
	case "dispatch":
		verdict(fmt.Sprintf("full dispatch within %s", target),
			len(recv) == len(f.sims) && pct(recv, 100) <= target, pct(recv, 100), len(recv), len(f.sims))
	}
	return nil
}

func verdict(target string, met bool, got time.Duration, have, want int) {
	word := "MET"
	if !met {
		word = "NOT MET"
	}
	fmt.Printf("  SPEC 30 %s: %s (%s, %d of %d nodes)\n", target, word, got, have, want)
}

func count(sims []*sim, at func(*sim) int64) int {
	n := 0
	for _, s := range sims {
		if at(s) != 0 {
			n++
		}
	}
	return n
}

func latencies(sims []*sim, t0 time.Time, at func(*sim) int64) []time.Duration {
	var out []time.Duration
	for _, s := range sims {
		if v := at(s); v != 0 {
			out = append(out, time.Duration(v-t0.UnixNano()))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	if i > len(sorted) {
		i = len(sorted)
	}
	return sorted[i-1].Round(time.Millisecond)
}

// rss is a process's resident memory, in KiB, through ps: the same
// command answers on Linux and FreeBSD, where /proc is not the same.
func rss(pid int) int64 {
	if pid == 0 {
		return -1
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return -1
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return -1
	}
	return v
}

func report(what string, kib int64) {
	if kib < 0 {
		return
	}
	fmt.Printf("%s: %.1f MiB resident\n", what, float64(kib)/1024)
}
