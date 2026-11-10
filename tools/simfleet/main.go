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
// A real node posts its returns on one client of its own, kept from one
// return to the next and separate from its stream, so that the hub holds
// two connections per node at most (`returnClient` in halite-node). The
// simulated nodes do the same by default. -reuse-client files returns on
// the stream's own connection instead, to measure what the second one
// costs; -client-per-return opens a new connection for every return and
// never closes it, which is what a node built before DIVERGENCE 5.641
// does, to measure a fleet that has not been upgraded.
//
// A simulated node reconnects at the node's pace and retries a return as
// the node does, through the same code (internal/nodelink), so that a
// reconnect storm or a hub near saturation sees what a fleet would give
// it.
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
//
// A fleet larger than one machine can hold is several drivers, each
// running its own range of nodes with -first and -n and submitting
// nothing; one `simfleet submit` sends a single job to all of them; and
// `simfleet merge` times that job across every driver's file, against
// the same SPEC 30 row a single-process run reports:
//
//	simfleet run -root DIR -hub H -first 1 -n 5000 -no-submit -out a.json
//	simfleet run -root DIR -hub H -first 5001 -n 5000 -no-submit -out b.json
//	simfleet submit -root DIR -hub H -fun test.ping -out ping.json
//	simfleet merge -submit ping.json a.json b.json
//
// -batch, -batch-wait and -batch-timeout, on run or submit, send the job
// a slice at a time as `halite-hub run --batch` does. A batched result has
// no SPEC 30 verdict, and reports instead the most nodes that had the job
// at once and the time from one slice's last return to the next slice's
// first arrival.
//
// `simfleet enroll-storm` is the one command that enrols over the wire:
// many machines asking for an identity at once, against the hub's
// per-source limiter and its bound on pending requests.
//
//	simfleet enroll-storm -root DIR -hub H -n 2000
//	simfleet setup -root DIR -enrollment token -token-uses 2000   (then restart the hub)
//	simfleet enroll-storm -root DIR -hub H -n 2000 -token-file DIR/bootstrap.token
//
// -json appends each result as one JSON line, for a run's archive.
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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/nodelink"
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
	case "submit":
		err = submitCmd(os.Args[2:])
	case "merge":
		err = mergeCmd(os.Args[2:])
	case "enroll-storm":
		err = stormCmd(os.Args[2:])
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
		"  simfleet run   -root DIR -hub ADDR -n N [flags] connect N simulated nodes and measure\n"+
		"  simfleet run   -root DIR -hub ADDR -first F -n N -no-submit -out FILE   one driver of several\n"+
		"  simfleet submit -root DIR -hub ADDR -fun F -out FILE   send one job to the whole fleet\n"+
		"  simfleet merge -submit FILE DRIVER.json...   time that job across every driver\n"+
		"  simfleet enroll-storm -root DIR -hub ADDR -n N [-token-file F]   N enrolment requests over the wire\n")
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
	enrollment := fs.String("enrollment", "manual", "the hub's enrollment_mode: manual, or token to also mint a bootstrap token for enroll-storm")
	tokenUses := fs.Int("token-uses", 2000, "with -enrollment token, how many names the token admits")
	tokenPrefix := fs.String("token-prefix", "storm", "with -enrollment token, the prefix of the names it admits (enroll-storm's -prefix)")
	_ = fs.Parse(args)
	if *root == "" {
		return errors.New("-root is required")
	}
	mode, err := keystore.ParseMode(*enrollment)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	for _, d := range []string{"pki", "state", "cache", "fleet"} {
		if err := fileperm.MkdirAll(filepath.Join(abs, d), 0o700); err != nil {
			return err
		}
	}
	hubYAML := fmt.Sprintf("state_dir: %s\npki_dir: %s\ncache_dir: %s\nlog_format: json\nlog_level: warn\nenrollment_mode: %s\n",
		filepath.Join(abs, "state"), filepath.Join(abs, "pki"), filepath.Join(abs, "cache"), mode)
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
	if mode == keystore.ModeToken {
		path, err := mintStormToken(store, abs, *tokenPrefix, *tokenUses)
		if err != nil {
			return err
		}
		fmt.Printf("enrollment_mode is token; a token for %d names %s-* is in %s\n", *tokenUses, *tokenPrefix, path)
	}

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
	if err := fileperm.WriteFile(base+".key", keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(base+".crt", certPEM, 0o644)
}

