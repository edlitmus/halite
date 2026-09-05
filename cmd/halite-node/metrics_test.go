package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
)

// nodeMetricsFor builds the recorder a configuration asks for.
func nodeMetricsFor(t *testing.T, body string) *nodeMetrics {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(config.Node, config.LoadOptions{
		Path:         writeConfig(t, dir, body),
		AllowMissing: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return newNodeMetrics(cfg)
}

// A node dials the hub and is dialled by nothing. The endpoint is the
// one exception and it is opt-in: a port on every managed machine is a
// decision an operator makes, not one that arrives with an upgrade.
func TestANodeRecordsNothingWithoutTheSetting(t *testing.T) {
	m := nodeMetricsFor(t, "id: web1.example\n")
	if m.on() {
		t.Fatal("a node with no metrics_listen is recording")
	}
	// Every call site is unconditional, so all of them have to be safe
	// on a recorder that is off.
	m.countJob(&job.Return{Fun: "test.ping", Success: true}, time.Second)
	m.countRefusal(ErrQueueFull)
	m.observeExtension("thing", "succeeded", time.Second)
	m.observeBeacon("dropped", "diskusage", 3)
	m.observeHubRequest("/v1/return", 200, time.Second)
	m.observeStateCompile(time.Second)
	m.observeStateRun(time.Second)
	m.countDroppedReturn()
	m.countConnect()
	m.countDisconnect()
	m.countScheduleRun("nightly")
	m.gauge("halite_node_job_queue_depth", "unused", func() float64 { return 1 })
	// And serving is a no-op rather than a listener on a default port.
	m.serve(context.Background(), func(err error) {
		t.Fatalf("a recorder that is off tried to serve: %v", err)
	}, func(addr string) {
		t.Fatalf("a recorder that is off listened on %s", addr)
	})
}

// `metrics: false` turns it off even with an address set, the same way
// it does on the hub and the API.
func TestMetricsFalseTurnsANodeOffWithAnAddressSet(t *testing.T) {
	m := nodeMetricsFor(t, "metrics: false\nmetrics_listen: ':4512'\n")
	if m.on() {
		t.Fatal("metrics: false left the node recording")
	}
}

// There is no plaintext metrics endpoint. A node's exposition says
// which functions ran, which extensions, and when a deployment went
// out; serving that unencrypted because a certificate was not
// configured would be the wrong way to fail.
func TestAnAddressWithNoCertificateServesNothing(t *testing.T) {
	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n")
	if !m.on() {
		t.Fatal("the recorder is off; this test would pass for the wrong reason")
	}
	var reported error
	m.serve(context.Background(), func(err error) { reported = err }, func(addr string) {
		t.Fatalf("it listened on %s with no certificate", addr)
	})
	if reported == nil {
		t.Fatal("no certificate, no listener, and nothing said about it")
	}
	if !strings.Contains(reported.Error(), "metrics_tls_cert") {
		t.Errorf("the error does not name the setting: %v", reported)
	}
	if !strings.Contains(reported.Error(), "plaintext") {
		t.Errorf("the error does not say why there is no fallback: %v", reported)
	}
}

// A certificate that is not there is reported and is not fatal: a node
// that refused to start over its metrics certificate is one no
// highstate could reach to fix the certificate.
func TestAMissingCertificateIsReportedAndNotFatal(t *testing.T) {
	dir := t.TempDir()
	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n"+
		"metrics_tls_cert: "+filepath.Join(dir, "absent.crt")+"\n"+
		"metrics_tls_key: "+filepath.Join(dir, "absent.key")+"\n")
	var reported error
	m.serve(context.Background(), func(err error) { reported = err }, nil)
	if reported == nil {
		t.Fatal("a missing certificate was not reported")
	}
	if !strings.Contains(reported.Error(), "metrics certificate") {
		t.Errorf("the error does not say what could not be read: %v", reported)
	}
}

// What a scraper actually gets.
func TestTheEndpointServesAnExposition(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	certFile, keyFile := ca.serving(t, dir, "node")

	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n"+
		"metrics_tls_cert: "+certFile+"\nmetrics_tls_key: "+keyFile+"\n")
	m.countJob(&job.Return{Fun: "state.apply", Success: true}, 12*time.Second)
	m.countJob(&job.Return{Fun: "state.apply", Success: false}, time.Second)
	m.countRefusal(&job.Refusal{Reason: job.ReasonReplayed})
	m.countDroppedReturn()
	m.observeStateCompile(400 * time.Millisecond)
	m.observeStateRun(11 * time.Second)
	m.observeExtension("inventory", "timed_out", 60*time.Second)
	m.observeBeacon("dropped", "diskusage", 7)
	m.observeHubRequest("/v1/return", 200, 30*time.Millisecond)
	m.countConnect()

	addr := serveForTest(t, m)
	client := ca.client(t, nil)

	res, err := client.Get("https://" + addr + "/v1/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the scrape answered %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("the content type is %q", got)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, want := range []string{
		`halite_build_info{component="node"`,
		`halite_node_jobs_total{fun="state.apply",result="succeeded"} 1`,
		`halite_node_jobs_total{fun="state.apply",result="failed"} 1`,
		`halite_node_jobs_refused_total{reason="replayed"} 1`,
		"halite_node_returns_dropped_total 1",
		"halite_state_compile_duration_seconds_count 1",
		"halite_state_run_duration_seconds_sum 11",
		`halite_ext_invocations_total{name="inventory",result="timed_out"} 1`,
		`halite_ext_timeouts_total{name="inventory"} 1`,
		`halite_beacon_dropped_total{beacon="diskusage"} 7`,
		`halite_node_hub_requests_total{route="/v1/return",code="200"} 1`,
		"halite_node_connected 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q", want)
		}
	}
	checkExposition(t, body)
}

