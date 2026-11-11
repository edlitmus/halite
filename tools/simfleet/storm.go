package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// An enrolment storm is many machines asking the hub for an identity at
// once: a rack racked, an autoscaling group scaled out, a fleet rebuilt.
// `simfleet setup` issues its identities in process, through
// keystore.Authority, because the steps that measure a connected fleet
// should not measure an operator accepting 20,000 requests; so nothing
// in the harness ever reached `POST /v1/enroll` (limit L6 of
// docs/simfleet-aws-plan.md), which is the one route that reads the
// input of a caller with no certificate, and the one with a per-source
// limiter and a bound on pending records in front of it.
//
// enroll-storm makes those calls over the wire, as `halite-node enroll`
// does: a new P-256 key and a certificate request per name, on a
// connection of its own with no client certificate, through the same
// transport.Client.Enroll. It asks once per name and does not wait or
// retry, which is `halite-node enroll` without --wait: what is measured
// is what the hub answered, and a retry would measure the harness's
// patience instead.
//
// In manual mode every name it sends is new, so each answer the hub
// accepts makes a pending record, and from one driver address the
// limiter admits enroll_source_burst (32) and then one every 10 s. In
// token mode (`simfleet setup -enrollment token`) an answer is a
// certificate, and a token-mode bootstrap is not metered at all. That
// was said here before it was true: until DIVERGENCE 5.656 the hub
// charged each token request on entry and refunded it once issued, so
// with more in flight than the burst it refused most of them (5.279,
// 90 issued of 2,000 at 64 at a time), and this harness's own test sent
// four at a time and never saw it. Now a request whose token would
// spend is not charged, up to one in flight per use the token has left.
//
// The names are -prefix's, "storm" by default, so that they do not
// collide with the identities `setup` issued to the connected fleet; a
// second storm against the same hub needs -first past the first one's
// names, or every answer is "pending" for a record that already exists,
// which the limiter refunds.

// stormOutcomes are what one enrolment request can come to, as the hub's
// own halite_hub_enrollments_total counts them, and "error" for an
// answer that never arrived.
var stormOutcomes = []string{"issued", "pending", "rate_limited", "refused", "failed", "error"}

// stormResult is one storm, as `enroll-storm` prints it and, with -json,
// writes it.
type stormResult struct {
	Hub         string             `json:"hub"`
	Mode        string             `json:"mode"`
	Requests    int                `json:"requests"`
	Concurrency int                `json:"concurrency"`
	Seconds     float64            `json:"seconds"`
	Outcomes    map[string]int     `json:"outcomes"`
	Latency     map[string]latency `json:"latency"`
	// Errors holds the first few answers of the "failed" and "error"
	// kinds, as text: a storm that failed for a reason of the
	// harness's own, a certificate the driver does not trust, say,
	// must say what the reason was.
	Errors []string `json:"errors,omitempty"`
}

func stormCmd(args []string) error {
	fs := flag.NewFlagSet("enroll-storm", flag.ExitOnError)
	root := fs.String("root", "", "the directory `simfleet setup` wrote, for the hub's CA")
	hub := fs.String("hub", "127.0.0.1:4510", "the hub's address")
	serverName := fs.String("server-name", "localhost", "the name to verify in the hub's certificate")
	n := fs.Int("n", 2000, "how many enrolment requests to make, each for a new name")
	first := fs.Int("first", 1, "the number of the first name, so that a second storm can use new names")
	prefix := fs.String("prefix", "storm", "the node id prefix; not setup's, whose names are already enrolled")
	concurrency := fs.Int("concurrency", 64, "requests in flight at once")
	tokenFile := fs.String("token-file", "", "a bootstrap token to present, as `setup -enrollment token` wrote it (default: none, which is manual mode's request)")
	jsonOut := fs.String("json", "", "also append the result, as one JSON line, to this file")
	_ = fs.Parse(args)
	if *root == "" {
		return errors.New("-root is required")
	}
	if *n < 1 || *concurrency < 1 || *first < 1 {
		return errors.New("-n, -concurrency and -first are counts from 1")
	}
	ca, err := pki.Files{Dir: filepath.Join(*root, "pki")}.ReadCert(pki.CACertFile)
	if err != nil {
		return err
	}
	token := ""
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	res := storm(context.Background(), ca, "https://"+*hub, *serverName, *prefix, *first, *n, *concurrency, token)
	res.print(os.Stdout)
	return writeJSONLine(*jsonOut, res)
}