// ---- run ----

// sim is one simulated node; idx is its place in fleet.sims, and so in
// every jobRecord.
type sim struct {
	id   string
	idx  int
	pair tls.Certificate
}

type fleet struct {
	hubURL, serverName string
	ca                 *x509.Certificate
	reuse              bool
	perReturn          bool
	sims               []*sim
	connected          atomic.Int64
	jobs               *jobs
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	root := fs.String("root", "", "the directory `simfleet setup` wrote")
	hub := fs.String("hub", "127.0.0.1:4510", "the hub's address")
	serverName := fs.String("server-name", "localhost", "the name to verify in the hub's certificate")
	n := fs.Int("n", 1000, "how many simulated nodes to connect")
	first := fs.Int("first", 1, "the number of the first node to connect, so that several drivers can each run their own range")
	prefix := fs.String("prefix", "sim", "the node id prefix setup used")
	ramp := fs.Int("ramp", 1000, "nodes connected per second")
	connectWait := fs.Duration("connect-timeout", 5*time.Minute, "how long to wait for every node to connect before measuring anyway")
	hubPID := fs.Int("hub-pid", 0, "the hub's process id, to report its resident memory")
	ping := fs.Bool("ping", false, "time a test.ping to every simulated node")
	apply := fs.Bool("apply", false, "time a state.apply dispatch to every simulated node")
	reuse := fs.Bool("reuse-client", false, "file returns on the stream's own connection, rather than on a second one per node as a real node does")
	perReturn := fs.Bool("client-per-return", false, "file each return on a new connection, never closed, as a node before DIVERGENCE 5.641 does")
	hold := fs.Duration("hold", 0, "stay connected this long after measuring; with -no-submit, how long to run (0: until interrupted)")
	jsonOut := fs.String("json", "", "also append each measurement, as one JSON line, to this file")
	noSubmit := fs.Bool("no-submit", false, "submit nothing: answer whatever jobs arrive, and write what each node saw to -out for `simfleet merge`")
	out := fs.String("out", "", "with -no-submit, the file to write each job's arrival and return times to")
	driver := fs.String("driver", "", "this driver's name in -out (default: the host name)")
	batch := addBatchFlags(fs)
	_ = fs.Parse(args)
	if *root == "" {
		return errors.New("-root is required")
	}
	if *reuse && *perReturn {
		return errors.New("-reuse-client and -client-per-return are two different ways to file returns; give one")
	}
	if *noSubmit && (*ping || *apply) {
		return errors.New("-no-submit submits nothing, so it cannot also -ping or -apply; run `simfleet submit` instead")
	}
	if *noSubmit && *out == "" {
		return errors.New("-no-submit needs -out, or what the nodes saw is lost")
	}
	if *first < 1 {
		return errors.New("-first counts from 1")
	}
	if err := batch.check(); err != nil {
		return err
	}
	if *noSubmit && batch.on() {
		return errors.New("-no-submit submits nothing, so it has no batch; give -batch to `simfleet submit`")
	}
	if *driver == "" {
		*driver, _ = os.Hostname()
	}

	files := pki.Files{Dir: filepath.Join(*root, "pki")}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		return err
	}
	f := &fleet{hubURL: "https://" + *hub, serverName: *serverName, ca: ca, reuse: *reuse, perReturn: *perReturn, jobs: newJobs(*n)}
	for i := *first; i < *first+*n; i++ {
		id := nodeID(*prefix, i)
		base := filepath.Join(*root, "fleet", id)
		pair, err := tls.LoadX509KeyPair(base+".crt", base+".key")
		if err != nil {
			return fmt.Errorf("%s: %w; run simfleet setup with -n of at least %d", id, err, *first+*n-1)
		}
		f.sims = append(f.sims, &sim{id: id, idx: len(f.sims), pair: pair})
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
	deadline := time.Now().Add(*connectWait)
	for f.connected.Load() < int64(*n) && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	connectTook := time.Since(started)
	fmt.Printf("connected: %d of %d in %s\n", f.connected.Load(), *n, connectTook.Round(time.Millisecond))

	if *noSubmit {
		return f.serve(*hold, *out, driverFile{Driver: *driver, First: *first, N: *n, ConnectSeconds: connectTook.Seconds()})
	}

	time.Sleep(2 * time.Second)
	report(fmt.Sprintf("hub memory with %d nodes connected", f.connected.Load()), rss(*hubPID))

	op, err := f.operator(*root)
	if err != nil {
		return err
	}
	for _, m := range []struct {
		on  bool
		fun string
	}{{*ping, "test.ping"}, {*apply, "state.apply"}} {
		if !m.on {
			continue
		}
		res, err := f.measure(ctx, op, m.fun, *prefix, batch)
		if err != nil {
			return err
		}
		res.print(os.Stdout)
		if err := writeJSONLine(*jsonOut, res); err != nil {
			return err
		}
		report("hub memory after "+m.fun, rss(*hubPID))
	}
	if *hold > 0 {
		time.Sleep(*hold)
	}
	return nil
}