// A scrape target and not a second control surface on a managed
// machine. An unrouted path says nothing about what else might be here.
func TestTheEndpointServesNothingElse(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	certFile, keyFile := ca.serving(t, dir, "node")
	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n"+
		"metrics_tls_cert: "+certFile+"\nmetrics_tls_key: "+keyFile+"\n")
	addr := serveForTest(t, m)
	client := ca.client(t, nil)

	for _, path := range []string{"/", "/v1/jobs", "/metrics", "/v1/metrics/../../etc/passwd"} {
		res, err := client.Get("https://" + addr + path)
		if err != nil {
			// A path the client normalises away before sending is not
			// this endpoint's to refuse.
			continue
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusOK && strings.Contains(string(body), "halite_") {
			t.Errorf("%s served an exposition", path)
		}
	}
}

// With metrics_client_ca set, a scraper that presents nothing is
// refused at the handshake. Left empty the port is served to anyone who
// can reach it, which is a decision to make deliberately.
func TestAClientCertificateCanBeRequired(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	certFile, keyFile := ca.serving(t, dir, "node")
	caFile := ca.certFile(t, dir)

	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n"+
		"metrics_tls_cert: "+certFile+"\nmetrics_tls_key: "+keyFile+"\n"+
		"metrics_client_ca: "+caFile+"\n")
	if !m.requiresClientCert() {
		t.Fatal("metrics_client_ca is set and the endpoint does not require one")
	}
	addr := serveForTest(t, m)

	if _, err := ca.client(t, nil).Get("https://" + addr + "/v1/metrics"); err == nil {
		t.Error("a scraper with no client certificate was served")
	}

	scraper := ca.clientPair(t, dir, "prometheus")
	res, err := ca.client(t, &scraper).Get("https://" + addr + "/v1/metrics")
	if err != nil {
		t.Fatalf("a scraper with a certificate from the CA was refused: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the scrape answered %d", res.StatusCode)
	}
}

// A client CA file that names no certificate is a configuration error,
// not a listener that quietly serves everyone.
func TestAnUnreadableClientCAServesNothing(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	certFile, keyFile := ca.serving(t, dir, "node")
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n"+
		"metrics_tls_cert: "+certFile+"\nmetrics_tls_key: "+keyFile+"\n"+
		"metrics_client_ca: "+empty+"\n")
	var reported error
	m.serve(context.Background(), func(err error) { reported = err }, func(addr string) {
		t.Fatalf("it listened on %s with an unusable client CA", addr)
	})
	if reported == nil {
		t.Fatal("an unusable client CA was not reported")
	}
}

// The endpoint stops with the agent rather than outliving it.
func TestTheEndpointStopsWithTheContext(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	certFile, keyFile := ca.serving(t, dir, "node")
	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n"+
		"metrics_tls_cert: "+certFile+"\nmetrics_tls_key: "+keyFile+"\n")

	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		m.serve(ctx, func(err error) { t.Errorf("serving: %v", err) },
			func(addr string) { addrs <- addr })
		close(done)
	}()
	addr := <-addrs
	if _, err := ca.client(t, nil).Get("https://" + addr + "/v1/metrics"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the endpoint did not stop with the context")
	}
	if _, err := ca.client(t, nil).Get("https://" + addr + "/v1/metrics"); err == nil {
		t.Error("the endpoint answered after the agent stopped")
	}
}

// serveForTest starts the endpoint on an ephemeral port and answers
// with the address it landed on.
func serveForTest(t *testing.T, m *nodeMetrics) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addrs := make(chan string, 1)
	go m.serve(ctx, func(err error) { t.Errorf("serving: %v", err) },
		func(addr string) { addrs <- addr })
	select {
	case addr := <-addrs:
		return addr
	case <-time.After(10 * time.Second):
		t.Fatal("the endpoint never started")
		return ""
	}
}

// A file the scraper rejects is worse than no file: the format allows
// one declaration per family, and every sample line has to be a name
// and a number.
func checkExposition(t *testing.T, body string) {
	t.Helper()
	declared := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			declared[name]++
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		_, value, found := strings.Cut(line, " ")
		if !found {
			t.Errorf("this line carries no value: %q", line)
			continue
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			switch value {
			case "+Inf", "-Inf", "NaN":
			default:
				t.Errorf("this line's value is not a number: %q", line)
			}
		}
	}
	for name, n := range declared {
		if n > 1 {
			t.Errorf("%s is declared %d times; a scraper rejects the whole body for that", name, n)
		}
	}
	if len(declared) == 0 {
		t.Error("the exposition declared nothing")
	}
}

