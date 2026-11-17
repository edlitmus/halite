package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// The reactor row of SPEC 30: "5,000 events per second sustained, with
// bounded queue behaviour beyond it".
//
// A reaction is three things: the hub renders its SLS with the event in
// the template's context, parses what that rendered to, and dispatches
// each declaration it found as a job. "Events per second" can therefore
// mean two loads, and setup writes one reaction for each so that a
// number always says which:
//
//   - noop renders and parses, against the event's own data, and
//     declares nothing, so nothing is dispatched. It measures the
//     reactor: the bus, matching, the worker queues, rendering.
//   - ping dispatches `test.ping` to the node that posted the event, so
//     each event is also a job. It measures the reactor and the dispatch
//     behind it, and needs the nodes connected to answer (a `simfleet run
//     -no-submit` driver, or several).
//
// The harness counts what it offered and what the hub accepted. The
// hub's own counters, read with the operator certificate before and
// after, say what the reactor made of them: reactions finished
// (halite_reactor_duration_seconds_count), dropped from a full queue
// (halite_reactor_dropped_total), failed to dispatch
// (halite_reactor_failures_total), and events refused at the door by
// node_event_rate (halite_events_dropped_total). The queue depth is
// sampled every second during the run, and the run waits for the queue
// to drain before the after-reading, so that a reaction still queued is
// not counted as lost.

// reactionsFinished is the series that counts finished reactions: the
// count of the reactor's duration histogram. It is spelled as the
// family and its suffix, not as one string, because internal/metrics
// reads every quoted halite_ name in the source as a family the hub
// registers, and _count is a series of the histogram, not a family.
const reactionsFinished = "halite_reactor_duration_seconds" + "_count"

// reactorPrincipal is who the harness's reactions run as. SPEC 18.3: a
// reactor is subject to the policy like any caller, so setup binds it.
const reactorPrincipal = "reactor:simfleet"

// The tags a node posts. The hub files a node's event under
// halite/node/<id>/, which is what the reactor globs match.
var reactionTags = map[string]string{
	"noop": "simfleet/noop",
	"ping": "simfleet/ping",
}

var reactionFiles = map[string]string{
	"noop": `# simfleet: the reactor row's own reaction. It reads the event, so the
# render does the work a real reaction's does, and declares nothing, so
# nothing is dispatched.
{%- set node = data['id'] %}
{%- set seq = data.get('seq', 0) %}
`,
	"ping": `# simfleet: one test.ping, to the node that posted the event.
{%- set node = data['id'] %}
ping_{{ node }}_{{ data.get('seq', 0) }}:
  local.test.ping:
    - tgt: {{ node }}
`,
}

// writeReactions writes the reaction files into dir and answers with the
// reactor: section of hub.yaml that runs them.
func writeReactions(dir string) (string, error) {
	var b strings.Builder
	b.WriteString("reactor:\n")
	for _, name := range []string{"noop", "ping"} {
		path := filepath.Join(dir, name+".sls")
		if err := os.WriteFile(path, []byte(reactionFiles[name]), 0o644); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  - tag: 'halite/node/*/%s'\n    sls:\n      - %s\n    principal: '%s'\n",
			reactionTags[name], path, reactorPrincipal)
	}
	return b.String(), nil
}