// serve is a driver of a multi-driver run: it holds its nodes connected,
// answers whatever arrives, and keeps -out current, until -hold runs out
// or it is told to stop. The file is rewritten whole every few seconds,
// and atomically, so a driver killed outright still leaves the last
// complete picture rather than half of one.
func (f *fleet) serve(hold time.Duration, out string, file driverFile) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	var until <-chan time.Time
	if hold > 0 {
		until = time.After(hold)
	}
	write := func() error {
		file.Connected = f.connected.Load()
		file.Jobs = f.jobs.snapshot(f.sims)
		b, err := json.Marshal(file)
		if err != nil {
			return err
		}
		return atomicfile.Write(out, append(b, '\n'), 0o644)
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := write(); err != nil {
				return err
			}
		case <-until:
			return write()
		case <-stop:
			return write()
		}
	}
}

func (f *fleet) client(s *sim) *transport.Client {
	return &transport.Client{HubURL: f.hubURL, CA: f.ca, Cert: &s.pair, ServerName: f.serverName,
		Timeout: 60 * time.Second}
}

// connect holds one node's stream open, reconnecting as a node does.
func (f *fleet) connect(ctx context.Context, s *sim) {
	stream := f.client(s)
	// The returns client, as halite-node's postReturns keeps it: one,
	// built once, not the stream's. Across a reconnect it is kept too,
	// because what a node compares (the hub, the CA, its certificate) is
	// the same for a simulated node for the whole run.
	returns := f.client(s)
	if f.reuse {
		returns = stream
	}
	grains, _ := json.Marshal(map[string]any{"id": s.id, "os": "Simulated", "kernel": "simfleet"})
	// The node's own pacer, with `hub_tries` at its default of 0: a
	// simulated node never gives up. It used to wait a fixed second after
	// every end, so a fleet that lost the hub together came back together,
	// once a second, which is a harsher storm than a fleet's.
	pace := nodelink.NewReconnect(0)
	for ctx.Err() == nil {
		opened := false
		pace.Attempting()
		err := stream.SubscribeOpened(ctx, transport.SubscribeRequest{NodeID: s.id, Grains: grains, Version: "simfleet"},
			func() { opened = true; pace.Opened(); f.connected.Add(1) },
			func(msg transport.Message) error {
				if msg.T == transport.MsgJob {
					if f.perReturn {
						returns = f.client(s)
					}
					go f.answer(ctx, returns, s, msg)
				}
				return nil
			})
		if opened {
			f.connected.Add(-1)
		}
		wait, _ := pace.Ended(err == nil)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// answer files the return a node would, with nothing behind it, and
// records both times under the job's own jid.
func (f *fleet) answer(ctx context.Context, c nodelink.Poster, s *sim, msg transport.Message) {
	rec := f.jobs.get(string(msg.JID), msg.Fun)
	rec.received[s.idx].Store(time.Now().UnixNano())
	body := json.RawMessage(`{}`)
	if msg.Fun == "test.ping" {
		body = json.RawMessage(`true`)
	}
	ret := &job.Return{JID: job.ID(msg.JID), NodeID: s.id, Fun: msg.Fun, Success: true,
		Return: body, StartTime: time.Now().UTC().Format(time.RFC3339Nano), Schema: job.ReturnSchema,
		NodeVersion: "simfleet"}
	// Retried as the node retries, so that a hub near saturation gets the
	// extra load a fleet would give it. A return error is one the node
	// would have given up on, or one the hub refused.
	if err := nodelink.PostReturn(ctx, c, ret, nil); err != nil {
		rec.returnErrs.Add(1)
		return
	}
	rec.returned[s.idx].Store(time.Now().UnixNano())
}

func (f *fleet) operator(root string) (*transport.Client, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(root, "operator.crt"), filepath.Join(root, "operator.key"))
	if err != nil {
		return nil, err
	}
	return &transport.Client{HubURL: f.hubURL, CA: f.ca, Cert: &pair, ServerName: f.serverName,
		Timeout: 120 * time.Second}, nil
}

// measure submits fun to every simulated node and waits for the returns,
// then summarizes them against the SPEC 30 row for fun. The clock starts
// before Submit is sent: a node can answer before Submit has returned,
// and an operator waits through the submit too. How long the submit
// itself took is reported beside it.
func (f *fleet) measure(ctx context.Context, op *transport.Client, fun, prefix string, batch batchFlags) (result, error) {
	t0 := time.Now()
	resp, err := op.Submit(ctx, batch.request(prefix+"-*", fun))
	t1 := time.Now()
	if err != nil {
		return result{}, fmt.Errorf("submitting %s: %w", fun, err)
	}
	rec := f.jobs.get(resp.JID, fun)
	target, _, ok := spec30Row(fun)
	if !ok {
		target = 10 * time.Second
	}
	wait := time.Now().Add(target*3 + 30*time.Second)
	if resp.Batch > 0 {
		wait = time.Now().Add(batch.patience())
	}
	for last := 0; time.Now().Before(wait); {
		n := filled(rec.returned)
		if n >= len(f.sims) {
			break
		}
		// A batched job is waited for from its last progress, not from
		// the submit (batchFlags.patience).
		if resp.Batch > 0 && n > last {
			wait = time.Now().Add(batch.patience())
			last = n
		}
		time.Sleep(200 * time.Millisecond)
	}
	sub := submitFile{JID: resp.JID, Fun: fun, Target: prefix + "-*", Matched: len(resp.Nodes), Absent: len(resp.Absent),
		SubmitStart: t0, SubmitEnd: t1, Batch: resp.Batch}
	return summarize(sub, len(f.sims), load(rec.received), load(rec.returned), rec.returnErrs.Load(), 1), nil
}

func filled(at []atomic.Int64) int {
	n := 0
	for i := range at {
		if at[i].Load() != 0 {
			n++
		}
	}
	return n
}

func load(at []atomic.Int64) []int64 {
	out := make([]int64, len(at))
	for i := range at {
		out[i] = at[i].Load()
	}
	return out
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