// storm makes n enrolment requests, concurrency at a time, and counts
// what the hub answered.
func storm(ctx context.Context, ca *x509.Certificate, hubURL, serverName, prefix string, first, n, concurrency int, token string) stormResult {
	res := stormResult{Hub: hubURL, Mode: "manual", Requests: n, Concurrency: concurrency,
		Outcomes: map[string]int{}, Latency: map[string]latency{}}
	if token != "" {
		res.Mode = "token"
	}
	type answer struct {
		outcome string
		took    time.Duration
		err     error
	}
	answers := make([]answer, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	started := time.Now()
	for range min(concurrency, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				t0 := time.Now()
				outcome, err := enrollOnce(ctx, ca, hubURL, serverName, nodeID(prefix, first+i), token)
				answers[i] = answer{outcome, time.Since(t0), err}
			}
		}()
	}
	wg.Wait()
	res.Seconds = time.Since(started).Seconds()

	by := map[string][]time.Duration{}
	for _, a := range answers {
		res.Outcomes[a.outcome]++
		by[a.outcome] = append(by[a.outcome], a.took)
		if a.err != nil && (a.outcome == "failed" || a.outcome == "error") && len(res.Errors) < 5 {
			res.Errors = append(res.Errors, a.err.Error())
		}
	}
	for outcome, took := range by {
		sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
		res.Latency[outcome] = dist(took)
	}
	return res
}

// enrollOnce is one machine's first `halite-node enroll`: a new key, a
// connection of its own, one request, and the connection closed, since a
// machine that has enrolled holds no enrolment connection open.
func enrollOnce(ctx context.Context, ca *x509.Certificate, hubURL, serverName, id, token string) (string, error) {
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		return "error", err
	}
	c := &transport.Client{HubURL: hubURL, CA: ca, ServerName: serverName, Timeout: 60 * time.Second}
	defer c.CloseIdleConnections()
	_, err = c.Enroll(ctx, key, id, token)
	return enrollOutcome(err), err
}

// enrollOutcome names an answer as the hub's metric does.
func enrollOutcome(err error) string {
	if err == nil {
		return "issued"
	}
	if errors.Is(err, transport.ErrPending) {
		return "pending"
	}
	if _, limited := transport.RateLimited(err); limited {
		return "rate_limited"
	}
	var refused *transport.RefusedError
	if errors.As(err, &refused) && refused.Code == transport.CodeRefused {
		return "refused"
	}
	var status *transport.StatusError
	if errors.As(err, &refused) || errors.As(err, &status) {
		return "failed"
	}
	return "error"
}

func (r stormResult) print(w io.Writer) {
	fmt.Fprintf(w, "enroll-storm: %d requests, %d at a time, %s mode, in %s\n",
		r.Requests, r.Concurrency, r.Mode, secs(r.Seconds))
	for _, o := range stormOutcomes {
		c := r.Outcomes[o]
		if c == 0 {
			continue
		}
		l := r.Latency[o]
		fmt.Fprintf(w, "  %-12s %6d  answered in p50 %s, p95 %s, max %s\n", o, c, secs(l.P50), secs(l.P95), secs(l.Last))
	}
	if r.Seconds > 0 && r.Outcomes["issued"] > 0 {
		fmt.Fprintf(w, "  %.1f certificates issued a second\n", float64(r.Outcomes["issued"])/r.Seconds)
	}
	for _, e := range r.Errors {
		fmt.Fprintf(w, "  error: %s\n", e)
	}
}

// mintStormToken is `setup -enrollment token`'s token: one that admits
// uses names under prefix, for a day, which is the longest SPEC 7.3
// allows. The secret is written to ROOT/bootstrap.token, readable by the
// owner only, as an operator would keep it.
func mintStormToken(store *keystore.Store, root, prefix string, uses int) (string, error) {
	_, secret, err := store.MintToken(keystore.TokenOptions{
		TTL: keystore.MaxTokenTTL, NodeGlob: prefix + "-*", MaxUses: uses,
		Comment: "simfleet enroll-storm",
	}, time.Now())
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "bootstrap.token")
	return path, atomicfile.Write(path, []byte(secret+"\n"), 0o600)
}