// A refusal's reason is the structured token, not the message. A metric
// keyed off the wording stops working the day somebody rewords it.
func TestARefusalIsCountedByItsReasonAndNotItsMessage(t *testing.T) {
	m := nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n")
	m.countRefusal(&job.Refusal{Reason: job.ReasonExpired, Detail: "by 4m"})
	m.countRefusal(ErrQueueFull)

	body := expositionOf(t, m)
	if !strings.Contains(body, `halite_node_jobs_refused_total{reason="expired"} 1`) {
		t.Errorf("a structured refusal was not counted by its reason:\n%s", body)
	}
	// Anything that is not one of SPEC 6.3's refusals is counted under
	// one label rather than under its message, which would be a series
	// per distinct error string.
	if !strings.Contains(body, `halite_node_jobs_refused_total{reason="other"} 1`) {
		t.Errorf("a full queue was not counted:\n%s", body)
	}
}

// Through the real path, not the recorder alone: a hook that is never
// called is a counter that reads zero for ever, and zero is what a
// healthy node looks like.
func TestRunningAJobMovesTheNodesCounters(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	n.metrics = nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n")

	if ret := n.executeJob(&job.Job{JID: job.ID("20260904T1"), Fun: "test.ping"}); !ret.Success {
		t.Fatalf("test.ping failed: %s", ret.Return)
	}
	if ret := n.executeJob(&job.Job{JID: job.ID("20260904T2"), Fun: "nosuch.function"}); ret.Success {
		t.Fatal("a function this build does not ship reported success")
	}

	body := expositionOf(t, n.metrics)
	for _, want := range []string{
		`halite_node_jobs_total{fun="test.ping",result="succeeded"} 1`,
		`halite_node_jobs_total{fun="nosuch.function",result="failed"} 1`,
		`halite_node_job_duration_seconds_count{fun="test.ping"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, body)
		}
	}
}

// A full queue is a drop path, and SPEC 26.2 wants a number behind every
// one of them. It was a refusal nothing counted.
func TestAFullJobQueueIsCounted(t *testing.T) {
	n := nodeWithBrokenPillar(t)
	n.metrics = nodeMetricsFor(t, "metrics_listen: '127.0.0.1:0'\n")

	e := newExecutor(n, 1, func(*job.Return) {})
	if err := e.Offer(runnableJob(t, "20260904T101010101010")); err != nil {
		t.Fatal(err)
	}
	if err := e.Offer(runnableJob(t, "20260904T101010101011")); err == nil {
		t.Fatal("the queue took a second job with a depth of one")
	}
	if got := e.Depth(); got != 1 {
		t.Errorf("depth = %d, want 1", got)
	}
	if body := expositionOf(t, n.metrics); !strings.Contains(body,
		`halite_node_jobs_refused_total{reason="other"} 1`) {
		t.Errorf("a refused job was not counted:\n%s", body)
	}
}

// expositionOf renders the registry the way the handler does, for a
// test about what is counted rather than about the listener.
func expositionOf(t *testing.T, m *nodeMetrics) string {
	t.Helper()
	var b strings.Builder
	if err := m.registry.Write(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// runnableJob is a job the replay guard admits, so that a test about
// the queue is about the queue.
func runnableJob(t *testing.T, jid string) *job.Job {
	t.Helper()
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	return &job.Job{
		JID:     job.ID(jid),
		Fun:     "test.ping",
		Nonce:   nonce,
		Expires: time.Now().Add(job.DefaultTTL),
	}
}

// testCA issues the certificates these tests need. The node's own
// node.crt cannot serve TLS -- it is issued for client authentication
// and carries no name -- which is the whole reason the endpoint takes a
// certificate the operator supplies.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	der  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, der: der}
}

func (ca *testCA) issue(t *testing.T, dir, name string, usage x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if usage == x509.ExtKeyUsageServerAuth {
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.DNSNames = []string{"localhost"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	write(t, certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	write(t, keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}

func (ca *testCA) serving(t *testing.T, dir, name string) (string, string) {
	return ca.issue(t, dir, name, x509.ExtKeyUsageServerAuth)
}

func (ca *testCA) clientPair(t *testing.T, dir, name string) tls.Certificate {
	t.Helper()
	certPath, keyPath := ca.issue(t, dir, name, x509.ExtKeyUsageClientAuth)
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func (ca *testCA) certFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "ca.crt")
	write(t, path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.der}), 0o644)
	return path
}

// client is a scraper, with or without a certificate of its own.
func (ca *testCA) client(t *testing.T, pair *tls.Certificate) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
	if pair != nil {
		cfg.Certificates = []tls.Certificate{*pair}
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
}

func write(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(fmt.Errorf("writing %s: %w", path, err))
	}
}