// eventsResult is one run, as `events` prints it and, with -json,
// writes it.
type eventsResult struct {
	Reaction    string  `json:"reaction"`
	Nodes       int     `json:"nodes"`
	Rate        float64 `json:"rate"`
	Seconds     float64 `json:"seconds"`
	Concurrency int     `json:"concurrency"`
	// Offered is how many events the harness put on the wire. NotOffered
	// is how many its schedule called for while every poster was busy:
	// the harness, not the hub, fell behind, and a run with any is a run
	// whose offered rate was not the one asked for.
	Offered    int64 `json:"offered"`
	NotOffered int64 `json:"not_offered"`
	// What the hub answered.
	Accepted    int64   `json:"accepted"`
	RateLimited int64   `json:"rate_limited"`
	Failed      int64   `json:"failed"`
	Latency     latency `json:"latency"`
	// AcceptedPerSecond is over the whole run; SlowestSecond is the
	// fewest accepted of the events due in any one whole second of it,
	// which is what "sustained" is about: equal to -rate when every
	// event of every second was accepted.
	AcceptedPerSecond float64 `json:"accepted_per_second"`
	SlowestSecond     int64   `json:"slowest_second"`
	// The hub's own counters, after minus before.
	Published       float64 `json:"published"`
	Reacted         float64 `json:"reacted"`
	ReactorDropped  float64 `json:"reactor_dropped"`
	ReactorFailures float64 `json:"reactor_failures"`
	NodeRateDropped float64 `json:"node_rate_dropped"`
	MaxQueueDepth   float64 `json:"max_queue_depth"`
	// WarmSeconds is how long connecting every node took, before the
	// schedule started; Handshakes is how many TLS handshakes posts
	// started during it, which warming should make none. See warm.
	WarmSeconds float64 `json:"warm_seconds"`
	Handshakes  int64   `json:"handshakes_during_run"`
	// DrainSeconds is how long after the last post the queue took to
	// empty; Drained is false when it had not within -drain-timeout.
	DrainSeconds float64  `json:"drain_seconds"`
	Drained      bool     `json:"drained"`
	Errors       []string `json:"errors,omitempty"`
}

func eventsCmd(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	root := fs.String("root", "", "the directory `simfleet setup` wrote")
	hub := fs.String("hub", "127.0.0.1:4510", "the hub's address")
	serverName := fs.String("server-name", "localhost", "the name to verify in the hub's certificate")
	n := fs.Int("n", 1000, "how many simulated nodes post events")
	first := fs.Int("first", 1, "the number of the first node, so that several drivers can each post from their own range")
	prefix := fs.String("prefix", "sim", "the node id prefix setup used")
	rate := fs.Float64("rate", 100, "events per second, in total, across every node")
	duration := fs.Duration("duration", 10*time.Second, "how long to post for")
	reaction := fs.String("reaction", "noop", "noop (rendered, nothing dispatched) or ping (a test.ping to the posting node)")
	concurrency := fs.Int("concurrency", 256, "posts in flight at once")
	drainTimeout := fs.Duration("drain-timeout", 60*time.Second, "how long to wait for the reactor's queue to empty after the last post")
	jsonOut := fs.String("json", "", "also append the result, as one JSON line, to this file")
	_ = fs.Parse(args)
	if *root == "" {
		return errors.New("-root is required")
	}
	tag, ok := reactionTags[*reaction]
	if !ok {
		return fmt.Errorf("-reaction %q: it is noop or ping", *reaction)
	}
	if *rate <= 0 || *duration <= 0 || *n < 1 || *first < 1 || *concurrency < 1 {
		return errors.New("-rate, -duration, -n, -first and -concurrency must all be positive")
	}

	files := pki.Files{Dir: filepath.Join(*root, "pki")}
	ca, err := files.ReadCert(pki.CACertFile)
	if err != nil {
		return err
	}
	f := &fleet{hubURL: "https://" + *hub, serverName: *serverName, ca: ca}
	clients := make([]*transport.Client, 0, *n)
	for i := *first; i < *first+*n; i++ {
		id := nodeID(*prefix, i)
		base := filepath.Join(*root, "fleet", id)
		pair, err := tls.LoadX509KeyPair(base+".crt", base+".key")
		if err != nil {
			return fmt.Errorf("%s: %w; run simfleet setup with -n of at least %d", id, err, *first+*n-1)
		}
		clients = append(clients, f.client(&sim{id: id, pair: pair}))
	}
	op, err := f.operator(*root)
	if err != nil {
		return err
	}
	ctx := context.Background()
	warmStart := time.Now()
	if err := warm(ctx, clients, *concurrency); err != nil {
		return err
	}
	warmed := time.Since(warmStart)
	before, err := scrape(ctx, op)
	if err != nil {
		return fmt.Errorf("reading the hub's metrics before the run: %w", err)
	}

	res := eventsResult{Reaction: *reaction, Nodes: *n, Rate: *rate, Seconds: duration.Seconds(), Concurrency: *concurrency,
		WarmSeconds: warmed.Seconds()}
	r := postEvents(ctx, clients, tag, *rate, *duration, *concurrency, func() (float64, error) {
		m, err := scrape(ctx, op)
		return m["halite_reactor_queue_depth"], err
	})
	res.Offered, res.NotOffered = r.offered, r.notOffered
	res.Accepted, res.RateLimited, res.Failed = r.accepted.Load(), r.rateLimited.Load(), r.failed.Load()
	res.Latency = dist(r.latencies())
	res.AcceptedPerSecond = float64(res.Accepted) / duration.Seconds()
	res.SlowestSecond = r.slowestSecond()
	res.MaxQueueDepth = r.maxQueue
	res.Handshakes = r.handshakes.Load()
	res.Errors = r.errs

	// Wait for the reactor to finish what it was given: every accepted
	// event is either reacted to or dropped, and the queue is empty.
	drainStart := time.Now()
	var after map[string]float64
	for {
		after, err = scrape(ctx, op)
		if err != nil {
			return fmt.Errorf("reading the hub's metrics after the run: %w", err)
		}
		res.MaxQueueDepth = max(res.MaxQueueDepth, after["halite_reactor_queue_depth"])
		done := after[reactionsFinished] - before[reactionsFinished] +
			after["halite_reactor_dropped_total"] - before["halite_reactor_dropped_total"]
		if after["halite_reactor_queue_depth"] == 0 && done >= float64(res.Accepted) {
			res.Drained = true
			break
		}
		if time.Since(drainStart) > *drainTimeout {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	res.DrainSeconds = time.Since(drainStart).Seconds()
	delta := func(name string) float64 { return after[name] - before[name] }
	res.Published = delta("halite_events_published_total")
	res.Reacted = delta(reactionsFinished)
	res.ReactorDropped = delta("halite_reactor_dropped_total")
	res.ReactorFailures = delta("halite_reactor_failures_total")
	res.NodeRateDropped = delta("halite_events_dropped_total")

	res.print(os.Stdout)
	return writeJSONLine(*jsonOut, res)
}

// posting is what postEvents counted.
type posting struct {
	offered, notOffered           int64
	accepted, rateLimited, failed atomic.Int64
	handshakes                    atomic.Int64
	mu                            sync.Mutex
	lat                           []time.Duration
	perSecond                     []atomic.Int64
	maxQueue                      float64
	errs                          []string
}

func (p *posting) latencies() []time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]time.Duration(nil), p.lat...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// slowestSecond is the fewest events accepted in any whole second of
// the run. The last second is left out when it is not a whole one.
func (p *posting) slowestSecond() int64 {
	if len(p.perSecond) == 0 {
		return 0
	}
	least := p.perSecond[0].Load()
	for i := range p.perSecond {
		least = min(least, p.perSecond[i].Load())
	}
	return least
}

// warm connects every node to the hub before the schedule starts, with
// one health request each through the node's own client, so that the
// connection, TLS handshake and all, is open and idle in that client's
// pool when the first event is due. The client speaks HTTP/2, so that one
// connection carries all of the node's posts, and the hub keeps an idle
// one for transport.IdleStreamTimeout.
//
// Without it, each node's first post paid its handshake inside the
// schedule. On an emulated FreeBSD CI runner four of those took about
// 0.8 s, every poster was busy meanwhile, and a quarter of a 40 a second
// run's events were not offered (DIVERGENCE 5.673); on AWS, 256 posters
// opening 8,000 connections fell behind the same way (5.668.1). A real
// node posting an event is already connected, so a handshake in the
// schedule was measuring the harness starting, not the hub.
func warm(ctx context.Context, clients []*transport.Client, concurrency int) error {
	work := make(chan *transport.Client)
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if _, err := c.Health(ctx); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}
		}()
	}
	for _, c := range clients {
		work <- c
	}
	close(work)
	wg.Wait()
	select {
	case err := <-errs:
		return fmt.Errorf("connecting the nodes before the run: %w", err)
	default:
		return nil
	}
}

// postEvents offers rate events a second for d, open loop: the schedule
// does not wait for the hub. Each tick hands the events due to a pool of
// posters; an event due while every poster is busy is counted as not
// offered rather than queued in the harness, because a queue here would
// turn the hub falling behind into the harness posting late, and hide
// it. Events go to the nodes in turn, each from its own client, kept
// from one post to the next as a node keeps its connection.
func postEvents(ctx context.Context, clients []*transport.Client, tag string, rate float64, d time.Duration,
	concurrency int, queueDepth func() (float64, error)) *posting {
	p := &posting{perSecond: make([]atomic.Int64, int(d/time.Second))}
	type ev struct {
		node int
		seq  int64
	}
	work := make(chan ev, concurrency)
	// Every TLS handshake a post starts is counted, so that a run says
	// whether warm did its job rather than leaving it to be assumed.
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		TLSHandshakeStart: func() { p.handshakes.Add(1) },
	})
	start := time.Now()
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range work {
				data, _ := json.Marshal(map[string]int64{"seq": e.seq})
				t0 := time.Now()
				_, err := clients[e.node].SendEvent(ctx, transport.EventRequest{Tag: tag, Data: data})
				took := time.Since(t0)
				var status *transport.StatusError
				var refused *transport.RefusedError
				switch {
				case err == nil:
					p.accepted.Add(1)
					// Counted against the second the event was due in, not
					// the second its answer came: by arrival, a run that
					// kept up exactly shows one second a few short and the
					// next a few over, and the row would read as missed.
					if s := int(float64(e.seq) / rate); s < len(p.perSecond) {
						p.perSecond[s].Add(1)
					}
				case errors.As(err, &status) && status.Status == 429,
					errors.As(err, &refused) && refused.Status == 429:
					p.rateLimited.Add(1)
				default:
					p.failed.Add(1)
				}
				p.mu.Lock()
				p.lat = append(p.lat, took)
				if err != nil && len(p.errs) < 5 {
					p.errs = append(p.errs, err.Error())
				}
				p.mu.Unlock()
			}
		}()
	}

	// The queue depth, once a second, from the hub's own gauge.
	sampled := make(chan struct{})
	stopSampling := make(chan struct{})
	go func() {
		defer close(sampled)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopSampling:
				return
			case <-t.C:
				if q, err := queueDepth(); err == nil {
					p.maxQueue = max(p.maxQueue, q)
				}
			}
		}
	}()

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var seq int64
	for now := range tick.C {
		elapsed := now.Sub(start)
		if elapsed > d {
			elapsed = d
		}
		due := int64(rate * elapsed.Seconds())
		for ; seq < due; seq++ {
			select {
			case work <- ev{node: int(seq % int64(len(clients))), seq: seq}:
				p.offered++
			default:
				p.notOffered++
			}
		}
		if elapsed >= d {
			break
		}
	}
	close(work)
	wg.Wait()
	close(stopSampling)
	<-sampled
	return p
}

// scrape reads the hub's metrics and sums each series by name, across
// its labels: the harness wants how many reactions finished, not how
// many under each tag prefix.
func scrape(ctx context.Context, op *transport.Client) (map[string]float64, error) {
	text, err := op.Metrics(ctx)
	if err != nil {
		return nil, err
	}
	return parseExposition(strings.NewReader(text))
}

func parseExposition(r io.Reader) (map[string]float64, error) {
	out := map[string]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest := line, ""
		if i := strings.IndexAny(line, "{ "); i >= 0 {
			name, rest = line[:i], line[i:]
		}
		if strings.HasPrefix(rest, "{") {
			end := strings.LastIndex(rest, "}")
			if end < 0 {
				return nil, fmt.Errorf("metrics: an unclosed label set: %q", line)
			}
			rest = rest[end+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return nil, fmt.Errorf("metrics: no value: %q", line)
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return nil, fmt.Errorf("metrics: %q: %w", line, err)
		}
		out[name] += v
	}
	return out, sc.Err()
}

// spec30ReactorRate is the row's number.
const spec30ReactorRate = 5000

// reactorRowFailures is why a run does not meet the reactor row, or
// nothing when it does. The run must have been offered at least the
// row's rate; the caller does not judge one that was not.
//
// The row is 5,000 events a second sustained, so what is judged is the
// slowest whole second of accepted events against 5,000, not against
// -rate. At -rate 5,000 the two are the same thing, and any event not
// offered leaves its second short. Above it they are not: a run offered
// 20,000 a second whose one posting driver managed 7,305 in its slowest
// second has shown the row, and an event the harness could not offer
// says something about the harness, not the hub. Failing that run on
// not_offered, as this did when 5.668.1 found it, printed NOT MET for the two
// runs that went furthest past the row. Not offered still fails a run
// at the row's own rate, where it is the harness failing to make the
// measurement and the slowest second may not show it: a -duration with
// a fraction of a second has events due outside any whole second.
//
// Everything the hub accepted must also have been reacted to, with
// nothing dropped from the queue, no reaction failing, and the queue
// drained after the last post: a hub that accepts 5,000 a second and
// falls behind reacting to them has not sustained the rate.
func (r eventsResult) reactorRowFailures() []string {
	var why []string
	if r.Rate <= spec30ReactorRate && r.NotOffered > 0 {
		why = append(why, fmt.Sprintf("%d events not offered at the row's own rate", r.NotOffered))
	}
	if r.SlowestSecond < spec30ReactorRate {
		why = append(why, fmt.Sprintf("slowest second %d accepted, under %d", r.SlowestSecond, spec30ReactorRate))
	}
	if r.Reacted < float64(r.Accepted) {
		why = append(why, fmt.Sprintf("reacted to %.0f of %d accepted", r.Reacted, r.Accepted))
	}
	if r.ReactorDropped > 0 {
		why = append(why, fmt.Sprintf("%.0f dropped from the queue", r.ReactorDropped))
	}
	if r.ReactorFailures > 0 {
		why = append(why, fmt.Sprintf("%.0f reactions failed", r.ReactorFailures))
	}
	if !r.Drained {
		why = append(why, "the queue did not drain")
	}
	return why
}

func (r eventsResult) print(w io.Writer) {
	fmt.Fprintf(w, "\nevents (%s reaction): %d nodes, %.0f a second for %s, %d posters\n",
		r.Reaction, r.Nodes, r.Rate, secs(r.Seconds), r.Concurrency)
	fmt.Fprintf(w, "  offered %d (%d not offered: every poster was busy)\n", r.Offered, r.NotOffered)
	fmt.Fprintf(w, "  accepted %d, %.0f a second, slowest whole second %d; rate limited %d; failed %d\n",
		r.Accepted, r.AcceptedPerSecond, r.SlowestSecond, r.RateLimited, r.Failed)
	fmt.Fprintf(w, "  post latency p50 %s, p95 %s, last %s\n", secs(r.Latency.P50), secs(r.Latency.P95), secs(r.Latency.Last))
	fmt.Fprintf(w, "  nodes connected in %s before the schedule; TLS handshakes during it %d\n", secs(r.WarmSeconds), r.Handshakes)
	fmt.Fprintf(w, "  hub: published %.0f, reacted %.0f, dropped from the queue %.0f, reactions failed %.0f, refused by node_event_rate %.0f\n",
		r.Published, r.Reacted, r.ReactorDropped, r.ReactorFailures, r.NodeRateDropped)
	drained := fmt.Sprintf("drained %s after the last post", secs(r.DrainSeconds))
	if !r.Drained {
		drained = fmt.Sprintf("NOT drained %s after the last post", secs(r.DrainSeconds))
	}
	fmt.Fprintf(w, "  queue depth at most %.0f; %s\n", r.MaxQueueDepth, drained)
	for _, e := range r.Errors {
		fmt.Fprintf(w, "  error: %s\n", e)
	}
	if r.Rate < spec30ReactorRate {
		fmt.Fprintf(w, "  SPEC 30 reactor row: not judged, because -rate is under %d\n", spec30ReactorRate)
		return
	}
	reasons := r.reactorRowFailures()
	verdict := "MET"
	if len(reasons) > 0 {
		verdict = "NOT MET: " + strings.Join(reasons, "; ")
	}
	fmt.Fprintf(w, "  SPEC 30 reactor row (%d events a second sustained, %s reaction): %s\n", spec30ReactorRate, r.Reaction, verdict)
}
